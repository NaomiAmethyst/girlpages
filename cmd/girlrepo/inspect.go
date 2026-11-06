// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/NaomiAmethyst/girlpages/internal/inspect"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

const inspectUsage = `girlrepo inspect looks around a repository, on its own terms, local or remote 🔎

usage: girlrepo [-d DIR] inspect COMMAND [--repo REPO] [options] [ADDRESS]

commands:
  ls [ADDRESS]        what's under something (--follow: further under)
  get [ADDRESS]       its JSON, as the repository stores it
  describe [ADDRESS]  everything about it, for reading
  cat [ADDRESS]       the main thing it holds: a page's text, an index's entries
  check [ADDRESS]     whether it's right (-r: and everything under it)

REPO is a directory or a URL, of the repository or of a site with one in repo/;
without it, DIR's. An ADDRESS is what ls shows, and / is the top of it all:

  chapters/3  chapters/3/shards/2  sections/3ssl
  ls(1)  ls(1)@9.12  ls(1)/girl  ls(1)@9.12/girl/generations/1  ls(1)/girl/headings/NAME
  index/words  index/words/grep  index/names/12  index/packages/coreutils
  packs/2  sources/debian/trixie

or a file's path in the repository, like page/page-ab/<sha>.json.
`

func (a *app) inspect(ctx context.Context, args []string) error {
	cmds := map[string]func(context.Context, []string) error{
		"ls": a.inspectLs, "get": a.inspectGet, "describe": a.inspectDescribe, "cat": a.inspectCat, "check": a.inspectCheck,
	}
	if len(args) == 0 || cmds[args[0]] == nil {
		fmt.Fprint(os.Stderr, inspectUsage)
		if len(args) > 0 && args[0] != "--help" && args[0] != "-h" && args[0] != "help" {
			return errUsage
		}
		return pflag.ErrHelp
	}
	return cmds[args[0]](ctx, args[1:])
}

// inspectFlags are the flags every inspect command has.
func (a *app) inspectFlags(name string) (*pflag.FlagSet, *string, *time.Duration) {
	fs := flags("inspect "+name, "[--repo REPO] [options] [ADDRESS]")
	source := fs.String("repo", "", "the repository: a directory or a URL (default: -d's)")
	timeout := fs.Duration("timeout", time.Minute, "how long a request may take")
	return fs, source, timeout
}

// inspectOpen opens the repository a command's for.
func (a *app) inspectOpen(ctx context.Context, source string, timeout time.Duration) (*inspect.Repo, error) {
	if source == "" {
		source = a.dir
	}
	c, err := repo.Find(ctx, source, timeout, "girlrepo-inspect/"+version)
	if err != nil {
		return nil, err
	}
	return inspect.New(c), nil
}

// inspectNode opens the repository and finds what the arguments address.
func (a *app) inspectNode(ctx context.Context, fs *pflag.FlagSet, source string, timeout time.Duration) (*inspect.Repo, *inspect.Node, error) {
	if fs.NArg() > 1 {
		fs.Usage()
		return nil, nil, errUsage
	}
	r, err := a.inspectOpen(ctx, source, timeout)
	if err != nil {
		return nil, nil, err
	}
	n, err := r.Resolve(ctx, fs.Arg(0))
	if err != nil {
		return nil, nil, err
	}
	return r, n, nil
}

// wholeRepo reports whether going so deep under a node could be most of a
// repository.
func wholeRepo(n *inspect.Node, depth int) bool {
	return (n.Kind == "manifest" && depth > 1) || (n.Kind == "group" && depth > 0)
}

func (a *app) inspectLs(ctx context.Context, args []string) error {
	fs, source, timeout := a.inspectFlags("ls")
	follow := fs.Int("follow", 0, "go this many levels further under it (1, if not said)")
	fs.Lookup("follow").NoOptDefVal = "1"
	asJSON := fs.Bool("json", false, "a line of JSON for each")
	if err := parse(fs, args); err != nil {
		return err
	}
	_, n, err := a.inspectNode(ctx, fs, *source, *timeout)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	last := time.Now()
	return inspect.Follow(ctx, n, *follow, func(c *inspect.Node) error {
		var err error
		if *asJSON {
			err = json.NewEncoder(w).Encode(map[string]any{"address": c.Addr, "kind": c.Kind, "file": c.File, "summary": c.Summary})
		} else {
			err = inspect.Line(w, c)
		}
		// As it's found, however long the rest takes.
		if time.Since(last) > 200*time.Millisecond {
			last = time.Now()
			w.Flush()
		}
		return err
	})
}

