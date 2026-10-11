// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Command girlrepo builds and publishes girlpages repositories: static
// trees of girlified man pages, with a website on top, that girl can fetch
// from instead of asking a model.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/NaomiAmethyst/girlpages/internal/config"
	"github.com/NaomiAmethyst/girlpages/internal/core"
	"github.com/NaomiAmethyst/girlpages/internal/crawl"
	"github.com/NaomiAmethyst/girlpages/internal/ingest"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
	"github.com/NaomiAmethyst/girlpages/internal/site"
)

// version is set by the Makefile.
var version = ""

const usage = `girlrepo builds a girlpages repository and the website on top of it 💅

usage: girlrepo [-d DIR] COMMAND [options]

commands:
  ingest PATH...     add man pages from directories, package files
                     (.deb, .pkg.tar.*, .rpm, or URLs of them) or git
                     repositories (git+https://…[#ref], or URLs ending .git)
  ingest --system    add this system's man pages, noting their packages
  crawl KIND URL     add every package with man pages from a repository:
                     apt URL SUITE, rpm URL, or pacman URL
  import SOURCE      merge another girlpages repository (a URL or a
                     directory) into this one
  girlify [NAME...]  girlify pages that aren't yet
  publish            write the section lists, search index, manifest and
                     website
  gc                 delete files nothing refers to any more
  search QUERY       search the published index
  serve              preview the site at http://localhost:1990/
  status             summarise the repository

DIR (default .) is the website; the repository lives in DIR/repo.
Run "girlrepo COMMAND --help" for a command's options.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	global := pflag.NewFlagSet("girlrepo", pflag.ContinueOnError)
	global.SetInterspersed(false)
	global.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	dir := global.StringP("dir", "d", ".", "site directory")
	configFile := global.StringP("config-file", "C", "", "girlpages config file")
	showVersion := global.BoolP("version", "V", false, "print the version")
	if err := global.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "girlrepo: %v\nTry 'girlrepo --help' 🫶\n", err)
		return 1
	}
	if *showVersion {
		fmt.Println("girlrepo", getVersion())
		return 0
	}
	rest := global.Args()
	if len(rest) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 1
	}
	cfg, err := config.Load(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "girlrepo: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := &app{dir: *dir, cfg: &cfg}
	cmds := map[string]func(context.Context, []string) error{
		"ingest":  a.ingest,
		"crawl":   a.crawl,
		"import":  a.importRepo,
		"girlify": a.girlify,
		"publish": a.publish,
		"gc":      a.gc,
		"search":  a.search,
		"serve":   a.serve,
		"status":  a.status,
	}
	fn, ok := cmds[rest[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "girlrepo: no command %q\n\n%s", rest[0], usage)
		return 1
	}
	err = fn(ctx, rest[1:])
	for _, c := range a.cleanups {
		c()
	}
	if err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		if errors.Is(err, errUsage) {
			return 1
		}
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "girlrepo: interrupted, see you later 👋")
			return 130
		}
		fmt.Fprintf(os.Stderr, "girlrepo: %v\n", err)
		return 2
	}
	return 0
}

var errUsage = errors.New("usage")

type app struct {
	dir      string
	cfg      *config.Config
	cleanups []func()
}

func (a *app) store() (*repo.Store, error) { return repo.Open(filepath.Join(a.dir, "repo")) }

func flags(name, synopsis string) *pflag.FlagSet {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SortFlags = false
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: girlrepo %s %s\n\n%s", name, synopsis, fs.FlagUsages())
	}
	return fs
}

func parse(fs *pflag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return err
		}
		// pflag doesn't say what was wrong when it's told to carry on.
		fmt.Fprintf(os.Stderr, "girlrepo %s: %v\nTry 'girlrepo %s --help' 🫶\n", fs.Name(), err, fs.Name())
		return errUsage
	}
	return nil
}

func splitSections(s string) []string {
	if s == "" {
		return nil
	}
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ':' })
}

func (a *app) ingest(ctx context.Context, args []string) error {
	fs := flags("ingest", "[options] PATH... | --system")
	system := fs.Bool("system", false, "ingest this system's man pages")
	var pkg repo.Package
	fs.StringVar(&pkg.Distro, "distro", "", "distribution the pages come from (debian, arch…)")
	fs.StringVar(&pkg.Release, "release", "", "distribution release (trixie, 42…)")
	fs.StringVar(&pkg.Name, "package", "", "package name, if it can't be told from the file name")
	fs.StringVar(&pkg.Version, "package-version", "", "package version, likewise")
	fs.StringVar(&pkg.Arch, "arch", "", "package architecture, likewise")
	sections := fs.StringP("sections", "s", "", "only these sections (comma separated)")
	jobs := fs.IntP("jobs", "j", 0, "pages to render at once (default: one per CPU)")
	dry := fs.BoolP("dry-run", "n", false, "say what would be added")
	noCache := fs.Bool("no-cache", false, "don't take conversions from girl's cache")
	quiet := fs.BoolP("quiet", "q", false, "only report problems")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *system == (fs.NArg() > 0) {
		fs.Usage()
		return errUsage
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	o := ingest.Options{Package: pkg, Sections: splitSections(*sections), Jobs: *jobs,
		DryRun: *dry, Quiet: *quiet, Log: os.Stderr}
	if !*noCache {
		o.Cache = core.CacheSet(a.cfg)
	}
	start := time.Now()
	var st ingest.Stats
	if *system {
		st, err = ingest.System(ctx, s, o)
	} else {
		var srcs []ingest.Source
		srcs, err = a.sources(ctx, fs.Args(), pkg)
		if err == nil {
			st, err = ingest.Sources(ctx, s, srcs, o)
		}
	}
	verb := "added"
	if *dry {
		verb = "would add"
	}
	fmt.Fprintf(os.Stderr, "📥 %d pages: %s %d new versions (%d already girlified locally), %d already known, %d skipped (%s)\n",
		st.Pages, verb, st.New, st.FromCache, st.Known, st.Skipped, time.Since(start).Round(time.Second))
	if err == nil && st.New > 0 && !*dry {
		fmt.Fprintln(os.Stderr, "   next: girlrepo girlify, then girlrepo publish ✨")
	}
	return err
}

// sources turns ingest arguments into sources: git repositories are
// cloned, and package URLs downloaded, into temporary directories that go
// when girlrepo exits.
func (a *app) sources(ctx context.Context, args []string, pkg repo.Package) ([]ingest.Source, error) {
	var srcs []ingest.Source
	for _, arg := range args {
		switch {
		case ingest.IsGit(arg):
			fmt.Fprintf(os.Stderr, "🐙 cloning %s\n", arg)
			src, cleanup, err := ingest.CloneGit(ctx, arg)
			if err != nil {
				return nil, err
			}
			a.cleanups = append(a.cleanups, cleanup)
			src.Package.Distro, src.Package.Release = pkg.Distro, pkg.Release
			if pkg.Name != "" && len(args) == 1 {
				src.Package.Name = pkg.Name
			}
			srcs = append(srcs, src)
		case strings.HasPrefix(arg, "https://") || strings.HasPrefix(arg, "http://"):
			dir, err := os.MkdirTemp("", "girlrepo-dl-")
			if err != nil {
				return nil, err
			}
			a.cleanups = append(a.cleanups, func() { os.RemoveAll(dir) })
			u := strings.SplitN(arg, "?", 2)[0]
			file := filepath.Join(dir, filepath.Base(u))
			if ingest.PackageKind(file) == "" {
				return nil, fmt.Errorf("%s: only package files (.deb, .pkg.tar.*, .rpm) and git repositories can be fetched", arg)
			}
			fmt.Fprintf(os.Stderr, "⬇️  downloading %s\n", arg)
			if _, err := a.fetcher().Download(ctx, arg, "", file); err != nil {
				return nil, err
			}
			srcs = append(srcs, ingest.LocalSource(file, pkg, len(args) > 1))
		default:
			srcs = append(srcs, ingest.LocalSource(arg, pkg, len(args) > 1))
		}
	}
	return srcs, nil
}

func (a *app) fetcher() *crawl.Fetcher {
	return &crawl.Fetcher{Client: &http.Client{Timeout: 30 * time.Minute}, Retries: 3, UserAgent: "girlrepo/" + getVersion()}
}

func (a *app) crawl(ctx context.Context, args []string) error {
	fs := flags("crawl", "apt URL SUITE | rpm URL | pacman URL [options]")
	components := fs.String("components", "", "apt: components to crawl (default: all the suite has)")
	arch := fs.String("arch", "", "apt: architecture (default amd64)")
	dbName := fs.String("repo", "", "pacman: database name (default: from the URL, e.g. core)")
	distro := fs.String("distro", "", "distribution to record (default: what the repository says)")
	release := fs.String("release", "", "release to record (default: what the repository says)")
	match := fs.StringP("match", "m", "", "only packages whose names match this regular expression")
	limit := fs.Int("limit", 0, "stop after this many packages")
	jobs := fs.IntP("jobs", "j", 4, "packages to download at once")
	group := fs.Int("group", 16, "packages to ingest together (pages can include pages from the same group)")
	again := fs.Bool("again", false, "ingest packages even if an earlier crawl did")
	allVersions := fs.Bool("all-versions", false, "crawl every version the repository has, not just the newest of each package")
	all := fs.Bool("all", false, "download every package if the repository can't say which have man pages")
	sections := fs.StringP("sections", "s", "", "only pages in these sections (comma separated)")
	dry := fs.BoolP("dry-run", "n", false, "list the packages that would be fetched")
	noCache := fs.Bool("no-cache", false, "don't take conversions from girl's cache")
	quiet := fs.BoolP("quiet", "q", false, "only report problems")
	if err := parse(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 2 {
		fs.Usage()
		return errUsage
	}
	var r crawl.Repository
	switch rest[0] {
	case "apt", "deb", "debian":
		if len(rest) != 3 {
			return fmt.Errorf("crawl apt wants a URL and a suite, like: crawl apt https://deb.debian.org/debian trixie")
		}
		r = &crawl.Apt{URL: rest[1], Suite: rest[2], Arch: *arch, All: *all, Components: splitSections(*components)}
	case "rpm", "yum", "dnf":
		r = &crawl.RPM{URL: rest[1], All: *all}
	case "pacman", "arch":
		r = &crawl.Pacman{URL: rest[1], Repo: *dbName}
	default:
		return fmt.Errorf("can't crawl %q repositories; try apt, rpm or pacman", rest[0])
	}
	if len(rest) > 2 && rest[0] != "apt" && rest[0] != "deb" && rest[0] != "debian" {
		fs.Usage()
		return errUsage
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	o := crawl.Options{Distro: *distro, Release: *release, Limit: *limit, Jobs: *jobs, Group: *group,
		Again: *again, AllVersions: *allVersions, DryRun: *dry, Fetcher: a.fetcher(), Log: os.Stderr, Quiet: *quiet,
		State:  filepath.Join(config.UserCacheDir(), "girlrepo-crawl", repo.Hash([]byte(absPath(s.Dir)))[:16]+".json"),
		Ingest: ingest.Options{Sections: splitSections(*sections), Log: os.Stderr}}
	if *match != "" {
		re, err := regexp.Compile(*match)
		if err != nil {
			return fmt.Errorf("--match: %w", err)
		}
		o.Match = re
	}
	if !*noCache {
		o.Ingest.Cache = core.CacheSet(a.cfg)
	}
	start := time.Now()
	st, err := crawl.Run(ctx, s, r, o)
	if !*dry {
		fmt.Fprintf(os.Stderr, "🕷️  %d of %d packages fetched (%s), %d failed: %d pages, %d new versions, %d already known (%s)\n",
			st.Fetched, st.WithMan-st.Done, humanBytes(st.Bytes), st.Failed, st.Pages.Pages, st.Pages.New, st.Pages.Known,
			time.Since(start).Round(time.Second))
		if err == nil && st.Pages.New > 0 {
			fmt.Fprintln(os.Stderr, "   next: girlrepo girlify, then girlrepo publish ✨")
		}
	}
	return err
}

func (a *app) importRepo(ctx context.Context, args []string) error {
	fs := flags("import", "[options] SOURCE")
	sections := fs.StringP("sections", "s", "", "only these sections (comma separated)")
	match := fs.StringP("match", "m", "", "only pages whose names match this regular expression")
	preferRemote := fs.Bool("prefer-remote", false, "when both have a version's variant and they differ, take theirs")
	jobs := fs.IntP("jobs", "j", 8, "pages to fetch at once")
	dry := fs.BoolP("dry-run", "n", false, "say what would be imported")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errUsage
	}
	c, err := a.remote(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	if abs, _ := filepath.Abs(c.Base); abs == absPath(s.Dir) {
		return fmt.Errorf("that's this repository")
	}
	o := repo.ImportOptions{Sections: splitSections(*sections), PreferRemote: *preferRemote,
		Jobs: *jobs, DryRun: *dry, Log: os.Stderr}
	if *match != "" {
		if o.Match, err = regexp.Compile(*match); err != nil {
			return fmt.Errorf("--match: %w", err)
		}
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	start := time.Now()
	st, err := s.Import(ctx, c, o)
	verb := "imported"
	if *dry {
		verb = "would import"
	}
	fmt.Fprintf(os.Stderr, "📥 %d pages: %d the same already; %s %d new pages, %d versions, %d variants, %d places found",
		st.Pages, st.Same, verb, st.New, st.Versions, st.Variants, st.Packages)
	if st.Conflicts > 0 {
		if *preferRemote {
			fmt.Fprintf(os.Stderr, "; %d conversions differed and theirs were taken", st.Replaced)
		} else {
			fmt.Fprintf(os.Stderr, "; %d conversions differed and ours were kept (--prefer-remote takes theirs)", st.Conflicts)
		}
	}
	if st.Failed > 0 {
		fmt.Fprintf(os.Stderr, "; %d failed", st.Failed)
	}
	fmt.Fprintf(os.Stderr, " (%s)\n", time.Since(start).Round(time.Second))
	if err == nil && !*dry && st.New+st.Versions+st.Variants+st.Packages+st.Replaced > 0 {
		fmt.Fprintln(os.Stderr, "   next: girlrepo publish ✨")
	}
	if err == nil && st.Failed > 0 {
		return fmt.Errorf("%d pages failed", st.Failed)
	}
	return err
}

// remote finds a published repository at src: the repository itself, or
// a site with it in repo/.
func (a *app) remote(ctx context.Context, src string) (*repo.Client, error) {
	c, err := repo.Find(ctx, src, 2*time.Minute, "girlrepo/"+getVersion())
	if err != nil {
		return nil, fmt.Errorf("%w; run girlrepo publish there first", err)
	}
	return c, nil
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func (a *app) girlify(ctx context.Context, args []string) error {
	fs := flags("girlify", "[options] [NAME...]")
	backend := fs.String("backend", "", "openai or claude (default from the girlpages config)")
	model := fs.String("model", "", "model to use")
	jobs := fs.IntP("jobs", "j", a.cfg.Jobs, "pages to convert at once")
	limit := fs.Int("limit", 0, "stop after this many pages")
	sections := fs.StringP("sections", "s", "", "only these sections (comma separated)")
	all := fs.Bool("all-versions", false, "girlify old versions too, not just current ones")
	force := fs.Bool("force", false, "girlify again even if already done")
	reprompt := fs.Bool("reprompt", false, "girlify again pages made with an older prompt")
	dry := fs.BoolP("dry-run", "n", false, "say what would be girlified, and estimate the tokens it'd take")
	price := fs.String("price", "", "with --dry-run, estimate the cost at IN/OUT US dollars per million tokens, e.g. 0.10/0.50")
	ratio := fs.Float64("output-ratio", ingest.OutputRatio, "with --dry-run, how long output is reckoned to be next to the original")
	noCache := fs.Bool("no-cache", false, "don't take conversions from girl's cache")
	batch := fs.String("batch", "", "send pages in batches: openrouter, anthropic or none (default from the config)")
	noBatch := fs.Bool("no-batch", false, "the same as --batch=none")
	noWait := fs.Bool("no-wait", false, "send batches off and return; the next girlify collects them")
	quiet := fs.BoolP("quiet", "q", false, "only report problems")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *backend != "" {
		a.cfg.Backend = *backend
	}
	if *model != "" {
		a.cfg.SetModel(*model)
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	o := ingest.GirlifyOptions{
		LLM:  llm.Options{ChunkSize: a.cfg.ChunkSize, ExtraInstructions: a.cfg.ExtraInstructions},
		Jobs: *jobs, Limit: *limit, Sections: splitSections(*sections), Names: fs.Args(),
		AllVersions: *all, Force: *force, Reprompt: *reprompt, DryRun: *dry, Quiet: *quiet, Log: os.Stderr,
	}
	if !*noCache && !*force {
		o.Cache = core.CacheSet(a.cfg)
	}
	if *noBatch {
		a.cfg.Batch = config.BatchNone
	} else if *batch != "" {
		a.cfg.Batch = *batch
	}
	var backendErr error
	if !*dry {
		o.Backend, backendErr = core.NewBackend(a.cfg)
	}
	var inPrice, outPrice float64
	if *price != "" {
		if !*dry {
			return fmt.Errorf("--price goes with --dry-run")
		}
		in, out, ok := strings.Cut(strings.ReplaceAll(*price, "$", ""), "/")
		var e1, e2 error
		inPrice, e1 = strconv.ParseFloat(strings.TrimSpace(in), 64)
		outPrice, e2 = strconv.ParseFloat(strings.TrimSpace(out), 64)
		if !ok || e1 != nil || e2 != nil || inPrice < 0 || outPrice < 0 {
			return fmt.Errorf("--price wants IN/OUT dollars per million tokens, like 0.10/0.50")
		}
	}
	o.OutputRatio = *ratio
	if o.Batch, err = core.NewBatcher(a.cfg, batchDir(s.Dir), o.Backend); err != nil && !*dry {
		return err
	}
	if backendErr != nil {
		if o.Batch == nil {
			return backendErr
		}
		// Batches don't need it; it's only for redoing their failures.
		fmt.Fprintf(os.Stderr, "girlrepo: %v; batch failures will wait for the next batch\n", backendErr)
		o.Backend = nil
	}
	o.NoWait = *noWait
	if a.cfg.ExtraInstructions != "" {
		fmt.Fprintln(os.Stderr, "girlrepo: heads up, your extra_instructions will shape every page in the repo")
	}
	start := time.Now()
	st, err := ingest.Girlify(ctx, s, o)
	if *dry {
		e := st.Estimate
		fmt.Fprintf(os.Stderr, "✨ %d pages to girlify", e.Pages)
		if e.Cached > 0 {
			fmt.Fprintf(os.Stderr, " (and %d already in girl's cache, for free)", e.Cached)
		}
		if st.Pending > 0 {
			fmt.Fprintf(os.Stderr, " (and %d in batches already sent)", st.Pending)
		}
		fmt.Fprintf(os.Stderr, ": %d requests\n   ~%s tokens in, ~%s out (rough guesses; thinking isn't counted)\n",
			e.Requests, humanCount(e.InputTokens), humanCount(e.OutputTokens))
		if *price != "" {
			cost := (float64(e.InputTokens)*inPrice + float64(e.OutputTokens)*outPrice) / 1e6
			fmt.Fprintf(os.Stderr, "   at $%g in / $%g out per million tokens: ~$%.2f, or ~$%.2f in batches at half price\n",
				inPrice, outPrice, cost, cost/2)
		}
		return err
	}
	fmt.Fprintf(os.Stderr, "💅 girlified %d pages, %d from the local cache, %d failed", st.Converted, st.FromCache, st.Failed)
	if n := st.Batched + st.Pending; n > 0 {
		fmt.Fprintf(os.Stderr, ", %d in batches still running", n)
	}
	if st.Remaining > 0 {
		fmt.Fprintf(os.Stderr, ", %d left for next time", st.Remaining)
	}
	fmt.Fprintf(os.Stderr, " (%s)\n", time.Since(start).Round(time.Second))
	if err == nil && st.Failed > 0 {
		return fmt.Errorf("%d pages failed", st.Failed)
	}
	return err
}

func (a *app) publish(ctx context.Context, args []string) error {
	fs := flags("publish", "[options]")
	noSite := fs.Bool("no-site", false, "only update the repository, not the website")
	base := fs.String("base", "/", "URL path the site is served from")
	siteURL := fs.String("url", "", "the site's absolute URL, for canonical links and sitemap.xml")
	gc := fs.Bool("gc", false, "collect garbage afterwards")
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	start := time.Now()
	pages, err := s.AllPages()
	if err != nil {
		return err
	}
	if n, err := s.Refresh(pages); err != nil {
		return err
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "🔄 brought %d listings up to date\n", n)
		if pages, err = s.AllPages(); err != nil {
			return err
		}
	}
	m, err := s.Publish(pages)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "📚 published %d pages in %d sections (%d girlified)\n", m.Pages, len(m.Sections), m.Variants[repo.VariantGirl])
	if !*noSite {
		st, err := site.Build(s, pages, m, site.Options{Out: a.dir, Base: *base, URL: *siteURL,
			Version: getVersion(), Log: os.Stderr})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "🌸 website: %d files written, %d unchanged, %d removed\n", st.Written, st.Unchanged, st.Removed)
	}
	if *gc {
		st, err := s.GC(false)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "🧹 removed %d unused files (%s)\n", st.Files, humanBytes(st.Bytes))
	}
	fmt.Fprintf(os.Stderr, "✨ done in %s\n", time.Since(start).Round(time.Millisecond))
	return ctx.Err()
}

func (a *app) gc(_ context.Context, args []string) error {
	fs := flags("gc", "[options]")
	dry := fs.BoolP("dry-run", "n", false, "say what would be removed")
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	st, err := s.GC(*dry)
	if err != nil {
		return err
	}
	verb := "removed"
	if *dry {
		verb = "would remove"
	}
	fmt.Fprintf(os.Stderr, "🧹 %s %d unused files (%s)\n", verb, st.Files, humanBytes(st.Bytes))
	return nil
}

func (a *app) search(ctx context.Context, args []string) error {
	fs := flags("search", "[options] QUERY...")
	limit := fs.IntP("limit", "l", 20, "how many results")
	from := fs.String("repo", "", "search this published repository URL instead")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return errUsage
	}
	base := *from
	if base == "" {
		base = filepath.Join(a.dir, "repo")
	}
	sr := &repo.Searcher{Client: repo.NewClient(base, 30*time.Second)}
	hits, err := sr.Search(ctx, strings.Join(fs.Args(), " "), *limit)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		fmt.Fprintln(os.Stderr, "nothing found, bestie 😭")
		return nil
	}
	for _, h := range hits {
		fmt.Printf("%-28s %s\n", h.Name+" ("+h.Section+")", h.Description)
	}
	return nil
}

func (a *app) serve(ctx context.Context, args []string) error {
	fs := flags("serve", "[options]")
	listen := fs.String("listen", "localhost:1990", "address to listen on")
	if err := parse(fs, args); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(a.dir, "index.html")); err != nil {
		return fmt.Errorf("no website in %s yet; run girlrepo publish first", a.dir)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: previewHandler(a.dir), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	fmt.Fprintf(os.Stderr, "🌸 previewing %s at http://%s/ (ctrl-c to stop)\n", a.dir, ln.Addr())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// previewHandler serves the site the way a static host would, including
// its 404 page.
func previewHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
		if fi, err := os.Stat(p); err != nil || fi.IsDir() && !exists(filepath.Join(p, "index.html")) {
			if b, err := os.ReadFile(filepath.Join(dir, "404.html")); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write(b)
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (a *app) status(_ context.Context, args []string) error {
	fs := flags("status", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	pages, err := s.AllPages()
	if err != nil {
		return err
	}
	versions, girl, current := 0, 0, 0
	prompts := map[int]int{}
	sections := map[string]int{}
	for _, p := range pages {
		versions += len(p.Listing.Versions)
		sections[p.Pointer.Section]++
		if p.Girlified() {
			current++
		}
		for _, v := range p.Listing.Versions {
			if info, ok := v.Variants[repo.VariantGirl]; ok {
				girl++
				prompts[info.PromptVersion]++
			}
		}
	}
	fmt.Printf("repository: %s\n", s.Dir)
	fmt.Printf("pages:      %d in %d sections\n", len(pages), len(sections))
	fmt.Printf("versions:   %d (%d girlified)\n", versions, girl)
	if len(prompts) > 0 {
		var keys []int
		for k := range prompts {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		var parts []string
		for _, k := range keys {
			label := fmt.Sprintf("v%d", k)
			if k == 0 {
				label = "unknown"
			}
			if k == llm.PromptVersion {
				label += " (current)"
			}
			parts = append(parts, fmt.Sprintf("%s: %d", label, prompts[k]))
		}
		fmt.Printf("prompts:    %s\n", strings.Join(parts, ", "))
	}
	fmt.Printf("current:    %d of %d girlified", current, len(pages))
	if len(pages) > 0 {
		fmt.Printf(" (%d%%)", current*100/len(pages))
	}
	fmt.Println()
	if m, err := s.Manifest(); err == nil {
		fmt.Printf("published:  %s, %d pages\n", m.Generated.Local().Format(time.DateTime), m.Pages)
	} else {
		fmt.Println("published:  never")
	}
	return nil
}

// batchDir is where girlify keeps track of the batches it sent for a
// repository: in the user's cache, so it never gets published.
func batchDir(repoDir string) string {
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		abs = repoDir
	}
	return filepath.Join(config.UserCacheDir(), "girlrepo-batches", repo.Hash([]byte(abs))[:16])
}

func humanCount(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func getVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
