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
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/NaomiAmethyst/girlpages/internal/bulk"
	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/config"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/pager"
	"github.com/NaomiAmethyst/girlpages/internal/render"
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
	all, apropos, whatis, where, local           bool
	sections, manPath, locale, pager, configFile string
	help, version                                bool
	bulk, update, force, dryRun, prune, quiet    bool
	jobs, limit                                  int
	refresh, cachedOnly, raw, doctor             bool
	color, backend, model                        string
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
	fs.BoolVarP(&f.quiet, "quiet", "q", false, "")
	fs.IntVarP(&f.jobs, "jobs", "j", 0, "")
	fs.IntVar(&f.limit, "limit", 0, "")
	fs.BoolVar(&f.refresh, "refresh", false, "")
	fs.BoolVar(&f.cachedOnly, "cached-only", false, "")
	fs.BoolVar(&f.raw, "raw", false, "")
	fs.BoolVar(&f.doctor, "doctor", false, "")
	fs.StringVar(&f.color, "color", "auto", "")
	fs.StringVar(&f.backend, "backend", "", "")
	fs.StringVar(&f.model, "model", "", "")

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
       girl --bulk [options]   girlify every page on the system

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
      --cached-only       never call the LLM; only show cached pages
      --raw               print the stored markup instead of rendering it
      --color=WHEN        auto, always or never (default auto; NO_COLOR works)
      --backend=NAME      auto, openai or claude
      --model=MODEL       model to ask
      --doctor            show config, backend and cache status