func (a *app) inspectGet(ctx context.Context, args []string) error {
	fs, source, timeout := a.inspectFlags("get")
	raw := fs.Bool("raw", false, "its bytes exactly as they're stored")
	follow := fs.Int("follow", 0, "what's this many levels under it too, each as a line of JSON saying what it is (1, if not said)")
	fs.Lookup("follow").NoOptDefVal = "1"
	all := fs.Bool("all", false, "with --follow, go on even if it could be most of the repository")
	if err := parse(fs, args); err != nil {
		return err
	}
	r, n, err := a.inspectNode(ctx, fs, *source, *timeout)
	if err != nil {
		return err
	}
	if wholeRepo(n, *follow) && !*all {
		return fmt.Errorf("that could be most of the repository: go further in, or use --all")
	}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	if *follow > 0 {
		return r.Dump(ctx, n, *follow, func(addr, kind, file string, doc []byte) error {
			if !json.Valid(doc) {
				return fmt.Errorf("%s isn't JSON", addr)
			}
			var line bytes.Buffer
			enc := json.NewEncoder(&line)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(map[string]any{"address": addr, "kind": kind, "file": file, "value": json.RawMessage(doc)}); err != nil {
				return err
			}
			_, err := w.Write(line.Bytes())
			return err
		})
	}
	doc, err := n.JSON(ctx)
	if err != nil {
		return err
	}
	if *raw {
		_, err := w.Write(doc)
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, doc, "", "  "); err != nil {
		return fmt.Errorf("%s: %w", n.Addr, err)
	}
	pretty.WriteByte('\n')
	_, err = w.Write(bytes.TrimRight(pretty.Bytes(), "\n"))
	w.WriteByte('\n')
	return err
}

func (a *app) inspectDescribe(ctx context.Context, args []string) error {
	fs, source, timeout := a.inspectFlags("describe")
	if err := parse(fs, args); err != nil {
		return err
	}
	_, n, err := a.inspectNode(ctx, fs, *source, *timeout)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	return n.Describe(ctx, w)
}

func (a *app) inspectCat(ctx context.Context, args []string) error {
	fs, source, timeout := a.inspectFlags("cat")
	if err := parse(fs, args); err != nil {
		return err
	}
	_, n, err := a.inspectNode(ctx, fs, *source, *timeout)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	return n.Cat(ctx, w)
}

func (a *app) inspectCheck(ctx context.Context, args []string) error {
	fs, source, timeout := a.inspectFlags("check")
	recursive := fs.BoolP("recursive", "r", false, "and everything under it")
	sample := fs.Int("sample", 0, "with -r, only this many of what's under anything, at random")
	all := fs.Bool("all", false, "with -r, check all of a remote repository, however many requests that takes")
	packs := fs.Bool("packs", false, "with -r, go through its packs too")
	jobs := fs.IntP("jobs", "j", 0, "how many to check at once (default: one per CPU, or 8 remotely)")
	as := fs.String("as", "", "check a file of yours as this kind of file, not anything in a repository ("+inspectKinds()+")")
	quiet := fs.BoolP("quiet", "q", false, "only report problems")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return errUsage
	}
	var mu sync.Mutex
	problem := func(n *inspect.Node, warn bool, message string) {
		mu.Lock()
		defer mu.Unlock()
		tag := "💔"
		if warn {
			tag = "⚠️ "
		}
		name := n.Addr
		if n.File != "" && n.File != n.Addr {
			name += " (" + n.File + ")"
		}
		fmt.Fprintf(os.Stdout, "%s %s: %s\n", tag, name, message)
	}

	var r *inspect.Repo
	var n *inspect.Node
	if *as != "" {
		if fs.NArg() != 1 {
			return fmt.Errorf("--as checks a file: name it")
		}
		data, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return err
		}
		r = inspect.New(repo.NewClient(filepath.Dir(fs.Arg(0)), *timeout))
		if n, err = r.AsFile(*as, fs.Arg(0), data); err != nil {
			return err
		}
	} else {
		var err error
		if r, n, err = a.inspectNode(ctx, fs, *source, *timeout); err != nil {
			return err
		}
		if *recursive && r.C.Remote() && *sample == 0 && !*all && (n.Kind == "manifest" || n.Kind == "group") {
			return fmt.Errorf("checking all of a remote repository would be a great many requests: use --sample N, or --all")
		}
	}
	if *jobs <= 0 {
		*jobs = runtime.NumCPU()
		if r.C.Remote() {
			*jobs = 8
		}
	}
	o := inspect.CheckOptions{Recursive: *recursive, Sample: *sample, Packs: *packs, Jobs: *jobs, Problem: problem}
	// What's going on, if it's going to take a while and there's someone there.
	var done atomic.Int64
	if *recursive && !*quiet && term.IsTerminal(int(os.Stderr.Fd())) {
		o.Checked = func(*inspect.Node) { done.Add(1) }
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					mu.Lock()
					fmt.Fprintf(os.Stderr, "\r\033[K🔎 %d checked", done.Load())
					mu.Unlock()
				}
			}
		}()
	}
	st := r.Check(ctx, n, o)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !*quiet {
		if o.Checked != nil {
			fmt.Fprintf(os.Stderr, "\r\033[K")
		}
		switch {
		case st.Problems == 0 && st.Warnings == 0:
			fmt.Fprintf(os.Stderr, "✨ %d checked, all good\n", st.Checked)
		default:
			fmt.Fprintf(os.Stderr, "%d checked: %d problems, %d warnings\n", st.Checked, st.Problems, st.Warnings)
		}
	}
	if st.Problems > 0 {
		return errors.New("it isn't right")
	}
	return nil
}

func inspectKinds() string {
	s := ""
	for i, k := range inspect.Kinds {
		if i > 0 {
			s += ", "
		}
		s += k
	}
	return s
}
