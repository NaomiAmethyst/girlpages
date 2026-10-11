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
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/NaomiAmethyst/girlpages/internal/config"
	"github.com/NaomiAmethyst/girlpages/internal/core"
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
  ingest PATH...     add man pages from directories or package files
                     (.deb, .pkg.tar.*, .rpm)
  ingest --system    add this system's man pages, noting their packages
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
	if err := fn(ctx, rest[1:]); err != nil {
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
	dir string
	cfg *config.Config
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
	keep := fs.Bool("keep-current", false, "don't make these the current versions (for older releases)")
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
	o := ingest.Options{Package: pkg, KeepCurrent: *keep, Sections: splitSections(*sections), Jobs: *jobs,
		DryRun: *dry, Quiet: *quiet, Log: os.Stderr}
	if !*noCache {
		o.Cache = core.CacheSet(a.cfg)
	}
	start := time.Now()
	var st ingest.Stats
	if *system {
		st, err = ingest.System(ctx, s, o)
	} else {
		st, err = ingest.Paths(ctx, s, fs.Args(), o)
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

func (a *app) girlify(ctx context.Context, args []string) error {
	fs := flags("girlify", "[options] [NAME...]")
	backend := fs.String("backend", "", "openai or claude (default from the girlpages config)")
	model := fs.String("model", "", "model to use")
	jobs := fs.IntP("jobs", "j", a.cfg.Jobs, "pages to convert at once")
	limit := fs.Int("limit", 0, "stop after this many pages")
	sections := fs.StringP("sections", "s", "", "only these sections (comma separated)")
	all := fs.Bool("all-versions", false, "girlify old versions too, not just current ones")
	force := fs.Bool("force", false, "girlify again even if already done")
	dry := fs.BoolP("dry-run", "n", false, "say what would be girlified")
	noCache := fs.Bool("no-cache", false, "don't take conversions from girl's cache")
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
		AllVersions: *all, Force: *force, DryRun: *dry, Quiet: *quiet, Log: os.Stderr,
	}
	if !*noCache && !*force {
		o.Cache = core.CacheSet(a.cfg)
	}
	if !*dry {
		if o.Backend, err = core.NewBackend(a.cfg); err != nil {
			return err
		}
	}
	if a.cfg.ExtraInstructions != "" {
		fmt.Fprintln(os.Stderr, "girlrepo: heads up, your extra_instructions will shape every page in the repo")
	}
	start := time.Now()
	st, err := ingest.Girlify(ctx, s, o)
	if *dry {
		fmt.Fprintf(os.Stderr, "✨ %d pages to girlify\n", st.Remaining)
		return err
	}
	fmt.Fprintf(os.Stderr, "💅 girlified %d pages, %d from the local cache, %d failed", st.Converted, st.FromCache, st.Failed)
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
	m, err := s.Publish(pages)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "📚 published %d pages in %d sections (%d girlified)\n", m.Pages, len(m.Sections), m.Girlified)
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
	sections := map[string]int{}
	for _, p := range pages {
		versions += len(p.Listing.Versions)
		sections[p.Pointer.Section]++
		if p.Girlified() {
			current++
		}
		for _, v := range p.Listing.Versions {
			if _, ok := v.Variants[repo.VariantGirl]; ok {
				girl++
			}
		}
	}
	fmt.Printf("repository: %s\n", s.Dir)
	fmt.Printf("pages:      %d in %d sections\n", len(pages), len(sections))
	fmt.Printf("versions:   %d (%d girlified)\n", versions, girl)
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
