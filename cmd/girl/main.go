// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Command girl is man, but make it ✨ girly ✨: it shows man pages rewritten
// by an LLM in the voice of a Gen Z teenage girl, with colour and emoji,
// caching every page it converts.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/NaomiAmethyst/girlpages/internal/bulk"
	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/config"
	"github.com/NaomiAmethyst/girlpages/internal/core"
	"github.com/NaomiAmethyst/girlpages/internal/ingest"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/pager"
	"github.com/NaomiAmethyst/girlpages/internal/remote"
	"github.com/NaomiAmethyst/girlpages/internal/render"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
	"github.com/NaomiAmethyst/girlpages/internal/web"
)

// version is set by the Makefile.
var version = ""

// Exit statuses follow man(1).
const (
	exitOK       = 0
	exitUsage    = 1
	exitFailed   = 2
	exitNotFound = 16
)

func main() { os.Exit(run(os.Args[1:])) }

type flags struct {
	all, apropos, whatis, where, local                     bool
	sections, manPath, locale, pager, configFile           string
	help, version                                          bool
	bulk, update, force, dryRun, prune, quiet              bool
	noWait, noBatch, reprompt                              bool
	jobs, limit                                            int
	refresh, noCache, cachedOnly, raw, doctor, html, serve bool
	color, backend, model, listen, repo, batch             string
	importSrc, exportDir                                   string
	remote, versions, ngrams                               bool
	pageVersion, variant                                   string
}

func newFlagSet(f *flags) *pflag.FlagSet {
	fs := pflag.NewFlagSet("girl", pflag.ContinueOnError)
	fs.SortFlags = false
	fs.Usage = func() {}
	fs.SetOutput(new(strings.Builder))

	fs.BoolVarP(&f.all, "all", "a", false, "")
	fs.StringVarP(&f.sections, "sections", "S", "", "")
	fs.StringVarP(&f.sections, "section", "s", "", "")
	fs.BoolVarP(&f.apropos, "apropos", "k", false, "")
	fs.BoolVarP(&f.whatis, "whatis", "f", false, "")
	fs.BoolVarP(&f.where, "where", "w", false, "")
	fs.BoolVarP(&f.local, "local-file", "l", false, "")
	fs.StringVarP(&f.manPath, "manpath", "M", "", "")
	fs.StringVarP(&f.locale, "locale", "L", "", "")
	fs.StringVarP(&f.pager, "pager", "P", "", "")
	fs.StringVarP(&f.configFile, "config-file", "C", "", "")
	fs.BoolVarP(&f.help, "help", "h", false, "")
	fs.BoolVarP(&f.version, "version", "V", false, "")

	fs.BoolVar(&f.bulk, "bulk", false, "")
	fs.BoolVar(&f.update, "update", false, "")
	fs.BoolVar(&f.force, "force", false, "")
	fs.BoolVarP(&f.dryRun, "dry-run", "n", false, "")
	fs.BoolVar(&f.prune, "prune", false, "")
	fs.BoolVar(&f.noWait, "no-wait", false, "")
	fs.BoolVar(&f.noBatch, "no-batch", false, "")
	fs.BoolVar(&f.reprompt, "reprompt", false, "")
	fs.StringVar(&f.batch, "batch", "", "")
	fs.StringVar(&f.importSrc, "import", "", "")
	fs.StringVar(&f.exportDir, "export", "", "")
	fs.BoolVarP(&f.quiet, "quiet", "q", false, "")
	fs.IntVarP(&f.jobs, "jobs", "j", 0, "")
	fs.IntVar(&f.limit, "limit", 0, "")
	fs.BoolVar(&f.refresh, "refresh", false, "")
	fs.BoolVar(&f.noCache, "no-cache", false, "")
	fs.BoolVar(&f.cachedOnly, "cached-only", false, "")
	fs.BoolVar(&f.raw, "raw", false, "")
	fs.BoolVar(&f.doctor, "doctor", false, "")
	fs.BoolVar(&f.html, "html", false, "")
	fs.BoolVar(&f.serve, "serve", false, "")
	fs.StringVar(&f.listen, "listen", "localhost:1989", "")
	fs.StringVar(&f.color, "color", "auto", "")
	fs.StringVar(&f.backend, "backend", "", "")
	fs.StringVar(&f.model, "model", "", "")
	fs.StringVar(&f.repo, "repo", "", "")
	fs.BoolVarP(&f.remote, "remote", "r", false, "")
	fs.StringVar(&f.pageVersion, "page-version", "", "")
	fs.StringVar(&f.variant, "variant", "", "")
	fs.BoolVar(&f.versions, "versions", false, "")
	fs.BoolVar(&f.ngrams, "ngrams", false, "")

	// man spells a few of these more than one way.
	fs.SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		switch name {
		case "path", "location":
			name = "where"
		case "colour":
			name = "color"
		case "config":
			name = "config-file"
		}
		return pflag.NormalizedName(name)
	})
	return fs
}