bulk options:
      --bulk              convert every man page (incrementally)
      --update            refresh changed pages; with a previous full --bulk,
                          new pages too (this is what the hooks run)
      --force             reconvert pages even if they're up to date
      --prune             drop cache entries for pages that no longer exist
  -n, --dry-run           list what would be converted, convert nothing
  -j, --jobs=N            LLM requests at once (default from config, 4)
      --limit=N           stop after the first N pages
  -q, --quiet             only report errors

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

	if f.apropos || f.whatis || f.where {
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
	if f.pager != "" {
		cfg.Pager = f.pager
	}
	caches := cacheSet(&cfg)
	mo := manpage.Options{ManPath: f.manPath, Locale: f.locale, Sections: f.sections, All: f.all}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if f.doctor {
		return doctor(&cfg, caches)
	}
	if f.bulk || f.update {
		return runBulk(ctx, f, &cfg, caches, mo, rest)
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "What manual page do you want, bestie? 💅\nFor example, try 'girl girl'.")
		return exitUsage
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
		return exitNotFound
	}

	var backend llm.Backend
	backendFor := func() (llm.Backend, error) {
		if backend == nil {
			b, err := newBackend(&cfg)
			if err != nil {
				return nil, err
			}
			backend = b
		}
		return backend, nil
	}

	ui := newUI(f)
	var out strings.Builder
	shown := 0
	for _, p := range pages {
		markup, title, stale, err := getPage(ctx, p, f, &cfg, caches, mo, backendFor, ui)
		if err != nil {
			if ctx.Err() != nil {
				return exitFailed
			}
			fmt.Fprintf(os.Stderr, "girl: couldn't girlify %s 😭 %v\n", p.Title(), err)
			fmt.Fprintf(os.Stderr, "      (plain old 'man %s %s' still works)\n", p.Section, p.Name)
			status = max(status, exitFailed)
			continue
		}
		if f.raw {
			out.WriteString(markup)
		} else {
			if shown > 0 {
				out.WriteString("\n")
			}
			if title == "" {
				title = strings.ToUpper(p.Name) + "(" + p.Section + ")"
			}
			out.WriteString(render.Render(markup, render.Options{
				Width:  ui.width,
				Color:  ui.color,
				Title:  title,
				Footer: "girlpages " + getVersion(),
				Stale:  stale,
			}))
		}
		shown++
	}
	if shown == 0 {
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

// getPage returns the markup and title for a page, from the cache if
// possible, and whether the cached copy is out of date.
func getPage(ctx context.Context, p manpage.Page, f flags, cfg *config.Config, caches cache.Set,
	mo manpage.Options, backendFor func() (llm.Backend, error), ui *ui) (string, string, bool, error) {
	if !f.refresh {
		if markup, e, fresh := caches.Lookup(p.Path); markup != "" {
			return markup, e.Title, !fresh, nil
		}
	}
	text, err := manpage.Text(ctx, p.Path, mo)
	if err != nil {
		return "", "", false, err
	}
	hash := cache.Hash(text)
	title := manpage.HeaderTitle(text)
	dir := caches.Writable()
	entry := &cache.Entry{Source: p.Path, Name: p.Name, Section: p.Section, Title: title, Hash: hash,
		PromptVersion: llm.PromptVersion, Generated: time.Now().UTC().Truncate(time.Second)}
	if !f.refresh {
		if markup, ok := caches.Object(hash); ok {
			// Same text as a page we've already done (an alias, usually).
			if entry.Stamp() == nil {
				_ = dir.PutEntry(entry)
			}
			return markup, title, false, nil
		}
	}
	if f.cachedOnly {
		return "", "", false, errors.New("it isn't cached yet and --cached-only is on")
	}
	b, err := backendFor()
	if err != nil {
		return "", "", false, err
	}
	name := title
	if name == "" {
		name = p.Title()
	}
	stopSpin := ui.spin(name, b)
	markup, err := llm.Convert(ctx, b, name, text, llm.Options{
		ChunkSize:         cfg.ChunkSize,
		Parallel:          cfg.Jobs,
		ExtraInstructions: cfg.ExtraInstructions,
		Progress:          ui.progress,
	})
	stopSpin()
	if err != nil {
		return "", "", false, err
	}
	entry.Backend, entry.Model = b.Name(), b.Model()
	if err := dir.PutObject(hash, markup); err != nil {
		fmt.Fprintf(os.Stderr, "girl: couldn't save to cache: %v\n", err)
	} else if err := entry.Stamp(); err == nil {
		if err := dir.PutEntry(entry); err != nil {
			fmt.Fprintf(os.Stderr, "girl: couldn't save to cache: %v\n", err)
		}
	}
	return markup, title, false, nil
}

func runBulk(ctx context.Context, f flags, cfg *config.Config, caches cache.Set, mo manpage.Options, rest []string) int {
	sections := splitList(f.sections)
	// "girl --bulk 1 8" is a friendlier spelling of -s 1,8.
	for _, a := range rest {
		if !manpage.IsSection(a) {
			fmt.Fprintf(os.Stderr, "girl: --bulk takes sections, not %q\n", a)
			return exitUsage
		}
		sections = append(sections, a)
	}
	var b llm.Backend
	if !f.dryRun {
		var err error
		if b, err = newBackend(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "girl: %v\n", err)
			return exitFailed
		}
	}
	mo.Sections = ""
	_, err := bulk.Run(ctx, bulk.Options{
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
	}, caches, b, os.Stderr)
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

func newBackend(cfg *config.Config) (llm.Backend, error) {
	name, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	switch name {
	case config.BackendClaude:
		return &llm.Claude{Command: cfg.Claude.Command, ModelName: cfg.Claude.Model, Args: cfg.Claude.Args,
			Timeout: cfg.Claude.Timeout.Duration}, nil
	default:
		key, err := cfg.OpenAIKey()
		if err != nil {
			return nil, err
		}
		return &llm.OpenAI{
			BaseURL:     cfg.OpenAI.BaseURL,
			APIKey:      key,
			ModelName:   cfg.OpenAI.Model,
			Temperature: cfg.OpenAI.Temperature,
			MaxTokens:   cfg.OpenAI.MaxTokens,
			Headers:     cfg.OpenAI.Headers,
			Client:      &http.Client{Timeout: cfg.OpenAI.Timeout.Duration},
			Retries:     3,
		}, nil
	}
}

func cacheSet(cfg *config.Config) cache.Set {
	var s cache.Set
	for _, d := range cfg.ReadCacheDirs() {
		s = append(s, cache.Dir(d))
	}
	return s
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

func doctor(cfg *config.Config, caches cache.Set) int {
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
	if b, err := newBackend(cfg); err != nil {
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
	fmt.Printf("jobs %d, chunk size %d\n\n", cfg.Jobs, cfg.ChunkSize)
	fmt.Println("caches:")
	for i, d := range caches {
		pages, objects := 0, map[string]bool{}
		_ = d.Entries(func(e *cache.Entry) error { pages++; objects[e.Hash] = true; return nil })
		role := "read"
		if i == 0 {
			role = "read/write"
		}
		extra := ""
		if _, err := os.Stat(d.MarkerPath()); err == nil {
			extra = ", full bulk done"
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
	u := &ui{
		tty:    term.IsTerminal(int(os.Stdout.Fd())),
		errTTY: term.IsTerminal(int(os.Stderr.Fd())),
		width:  manpage.Width,
		quiet:  f.quiet,
	}
	if w, err := strconv.Atoi(os.Getenv("MANWIDTH")); err == nil && w > 0 {
		u.width = w
	} else if u.tty {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			// Long lines of chatty prose are hard to read; man pages
			// traditionally top out around here anyway.
			u.width = min(w, 100)
		}
	}
	switch f.color {
	case "always", "yes", "force":
		u.color = render.DetectColor()
		if u.color == render.NoColor {
			u.color = render.Color256
		}
	case "never", "no", "none":
		u.color = render.NoColor
	default:
		if u.tty {
			u.color = render.DetectColor()
		}
	}
	return u
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
