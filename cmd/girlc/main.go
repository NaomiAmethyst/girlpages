// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Command girlc reads girly man pages from a published girlpages
// repository, the way man reads pages from the system: nothing to install
// but itself, no model, no cache. It reads, searches and lists the
// versions of any page the repository has.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/NaomiAmethyst/girlpages/internal/config"
	"github.com/NaomiAmethyst/girlpages/internal/remote"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// version is set by the Makefile.
var version = ""

// defaultRepo is the repository read if none is configured (set by the
// Makefile, from GIRLC_REPO).
var defaultRepo = ""

func main() { os.Exit(run(os.Args[1:])) }

type flags struct {
	all, apropos, whatis, help, version, raw, versions, ngrams bool
	sections, pager, configFile, repo, color                   string
	pageVersion, variant                                       string
	limit                                                      int
}

const usage = `usage: girlc [options] [section] page ...
       girlc -k words ...      search the repo (like apropos)
       girlc -f page ...       one-line descriptions (like whatis)
       girlc --versions page   list a page's versions and variants

girlc reads girly man pages straight from a girlpages repository, like
man but for pages that live online: nothing installed but her, no model,
no cache. 💅

options:
  -a, --all               show every page by the name, not just the first
  -s, -S, --sections=LIST only look in these sections (colon or comma list)
  -k, --apropos           search names, descriptions and pages
  -f, --whatis            show one-line descriptions
      --ngrams            -k finds words inside words too (dir finds mkdir)
      --limit=N           how many pages -k lists (default 100)
      --page-version=V    which version: its version number (9.7), where
                          it's from (debian, debian/trixie) or the start of
                          its hash (default the latest)
      --variant=NAME      girl (the default), man (the original) or another
      --versions          list a page's versions and variants
      --raw               print the stored markup instead of rendering it
      --color=WHEN        auto, always or never (default auto; NO_COLOR works)
  -P, --pager=PAGER       page output with PAGER (default $GIRLPAGER, $PAGER, less)
      --repo=URL          the repository (or a site with one in repo/) to read
  -C, --config-file=FILE  read this config instead of girl's
  -h, --help              show this help
  -V, --version           show the version

The repository is --repo, else [repo] url from girl's config, else the one
she was built with. Full details: girlc girlc (or man girlc).
`

func newFlagSet(f *flags) *pflag.FlagSet {
	fs := pflag.NewFlagSet("girlc", pflag.ContinueOnError)
	fs.SortFlags = false
	fs.Usage = func() {}
	fs.SetOutput(new(strings.Builder))
	fs.BoolVarP(&f.all, "all", "a", false, "")
	fs.StringVarP(&f.sections, "sections", "S", "", "")
	fs.StringVarP(&f.sections, "section", "s", "", "")
	fs.BoolVarP(&f.apropos, "apropos", "k", false, "")
	fs.BoolVarP(&f.whatis, "whatis", "f", false, "")
	fs.BoolVar(&f.ngrams, "ngrams", false, "")
	fs.IntVar(&f.limit, "limit", 100, "")
	fs.StringVar(&f.pageVersion, "page-version", "", "")
	fs.StringVar(&f.variant, "variant", "", "")
	fs.BoolVar(&f.versions, "versions", false, "")
	fs.BoolVar(&f.raw, "raw", false, "")
	fs.StringVar(&f.color, "color", "auto", "")
	fs.StringVarP(&f.pager, "pager", "P", "", "")
	fs.StringVar(&f.repo, "repo", "", "")
	fs.StringVarP(&f.configFile, "config-file", "C", "", "")
	fs.BoolVarP(&f.help, "help", "h", false, "")
	fs.BoolVarP(&f.version, "version", "V", false, "")
	fs.SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		switch name {
		case "colour":
			name = "color"
		case "config":
			name = "config-file"
		}
		return pflag.NormalizedName(name)
	})
	return fs
}

func run(args []string) int {
	var f flags
	fs := newFlagSet(&f)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "girlc: %v\nTry 'girlc --help' bestie 🫶\n", err)
		return remote.ExitUsage
	}
	if f.help {
		fmt.Print(usage)
		return remote.ExitOK
	}
	if f.version {
		fmt.Printf("girlc (girlpages) %s ✨\n", getVersion())
		return remote.ExitOK
	}
	rest := fs.Args()
	if n := countTrue(f.apropos, f.whatis, f.versions); n > 1 {
		fmt.Fprintln(os.Stderr, "girlc: one of -k, -f or --versions at a time, bestie")
		return remote.ExitUsage
	}
	if len(rest) == 0 {
		switch {
		case f.apropos:
			fmt.Fprintln(os.Stderr, "girlc: search for what, bestie? 💅")
		case f.whatis:
			fmt.Fprintln(os.Stderr, "girlc: whatis what, bestie? 💅")
		default:
			fmt.Fprintln(os.Stderr, "What manual page do you want, bestie? 💅\nFor example, try 'girlc ls'.")
		}
		return remote.ExitUsage
	}

	cfg, err := config.Load(f.configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "girlc: %v\n", err)
		return remote.ExitUsage
	}
	u := firstOf(f.repo, cfg.Repo.URL, defaultRepo)
	if u == "" || u == "none" {
		fmt.Fprintf(os.Stderr, "girlc: which repo, bestie? use --repo=URL, or set [repo] url in %s\n", config.UserConfigPath())
		return remote.ExitUsage
	}
	if f.pager != "" {
		cfg.Pager = f.pager
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, err := repo.Find(ctx, u, cfg.Repo.Timeout.Duration, "girlc/"+getVersion())
	if err != nil {
		fmt.Fprintf(os.Stderr, "girlc: %v\n", err)
		return remote.ExitFailed
	}
	r := remote.New(c)
	if f.ngrams {
		r.Mode = repo.ModeNgrams
	}
	t := remote.Detect(f.color)
	rd := &remote.Reader{
		Repo:    r,
		Options: remote.Options{Sections: splitList(f.sections), All: f.all, Version: f.pageVersion, Variant: f.variant},
		Prog:    "girlc",
		Width:   t.Width,
		Color:   t.Color,
		Footer:  "girlpages " + getVersion(),
		Raw:     f.raw,
		Page:    t.TTY,
		Pager:   cfg.Pager,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
	switch {
	case f.apropos:
		return rd.Apropos(ctx, rest, max(f.limit, 0))
	case f.whatis:
		return rd.Whatis(ctx, rest)
	case f.versions:
		return rd.Versions(ctx, rest)
	}
	return rd.Read(ctx, rest)
}

func countTrue(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
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