const usage = `usage: girl [options] [section] page ...
       girl -k regexp ...      search page descriptions (like apropos)
       girl -f page ...        one-line descriptions (like whatis)
       girl -r [section] page  read a page from the repo, installed or not
       girl --bulk [options]   girlify every page on the system
       girl --import SOURCE    fill the cache from a girlpages repository
       girl --export DIR       write the cache out as a girlpages repository

girl is man, but she's ✨ that girl ✨. Pages are rewritten by an LLM with
every detail kept, then cached so the next read is instant. 💅

man options:
  -a, --all               show every matching page, not just the first
  -s, -S, --sections=LIST only look in these sections (colon or comma list)
  -k, --apropos           search names and descriptions, via man -k
  -f, --whatis            show one-line descriptions, via man -f
  -w, --where, --path     print where the source pages live
  -l, --local-file        treat arguments as man page files
  -M, --manpath=PATH      search PATH instead of the default manpath
  -L, --locale=LOCALE     look for pages in this locale
  -P, --pager=PAGER       page output with PAGER (default $GIRLPAGER, $PAGER, less)
  -C, --config-file=FILE  read this config instead of the user config
  -h, --help              show this help
  -V, --version           show the version

girl options:
      --refresh           re-girlify the page even if it's cached
      --no-cache          always ask the LLM: skip the cache and repo, save nothing
      --cached-only       never call the LLM; only show cached (or repo) pages
      --raw               print the stored markup instead of rendering it
      --html              write the page as a standalone HTML document
      --color=WHEN        auto, always or never (default auto; NO_COLOR works)
      --backend=NAME      auto, openai or claude
      --model=MODEL       model to ask
      --repo=URL          fetch pages from this girlpages repository before
                          asking the model ("none" to skip the configured one)
      --doctor            show config, backend and cache status

remote options (pages from the repo, whether or not they're installed):
  -r, --remote            read pages from the repo instead of this system;
                          -k and -f search it, and -a, -s and -P work too
      --page-version=V    which version: its version number (9.7), where
                          it's from (debian, debian/trixie) or the start of
                          its hash (default the latest)
      --variant=NAME      girl (the default), man (the original) or another
      --versions          list a page's versions and variants
      --ngrams            -k finds words inside words too (dir finds mkdir)
                          (--page-version, --variant and --versions mean -r)

web options:
      --serve             browse, search and read pages in a web browser,
                          girlifying any that aren't cached when opened
      --listen=ADDR       where --serve listens (default localhost:1989)

bulk options:
      --bulk              convert every man page (incrementally)
      --update            refresh changed pages; with a previous full --bulk,
                          new pages too (this is what the hooks run)
      --force             reconvert pages even if they're up to date
      --reprompt          reconvert pages made with an older prompt
      --prune             drop cache entries for pages that no longer exist
      --batch=API         send pages in batches: openrouter, anthropic, sail or none
                          (default from the config)
      --no-batch          the same as --batch=none
      --no-wait           send batches off and return; the next run collects
                          them
  -n, --dry-run           list what would be converted, convert nothing
  -j, --jobs=N            LLM requests at once (default from config, 4)
      --limit=N           stop after the first N pages
  -q, --quiet             only report errors

repository options:
      --import=SOURCE     cache every installed page the repository (a URL or
                          directory) has, without asking a model; takes
                          sections or page names, and -j, --limit, --force
                          (fetch again, replacing what's cached), -n, -q
      --export=DIR        write every up-to-date cached page, with the package
                          it came from, into a repository in DIR/repo, ready
                          to import or serve (girlrepo -d DIR publish makes
                          the website)

Full details: girl girl (or man girl, if you're boring).
`

