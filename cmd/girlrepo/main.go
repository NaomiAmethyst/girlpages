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
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/NaomiAmethyst/girlpages/internal/config"
	"github.com/NaomiAmethyst/girlpages/internal/core"
	"github.com/NaomiAmethyst/girlpages/internal/crawl"
	"github.com/NaomiAmethyst/girlpages/internal/ingest"
	"github.com/NaomiAmethyst/girlpages/internal/interrupt"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/precompress"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
	"github.com/NaomiAmethyst/girlpages/internal/rfc"
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
                     directory) into this one, by its packs if it has them
  unpack [PATH]      merge packs you have (in DIR/repo/packs, or PATH)
                     into this repository
  export DIR         write the repository as packs, into a pack-only
                     repository in DIR (adding to it, if it's there)
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
	ctx, stop := interrupt.Context("girlrepo", os.Stderr)
	defer stop()
	a := &app{dir: *dir, cfg: &cfg}
	cmds := map[string]func(context.Context, []string) error{
		"ingest":  a.ingest,
		"crawl":   a.crawl,
		"import":  a.importRepo,
		"unpack":  a.unpack,
		"export":  a.export,
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
	fs := flags("crawl", "apt URL SUITE | rpm URL | pacman URL | rfc [URL] [options]")
	numbers := fs.String("rfcs", "", "rfc: only these RFCs (comma separated numbers)")
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
	if len(rest) >= 1 && rest[0] == "rfc" {
		return a.crawlRFC(ctx, rest[1:], *numbers, *limit, *jobs, *again, *dry, *quiet)
	}
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

// crawlRFC crawls the RFC Editor (or a mirror at a URL) for RFCs.
func (a *app) crawlRFC(ctx context.Context, rest []string, numbers string, limit, jobs int, again, dry, quiet bool) error {
	if len(rest) > 1 {
		return fmt.Errorf("crawl rfc takes at most a URL, like: crawl rfc %s", rfc.DefaultURL)
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	o := rfc.Options{Fetcher: a.fetcher(), Limit: limit, Jobs: jobs, Again: again, DryRun: dry, Log: os.Stderr, Quiet: quiet,
		State: filepath.Join(config.UserCacheDir(), "girlrepo-crawl", repo.Hash([]byte(absPath(s.Dir)))[:16]+"-rfc.json")}
	if len(rest) == 1 {
		o.URL = rest[0]
	}
	if numbers != "" {
		o.Numbers = map[int]bool{}
		for _, f := range strings.FieldsFunc(numbers, func(r rune) bool { return r == ',' || r == ' ' }) {
			n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(f), "rfc"))
			if err != nil {
				return fmt.Errorf("--rfcs: %q isn't an RFC number", f)
			}
			o.Numbers[n] = true
		}
	}
	start := time.Now()
	st, err := rfc.Crawl(ctx, s, o)
	if !dry {
		fmt.Fprintf(os.Stderr, "🕷️  %d RFCs fetched (%s), %d failed, %d new (%s)\n",
			st.Fetched, humanBytes(st.Bytes), st.Failed, st.New, time.Since(start).Round(time.Second))
		if err == nil && st.New > 0 {
			fmt.Fprintln(os.Stderr, "   next: girlrepo girlify -s rfc, then girlrepo publish ✨")
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
	noPacks := fs.Bool("no-packs", false, "fetch files one by one, even if it has packs")
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
	m, err := c.Manifest(ctx)
	if err != nil {
		return err
	}
	var st repo.ImportStats
	if len(m.Packs) > 0 && !*noPacks {
		// A few big files, not a request for every one.
		pr := newProgress(os.Stderr)
		s.SyncRoot = syncRoot(s.Dir)
		sst, err := s.SyncFrom(ctx, c, m, pr.report)
		if err != nil {
			return err
		}
		pr.say("📦 %s\n", synced(sst))
		st, err = s.ImportFrom(ctx, filepath.Join(sst.Dir, "tree"), sst.Changed, o)
		return reportImport(st, err, *dry, *preferRemote, start)
	}
	st, err = s.Import(ctx, c, o)
	return reportImport(st, err, *dry, *preferRemote, start)
}

// packed says what adding to a log of packs did.
func packed(st repo.PackStats) string {
	var size int64
	for _, e := range st.Packs {
		size += e.Bytes
	}
	out := fmt.Sprintf("packed %d changes into %d packs (%s)", st.Puts+st.Deletes, len(st.Packs), humanBytes(size))
	if len(st.Replaced) > 0 {
		out += fmt.Sprintf(", replacing %d tail packs", len(st.Replaced))
	}
	return out
}

// syncRoot is where a repository keeps the logs of packs it syncs from:
// in the cache, not the repository (which is published).
func syncRoot(dir string) string {
	return filepath.Join(config.UserCacheDir(), "girlrepo-sync", repo.Hash([]byte(absPath(dir)))[:16])
}

// unpack merges packs there are files of into the repository.
func (a *app) unpack(ctx context.Context, args []string) error {
	fs := flags("unpack", "[options] [PATH]")
	sections := fs.StringP("sections", "s", "", "only these sections (comma separated)")
	match := fs.StringP("match", "m", "", "only pages whose names match this regular expression")
	preferRemote := fs.Bool("prefer-remote", false, "when both have a version's variant and they differ, take theirs")
	jobs := fs.IntP("jobs", "j", 8, "pages to import at once")
	dry := fs.BoolP("dry-run", "n", false, "say what would be imported")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return errUsage
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	s.SyncRoot = syncRoot(s.Dir)
	o := repo.ImportOptions{Sections: splitSections(*sections), PreferRemote: *preferRemote,
		Jobs: *jobs, DryRun: *dry, Log: os.Stderr}
	if *match != "" {
		if o.Match, err = regexp.Compile(*match); err != nil {
			return fmt.Errorf("--match: %w", err)
		}
	}
	// Packs dropped into the repository's packs/ (but not its own log's)
	// are moved out of the way as they're unpacked; packs elsewhere (a
	// mirror's, an export) are left as they are.
	dir, move := filepath.Join(s.Dir, "packs"), true
	own := map[string]bool{}
	if fs.NArg() == 1 {
		dir, move = fs.Arg(0), false
		if fi, err := os.Stat(filepath.Join(dir, "packs")); err == nil && fi.IsDir() {
			dir = filepath.Join(dir, "packs")
		}
	}
	if abs, _ := filepath.Abs(dir); abs == absPath(filepath.Join(s.Dir, "packs")) {
		log, err := repo.ReadPackLog(s.Dir)
		if err != nil {
			return err
		}
		for _, e := range log.Packs {
			own[e.SHA256] = true
		}
		for _, r := range log.Retired {
			own[strings.TrimSuffix(filepath.Base(r.Path), ".tar.zst")] = true
		}
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	start := time.Now()
	pr := newProgress(os.Stderr)
	sst, err := s.UnpackDir(ctx, dir, own, move, pr.report)
	if errors.Is(err, repo.ErrNoPacks) {
		// Nothing's come yet (or it's all been unpacked): fine.
		pr.say("📦 no packs to unpack in %s\n", dir)
		return nil
	}
	if err != nil {
		return err
	}
	pr.say("📦 %s\n", synced(sst))
	st, err := s.ImportFrom(ctx, filepath.Join(sst.Dir, "tree"), sst.Changed, o)
	return reportImport(st, err, *dry, *preferRemote, start)
}

// export writes the repository into a pack-only repository.
func (a *app) export(ctx context.Context, args []string) error {
	fs := flags("export", "[options] DIR")
	size := fs.Int("pack-size", repo.PackLimit>>20, "how big full packs get, in MiB (fixed once there are some)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *size < 1 {
		fs.Usage()
		return errUsage
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	out := fs.Arg(0)
	if absPath(out) == absPath(s.Dir) || absPath(out) == absPath(a.dir) {
		return fmt.Errorf("export goes somewhere else: DIR becomes a pack-only repository")
	}
	start := time.Now()
	pr := newProgress(os.Stderr)
	st, err := repo.AppendPacks(ctx, out, s.Dir, repo.PackOptions{Limit: int64(*size) << 20, Progress: pr.report})
	if err != nil {
		return err
	}
	log, err := repo.ReadPackLog(out)
	if err != nil {
		return err
	}
	if err := repo.WritePackManifest(out, log.Packs); err != nil {
		return err
	}
	var total int64
	for _, e := range log.Packs {
		total += e.Bytes
	}
	if len(st.Packs) == 0 {
		pr.say("📦 %s was up to date: %d packs (%s)\n", out, len(log.Packs), humanBytes(total))
	} else {
		pr.say("📦 %s; %s has %d packs (%s) (%s)\n", packed(st), out, len(log.Packs), humanBytes(total), time.Since(start).Round(time.Second))
	}
	return nil
}

// synced says what a pack sync did.
func synced(st repo.SyncStats) string {
	if len(st.Applied) == 0 {
		return "the packs were up to date already"
	}
	return fmt.Sprintf("unpacked %d packs (%s)", len(st.Applied), humanBytes(st.Bytes))
}

// reportImport says what an import did.
func reportImport(st repo.ImportStats, err error, dry, preferRemote bool, start time.Time) error {
	verb := "imported"
	if dry {
		verb = "would import"
	}
	fmt.Fprintf(os.Stderr, "📥 %d pages: %d the same already; %s %d new pages, %d versions, %d variants, %d places found",
		st.Pages, st.Same, verb, st.New, st.Versions, st.Variants, st.Packages)
	if st.Conflicts > 0 {
		if preferRemote {
			fmt.Fprintf(os.Stderr, "; %d conversions differed and theirs were taken", st.Replaced)
		} else {
			fmt.Fprintf(os.Stderr, "; %d conversions differed and ours were kept (--prefer-remote takes theirs)", st.Conflicts)
		}
	}
	if st.Failed > 0 {
		fmt.Fprintf(os.Stderr, "; %d failed", st.Failed)
	}
	fmt.Fprintf(os.Stderr, " (%s)\n", time.Since(start).Round(time.Second))
	if err == nil && !dry && st.New+st.Versions+st.Variants+st.Packages+st.Replaced > 0 {
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
	batch := fs.String("batch", "", "send pages in batches: openrouter, anthropic, sail or none (default from the config)")
	noBatch := fs.Bool("no-batch", false, "the same as --batch=none")
	batchRequests := fs.Int("batch-requests", 0, "requests in a batch, at most (default from the config, or as many as the API takes)")
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
	if *batchRequests > 0 {
		a.cfg.BatchMaxRequests = *batchRequests
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	o := ingest.GirlifyOptions{
		LLM:  llm.Options{ChunkSize: a.cfg.ChunkSize, ExtraInstructions: a.cfg.ExtraInstructions},
		Jobs: *jobs, Limit: *limit, Sections: splitSections(*sections), Names: fs.Args(),
		AllVersions: *all, Force: *force, Reprompt: *reprompt, DryRun: *dry, Quiet: *quiet, Log: os.Stderr,
		PartsDir: filepath.Join(config.UserCacheDir(), "girlrepo-parts"),
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
		if err != nil {
			return err // (an estimate of part of it would mislead)
		}
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
	compress := fs.String("precompress", "zstd,br,gzip", "write compressed copies of files for the web server to send (or none)")
	compressJobs := fs.Int("compress-jobs", 0, "files to compress at once (default: one per CPU)")
	noPacks := fs.Bool("no-packs", false, "don't add to the repository's packs")
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := a.store()
	if err != nil {
		return err
	}
	pr := newProgress(os.Stderr)
	s.Progress = pr.report
	encodings, err := precompress.Parse(*compress)
	if err != nil {
		return err
	}
	precompress.Workers = *compressJobs
	// One publish at a time (they'd write the same website), but the
	// repository is only locked while it's read and its indexes written:
	// the website is made from what was read then, and crawls, ingests
	// and girlifies can go on meanwhile.
	unpublish, err := repo.LockFile(filepath.Join(a.dir, ".publish.lock"))
	if err != nil {
		return err
	}
	defer unpublish()
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	start := time.Now()
	// Files are compressed as they're written, and those written since
	// the last publish (by crawls, say) are found and queued meanwhile.
	root := a.dir
	if *noSite {
		root = s.Dir
	}
	q := precompress.NewQueue(encodings)
	q.Ctx = ctx
	s.Written = q.Add
	stale := make(chan error, 1)
	go func() { stale <- q.Stale(root) }()
	pages, err := s.AllPagesContext(ctx)
	if err != nil {
		return err
	}
	if n, err := s.RefreshContext(ctx, pages); err != nil {
		return err
	} else if n > 0 {
		pr.say("🔄 brought %d listings up to date\n", n)
		if pages, err = s.AllPagesContext(ctx); err != nil {
			return err
		}
	}
	if !*noPacks {
		pst, err := repo.AppendPacks(ctx, s.Dir, s.Dir, repo.PackOptions{Progress: pr.report})
		if err != nil {
			return err
		}
		if len(pst.Packs) > 0 {
			pr.say("📦 %s\n", packed(pst))
		}
	}
	m, err := s.PublishContext(ctx, pages)
	if err != nil {
		return err
	}
	pr.say("📚 published %d pages in %d sections (%d girlified)\n", m.Pages, len(m.Sections), m.Variants[repo.VariantGirl])
	unlock()
	locked = false
	if !*noSite {
		st, err := site.Build(s, pages, m, site.Options{Out: a.dir, Base: *base, URL: *siteURL,
			Version: getVersion(), Log: os.Stderr, Progress: pr.report, Written: q.Add, Ctx: ctx})
		if err != nil {
			return err
		}
		pr.say("🌸 website: %d files written, %d unchanged, %d removed\n", st.Written, st.Unchanged, st.Removed)
	}
	if *gc {
		// GC deletes, so nothing else may be writing.
		unlock, err := s.Lock()
		if err != nil {
			return err
		}
		st, err := s.GCContext(ctx, false)
		unlock()
		if err != nil {
			return err
		}
		pr.say("🧹 removed %d unused files (%s)\n", st.Files, humanBytes(st.Bytes))
	}
	// Whatever's left to compress, and copies of what's gone since
	// (pruned pages, collected garbage).
	if err := <-stale; err != nil {
		return err
	}
	q.Progress = func(done, queued int) { pr.report("compressing", done, queued) }
	if err := q.Stale(root); err != nil {
		return err
	}
	pst, err := q.Wait()
	if err != nil {
		return err
	}
	if pst.Compressed+pst.Removed > 0 {
		pr.say("🗜️  compressed %d files (%s smaller), removed %d stale copies\n",
			pst.Compressed, humanBytes(pst.Saved), pst.Removed)
	}
	pr.say("✨ done in %s\n", time.Since(start).Round(time.Millisecond))
	return ctx.Err()
}

func (a *app) gc(ctx context.Context, args []string) error {
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
	st, err := s.GCContext(ctx, *dry)
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
	ngrams := fs.Bool("ngrams", false, "find words the query's words are anywhere inside of (dir finds mkdir), not just words they start")
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
	if *ngrams {
		sr.Mode = repo.ModeNgrams
	}
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
	srv := &http.Server{Handler: site.Handler(a.dir), ReadHeaderTimeout: 10 * time.Second}
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