func run(args []string) int {
	var f flags
	fs := newFlagSet(&f)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "girl: %v\nTry 'girl --help' bestie 🫶\n", err)
		return exitUsage
	}
	if f.help {
		fmt.Print(usage)
		return exitOK
	}
	if f.version {
		fmt.Printf("girl (girlpages) %s ✨\n", getVersion())
		return exitOK
	}
	rest := fs.Args()
	if f.noCache {
		switch {
		case f.cachedOnly:
			fmt.Fprintln(os.Stderr, "girl: --no-cache or --cached-only, bestie, not both at once")
			return exitUsage
		case f.bulk || f.update || f.serve || f.importSrc != "" || f.exportDir != "" || f.doctor:
			fmt.Fprintln(os.Stderr, "girl: --no-cache is just for reading pages, bestie")
			return exitUsage
		}
	}

	if f.pageVersion != "" || f.variant != "" || f.versions {
		f.remote = true
	}
	if f.ngrams && !f.remote {
		fmt.Fprintln(os.Stderr, "girl: --ngrams is for searching the repo, bestie: girl -r -k --ngrams")
		return exitUsage
	}
	if f.remote {
		switch {
		case f.where || f.local:
			fmt.Fprintln(os.Stderr, "girl: -r reads from the repo, so there are no files for -w or -l, bestie")
			return exitUsage
		case f.bulk || f.update || f.serve || f.importSrc != "" || f.exportDir != "" || f.doctor:
			fmt.Fprintln(os.Stderr, "girl: -r is just for reading and searching pages, bestie")
			return exitUsage
		case f.html:
			fmt.Fprintln(os.Stderr, "girl: -r doesn't do --html yet, bestie; the repo's website has every page")
			return exitUsage
		}
	}

	if !f.remote && (f.apropos || f.whatis || f.where) {
		return execMan(f, rest)
	}

	cfg, err := config.Load(f.configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "girl: %v\n", err)
		return exitUsage
	}
	if f.backend != "" {
		cfg.Backend = f.backend
	}
	if f.model != "" {
		cfg.SetModel(f.model)
	}
	if f.jobs > 0 {
		cfg.Jobs = f.jobs
	}
	if f.repo != "" {
		cfg.Repo.URL = f.repo
	}
	if f.pager != "" {
		cfg.Pager = f.pager
	}
	mo := manpage.Options{ManPath: f.manPath, Locale: f.locale, Sections: f.sections, All: f.all}
	g := &core.Girl{Cfg: &cfg, Caches: core.CacheSet(&cfg), Man: mo, Repo: core.NewRepo(&cfg, getVersion())}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if f.doctor {
		return doctor(ctx, &cfg, g)
	}
	if f.importSrc != "" && f.exportDir != "" {
		fmt.Fprintln(os.Stderr, "girl: --import or --export, bestie, not both at once")
		return exitUsage
	}
	if f.exportDir != "" {
		return runExport(ctx, f, g, mo)
	}
	if f.importSrc != "" {
		return runImport(ctx, f, &cfg, g, mo, rest)
	}
	if f.bulk || f.update {
		return runBulk(ctx, f, &cfg, g, mo, rest)
	}
	if f.serve {
		return runServe(ctx, f, g)
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "What manual page do you want, bestie? 💅\nFor example, try 'girl girl'.")
		return exitUsage
	}

	if f.remote {
		return runRemote(ctx, f, &cfg, rest)
	}
	if seriesDoc(rest) {
		if f.noCache {
			fmt.Fprintln(os.Stderr, "girl: documents like that only come from the repo, so --no-cache can't skip it")
			return exitUsage
		}
		return runRemote(ctx, f, &cfg, rest)
	}

	var pages []manpage.Page
	status := exitOK
	if f.local {
		for _, a := range rest {
			p, err := manpage.NewPage(a)
			if err == nil {
				_, err = os.Stat(p.Path)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "girl: %v\n", err)
				status = exitNotFound
				continue
			}
			pages = append(pages, p)
		}
	} else {
		var missing bool
		pages, missing, err = manpage.Locate(ctx, rest, mo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "girl: %v\n", err)
			return exitFailed
		}
		if missing {
			status = exitNotFound
		}
	}
	if len(pages) == 0 {
		if g.Repo != nil && !f.local {
			fmt.Fprintf(os.Stderr, "girl: (girl -r %s looks for it in the repo, installed or not 💅)\n", strings.Join(rest, " "))
		}
		return exitNotFound
	}

	ui := newUI(f)
	var out strings.Builder
	var docs []web.DocPage
	shown := 0
	for _, p := range pages {
		r, err := g.Get(ctx, p, core.GetOptions{
			Refresh:    f.refresh,
			NoCache:    f.noCache,
			CachedOnly: f.cachedOnly,
			Started: func(b llm.Backend) func() {
				return ui.spin(core.DisplayTitle(p, ""), b)
			},
			Progress: func(done, total int) {
				if ui.progress != nil {
					ui.progress(done, total)
				}
			},
		})
		if err != nil {
			if ctx.Err() != nil {
				return exitFailed
			}
			fmt.Fprintf(os.Stderr, "girl: couldn't girlify %s 😭 %v\n", p.Title(), err)
			fmt.Fprintf(os.Stderr, "      (plain old 'man %s %s' still works)\n", p.Section, p.Name)
			status = max(status, exitFailed)
			continue
		}
		title := core.DisplayTitle(p, r.Title)
		switch {
		case f.raw:
			out.WriteString(render.StripControls(r.Markup))
		case f.html:
			docs = append(docs, web.DocPage{Page: p, Title: title, Markup: r.Markup, Entry: r.Entry, Stale: r.Stale})
		default:
			if shown > 0 {
				out.WriteString("\n")
			}
			out.WriteString(render.Render(r.Markup, render.Options{
				Width:  ui.width,
				Color:  ui.color,
				Title:  title,
				Footer: "girlpages " + getVersion(),
				Stale:  r.Stale,
			}))
		}
		shown++
	}
	if shown == 0 {
		return status
	}
	if f.html {
		if err := web.WriteDocument(os.Stdout, docs, getVersion()); err != nil {
			fmt.Fprintf(os.Stderr, "girl: %v\n", err)
			return exitFailed
		}
		return status
	}
	if ui.tty && !f.raw {
		title := pages[0].Title()
		if err := pager.Show(pager.Command(cfg.Pager), out.String(), "💅 girl page "+title); err != nil {
			fmt.Fprintf(os.Stderr, "girl: %v\n", err)
			return exitFailed
		}
	} else {
		os.Stdout.WriteString(out.String())
	}
	return status
}

// seriesDoc reports whether the arguments ask for a document in a series
// ("rfc 9110", or "rfc9110"), which only the repository has.
func seriesDoc(rest []string) bool {
	switch {
	case len(rest) == 2 && manpage.IsSeries(strings.ToLower(rest[0])) && isNumber(rest[1]):
		return true
	case len(rest) == 1:
		n, ok := strings.CutPrefix(strings.ToLower(rest[0]), "rfc")
		return ok && isNumber(n)
	}
	return false
}

func isNumber(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != "" && strings.TrimLeft(s, "0") != ""
}

// runRemote reads or searches pages in the repository, whether or not
// they're installed here.
func runRemote(ctx context.Context, f flags, cfg *config.Config, rest []string) int {
	u := strings.TrimSpace(cfg.Repo.URL)
	if u == "" || u == "none" {
		fmt.Fprintln(os.Stderr, "girl: there's no repo to read from, bestie: set [repo] url, or use --repo=URL")
		return exitUsage
	}
	c, err := repo.Find(ctx, u, cfg.Repo.Timeout.Duration, "girlpages/"+getVersion())
	if err != nil {
		fmt.Fprintf(os.Stderr, "girl: %v\n", err)
		return exitFailed
	}
	r := remote.New(c)
	if f.ngrams {
		r.Mode = repo.ModeNgrams
	}
	ui := newUI(f)
	rd := &remote.Reader{
		Repo:    r,
		Options: remote.Options{Sections: splitList(f.sections), All: f.all, Version: f.pageVersion, Variant: f.variant},
		Prog:    "girl",
		Width:   ui.width,
		Color:   ui.color,
		Footer:  "girlpages " + getVersion(),
		Raw:     f.raw,
		Page:    ui.tty,
		Pager:   cfg.Pager,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
	switch {
	case f.apropos:
		return rd.Apropos(ctx, rest, aproposLimit)
	case f.whatis:
		return rd.Whatis(ctx, rest)
	case f.versions:
		return rd.Versions(ctx, rest)
	}
	return rd.Read(ctx, rest)
}

// aproposLimit is how many pages girl -r -k lists for each search.
const aproposLimit = 100

func runServe(ctx context.Context, f flags, g *core.Girl) int {
	g.Limit = max(g.Cfg.Jobs, 1)
	srv := &web.Server{Girl: g, CachedOnly: f.cachedOnly, Version: getVersion()}
	if err := srv.ListenAndServe(ctx, f.listen, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "girl: %v\n", err)
		return exitFailed
	}
	return exitOK
}

// runImport fills the cache from a repository: a bulk run that only
// fetches.
func runImport(ctx context.Context, f flags, cfg *config.Config, g *core.Girl, mo manpage.Options, rest []string) int {
	// Sections like --bulk, and page names too.
	sections := splitList(f.sections)
	var names []string
	for _, a := range rest {
		if manpage.IsSection(a) {
			sections = append(sections, a)
		} else {
			names = append(names, a)
		}
	}
	c, err := repo.Find(ctx, f.importSrc, cfg.Repo.Timeout.Duration, "girlpages/"+getVersion())
	if err != nil {
		fmt.Fprintf(os.Stderr, "girl: %v\n", err)
		return exitFailed
	}
	if cfg.ExtraInstructions != "" && !f.quiet {
		fmt.Fprintln(os.Stderr, "girl: heads up, these pages weren't made with your extra_instructions")
	}
	mo.Sections = ""
	_, err = bulk.Run(ctx, bulk.Options{
		Sections:  sections,
		Names:     names,
		Force:     f.force,
		DryRun:    f.dryRun,
		Quiet:     f.quiet,
		Jobs:      max(cfg.Jobs, 8),
		Limit:     f.limit,
		Man:       mo,
		FetchOnly: true,
		Fetch:     core.BulkFetch(c),
	}, g.Caches, nil, os.Stderr)
	switch {
	case errors.Is(err, bulk.ErrLocked):
		fmt.Fprintln(os.Stderr, "girl: a bulk run is using the cache; try again when it's done 💅")
		return exitFailed
	case err != nil:
		fmt.Fprintf(os.Stderr, "girl: %v\n", err)
		return exitFailed
	}
	return exitOK
}

// runExport writes the cache out as a repository.
func runExport(ctx context.Context, f flags, g *core.Girl, mo manpage.Options) int {
	s, err := repo.Open(filepath.Join(f.exportDir, "repo"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "girl: %v\n", err)
		return exitFailed
	}
	mo.Sections = ""
	start := time.Now()
	st, m, err := ingest.Export(ctx, s, g.Caches, ingest.Options{Sections: splitList(f.sections), Man: mo,
		DryRun: f.dryRun, Quiet: f.quiet, Log: os.Stderr, Jobs: f.jobs})
	if err != nil {
		fmt.Fprintf(os.Stderr, "girl: %v\n", err)
		return exitFailed
	}
	if f.dryRun {
		fmt.Fprintf(os.Stderr, "📤 would export %d pages to %s\n", st.New+st.Known, s.Dir)
		return exitOK
	}
	if !f.quiet {
		fmt.Fprintf(os.Stderr, "📤 %d pages exported to %s (%d new versions); it has %d pages, %d girlified (%s)\n",
			st.Pages-st.Skipped, s.Dir, st.New, m.Pages, m.Variants[repo.VariantGirl], time.Since(start).Round(time.Second))
		fmt.Fprintf(os.Stderr, "   import it with girl --import %s or girlrepo import, or make a website with girlrepo -d %s publish ✨\n",
			f.exportDir, f.exportDir)
	}
	return exitOK
}

func runBulk(ctx context.Context, f flags, cfg *config.Config, g *core.Girl, mo manpage.Options, rest []string) int {
	sections := splitList(f.sections)
	// "girl --bulk 1 8" is a friendlier spelling of -s 1,8.
	for _, a := range rest {
		if !manpage.IsSection(a) {
			fmt.Fprintf(os.Stderr, "girl: --bulk takes sections, not %q\n", a)
			return exitUsage
		}
		sections = append(sections, a)
	}
	if f.noBatch {
		cfg.Batch = config.BatchNone
	} else if f.batch != "" {
		cfg.Batch = f.batch
	}
	var b llm.Backend
	var backendErr error
	if !f.dryRun {
		b, backendErr = core.NewBackend(cfg)
	}
	batcher, err := core.NewBatcher(cfg, bulk.BatchDir(g.Caches.Writable()), b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "girl: %v\n", err)
		return exitFailed
	}
	if backendErr != nil {
		switch {
		case batcher != nil:
			// Batches don't need it; it's only for redoing their failures.
			fmt.Fprintf(os.Stderr, "girl: %v; batch failures will wait for the next batch\n", backendErr)
		case g.Repo != nil:
			// Pages in the repo can still be had without a model.
			fmt.Fprintf(os.Stderr, "girl: %v; only fetching from the repo\n", backendErr)
		default:
			fmt.Fprintf(os.Stderr, "girl: %v\n", backendErr)
			return exitFailed
		}
		b = nil
	}
	var fetch func(context.Context, manpage.Page) (string, *repo.Generator, error)
	if g.Repo != nil && cfg.ExtraInstructions == "" {
		fetch = core.BulkFetch(g.Repo)
	}
	mo.Sections = ""
	_, err = bulk.Run(ctx, bulk.Options{
		Sections: sections,
		Update:   f.update,
		Force:    f.force,
		DryRun:   f.dryRun,
		Prune:    f.prune,
		Quiet:    f.quiet,
		Jobs:     cfg.Jobs,
		Limit:    f.limit,
		Man:      mo,
		LLM:      llm.Options{ChunkSize: cfg.ChunkSize, ExtraInstructions: cfg.ExtraInstructions},
		Fetch:    fetch,
		Reprompt: f.reprompt,
		Batch:    batcher,
		NoWait:   f.noWait,
	}, g.Caches, b, os.Stderr)
	switch {
	case errors.Is(err, bulk.ErrLocked):
		if !f.quiet {
			fmt.Fprintln(os.Stderr, "girl: another bulk run is already going, she's on it 💅")
		}
		return exitOK
	case err != nil:
		fmt.Fprintf(os.Stderr, "girl: %v\n", err)
		return exitFailed
	}
	return exitOK
}

// execMan hands searches (-k, -f, -w) straight to man.
func execMan(f flags, rest []string) int {
	args := []string{"man"}
	switch {
	case f.apropos:
		args = append(args, "-k")
	case f.whatis:
		args = append(args, "-f")
	case f.where:
		args = append(args, "-w")
	}
	if f.all {
		args = append(args, "-a")
	}
	if f.sections != "" {
		args = append(args, "-S", f.sections)
	}
	if f.locale != "" {
		args = append(args, "-L", f.locale)
	}
	if f.manPath != "" {
		args = append(args, "-M", f.manPath)
	}
	if f.local {
		args = append(args, "-l")
	}
	args = append(args, rest...)
	path, err := exec.LookPath("man")
	if err != nil {
		fmt.Fprintf(os.Stderr, "girl: %v\n", err)
		return exitFailed
	}
	err = syscall.Exec(path, args, os.Environ())
	fmt.Fprintf(os.Stderr, "girl: %v\n", err)
	return exitFailed
}

func doctor(ctx context.Context, cfg *config.Config, g *core.Girl) int {
	caches := g.Caches
	fmt.Printf("girl (girlpages) %s ✨\n\n", getVersion())
	fmt.Println("config files:")
	for _, p := range []string{config.SystemConfigPath(), config.UserConfigPath()} {
		state := "not there"
		for _, r := range cfg.Files {
			if r == p {
				state = "read ✓"
			}
		}
		if _, err := os.Stat(p); err == nil && state == "not there" {
			state = "can't read"
		}
		fmt.Printf("  %s (%s)\n", p, state)
	}
	for _, p := range cfg.Files {
		if p != config.SystemConfigPath() && p != config.UserConfigPath() {
			fmt.Printf("  %s (read ✓)\n", p)
		}
	}
	fmt.Println()
	if b, err := core.NewBackend(cfg); err != nil {
		fmt.Printf("backend: 💔 %v\n", err)
	} else {
		model := b.Model()
		if model == "" {
			model = "(its default)"
		}
		fmt.Printf("backend: %s, model %s\n", b.Name(), model)
		switch bb := b.(type) {
		case *llm.OpenAI:
			fmt.Printf("  endpoint %s\n", bb.BaseURL)
		case *llm.Claude:
			if p, err := exec.LookPath(bb.Command); err == nil {
				fmt.Printf("  command %s\n", p)
			} else {
				fmt.Printf("  command %s 💔 not found\n", bb.Command)
			}
		}
	}
	fmt.Printf("jobs %d, chunk size %d, prompt version %d\n", cfg.Jobs, cfg.ChunkSize, llm.PromptVersion)
	if kind, err := cfg.ResolveBatch(); err != nil || kind != config.BatchNone {
		fmt.Printf("batches: %s (for --bulk)", kind)
		if b, err := core.NewBatcher(cfg, "", nil); err != nil {
			fmt.Printf(" 💔 %v", err)
		} else {
			fmt.Printf(", model %s", b.Client.ModelName())
		}
		files, _ := filepath.Glob(filepath.Join(bulk.BatchDir(caches.Writable()), "*.json"))
		if len(files) > 0 {
			fmt.Printf(", %d sent and not collected yet", len(files))
		}
		fmt.Println()
	}
	fmt.Println()
	switch {
	case g.Repo == nil:
		fmt.Print("repo: none (set [repo] url to fetch ready-made pages)\n\n")
	default:
		fmt.Printf("repo: %s\n", g.Repo.Base)
		rctx, cancel := context.WithTimeout(ctx, cfg.Repo.Timeout.Duration)
		m, err := g.Repo.Manifest(rctx)
		cancel()
		if err != nil {
			fmt.Printf("  💔 %v\n", err)
		} else {
			fmt.Printf("  %d pages, %d girlified, published %s ✓\n", m.Pages, m.Variants[repo.VariantGirl], m.Generated.Local().Format(time.DateOnly))
		}
		if cfg.ExtraInstructions != "" {
			fmt.Println("  (not used: you have extra_instructions, so pages are made just for you)")
		}
		fmt.Println()
	}
	fmt.Println("caches:")
	for i, d := range caches {
		pages, objects, older := 0, map[string]bool{}, 0
		_ = d.Entries(func(e *cache.Entry) error {
			pages++
			objects[e.Hash] = true
			if e.PromptVersion < llm.PromptVersion {
				older++
			}
			return nil
		})
		role := "read"
		if i == 0 {
			role = "read/write"
		}
		extra := ""
		if _, err := os.Stat(d.MarkerPath()); err == nil {
			extra = ", full bulk done"
		}
		if older > 0 {
			extra += fmt.Sprintf(", %d pages from an older prompt (girl --bulk --reprompt redoes them)", older)
		}
		fmt.Printf("  %s (%s): %d pages, %d conversions%s\n", d, role, pages, len(objects), extra)
	}
	return exitOK
}

type ui struct {
	tty      bool // stdout is a terminal
	errTTY   bool
	width    int
	color    render.ColorMode
	quiet    bool
	progress func(done, total int)
	status   chan string
}

func newUI(f flags) *ui {
	t := remote.Detect(f.color)
	return &ui{
		tty:    t.TTY,
		errTTY: term.IsTerminal(int(os.Stderr.Fd())),
		width:  t.Width,
		color:  t.Color,
		quiet:  f.quiet,
	}
}

var spinFrames = []string{"💅", "✨", "💖", "🎀", "🦋", "🌸"}

// spin shows a little status line on stderr while a page is converted.
func (u *ui) spin(title string, b llm.Backend) func() {
	if !u.errTTY || u.quiet {
		u.progress = nil
		return func() {}
	}
	who := b.Name()
	if m := b.Model(); m != "" {
		who += " (" + m + ")"
	}
	part := ""
	partCh := make(chan string, 8)
	u.progress = func(done, total int) {
		if total > 1 {
			partCh <- fmt.Sprintf(", part %d/%d", min(done+1, total), total)
		}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(finished)
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			fmt.Fprintf(os.Stderr, "\r\x1b[K%s girlifying %s with %s%s… %ds",
				spinFrames[(i/2)%len(spinFrames)], title, who, part, int(time.Since(start).Seconds()))
			select {
			case <-done:
				fmt.Fprint(os.Stderr, "\r\x1b[K")
				return
			case p := <-partCh:
				part = p
			case <-t.C:
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ':' || r == ',' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
