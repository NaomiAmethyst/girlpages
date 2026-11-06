// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package inspect walks a repository on its own terms, local or remote:
// everything stored in one, reached by following what refers to it from
// manifest.json, as a remote repository has no directory listings.
//
// Everything's a Node, with an address (see Resolve): one of the repository's
// files, a part of one (a version, in a listing), or a view of some (a
// section, of its chapter's pages). What each kind of thing has under it, how
// to describe it, what it holds, and what makes it right are all a Node's.
package inspect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// Repo is a repository being inspected.
type Repo struct {
	C *repo.Client

	mu       sync.Mutex
	manifest *repo.Manifest
	docs     map[string][]byte
	docBytes int
	names    map[string][]repo.PageName
}

// New inspects the repository c reads.
func New(c *repo.Client) *Repo {
	return &Repo{C: c, docs: map[string][]byte{}, names: map[string][]repo.PageName{}}
}

// How much of what's been fetched is kept: moving around in a repository
// comes back to the same few files.
const keepBytes = 128 << 20

// fetch reads one of the repository's files.
func (r *Repo) fetch(ctx context.Context, rel string) ([]byte, error) {
	r.mu.Lock()
	b, ok := r.docs[rel]
	r.mu.Unlock()
	if ok {
		return b, nil
	}
	b, err := r.C.Raw(ctx, rel)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.docBytes+len(b) > keepBytes {
		r.docs, r.docBytes = map[string][]byte{}, 0
	}
	r.docs[rel], r.docBytes = b, r.docBytes+len(b)
	r.mu.Unlock()
	return b, nil
}

// get reads and decodes one of the repository's files.
func (r *Repo) get(ctx context.Context, rel string, v any) error {
	b, err := r.fetch(ctx, rel)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	return nil
}

// Manifest is the repository's manifest.json.
func (r *Repo) Manifest(ctx context.Context) (*repo.Manifest, error) {
	r.mu.Lock()
	m := r.manifest
	r.mu.Unlock()
	if m != nil {
		return m, nil
	}
	m = &repo.Manifest{}
	if err := r.get(ctx, repo.ManifestPath(), m); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.manifest = m
	r.mu.Unlock()
	return m, nil
}

// Report is told what's wrong with something: warn for what's odd, but
// allowed.
type Report func(warn bool, format string, a ...any)

// Node is something in a repository.
type Node struct {
	r *Repo
	// Kind says what it is, and Addr where it is (see Resolve).
	Kind, Addr string
	// File is the repository file it is, or "" if it's a part or a view of
	// some. Hash is what File's named after, if it is, and Bytes how big
	// what refers to it says it is, if it does.
	File  string
	Hash  string
	Bytes int64
	// Summary says what it is in a line, for listings.
	Summary string
	// View is for what's another way of looking at things elsewhere under
	// its parent (a chapter's sections): not followed.
	View bool
	// NoWalk is for what checking everything doesn't go into: what's checked
	// by getting there another way, or is too big to.
	NoWalk bool
	// big is a file not to read whole into memory (a pack).
	big bool
	// data is what it holds, if that's known already.
	data []byte

	value    func(ctx context.Context) (any, error)
	children func(ctx context.Context, yield func(*Node) error) error
	describe func(ctx context.Context, w io.Writer) error
	cat      func(ctx context.Context, w io.Writer) error
	check    func(ctx context.Context, data []byte, report Report)
}

// JSON is it as the repository has it: a file's bytes, or the part of one
// it is.
func (n *Node) JSON(ctx context.Context) ([]byte, error) {
	if n.value != nil {
		v, err := n.value(ctx)
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	if n.data != nil {
		return n.data, nil
	}
	if n.File == "" {
		return nil, fmt.Errorf("%s has nothing stored of its own", n.Addr)
	}
	return n.r.fetch(ctx, n.File)
}

// Children calls yield with what's under it.
func (n *Node) Children(ctx context.Context, yield func(*Node) error) error {
	if n.children == nil {
		return nil
	}
	return n.children(ctx, yield)
}

// Describe writes everything about it, for reading.
func (n *Node) Describe(ctx context.Context, w io.Writer) error {
	fmt.Fprintf(w, "%s: %s\n", n.Kind, n.Addr)
	if n.File != "" {
		fmt.Fprintf(w, "file: %s\n", n.File)
	}
	if n.Hash != "" {
		fmt.Fprintf(w, "hash: %s\n", n.Hash)
	}
	if n.File != "" && !n.big {
		if b, err := n.JSON(ctx); err == nil {
			fmt.Fprintf(w, "size: %s\n", size(int64(len(b))))
		} else {
			return err
		}
	} else if n.Bytes > 0 {
		fmt.Fprintf(w, "size: %s\n", size(n.Bytes))
	}
	if n.Summary != "" && !n.big {
		fmt.Fprintf(w, "%s\n", n.Summary)
	}
	if n.describe != nil {
		fmt.Fprintln(w)
		return n.describe(ctx, w)
	}
	return nil
}

// Cat writes the main thing it holds.
func (n *Node) Cat(ctx context.Context, w io.Writer) error {
	if n.cat != nil {
		return n.cat(ctx, w)
	}
	// With nothing to say of its own, what's under it says most.
	found := false
	err := n.Children(ctx, func(c *Node) error {
		found = true
		return Line(w, c)
	})
	if err != nil || found {
		return err
	}
	return n.Describe(ctx, w)
}

// Line writes a line about n, for a listing.
func Line(w io.Writer, n *Node) error {
	_, err := fmt.Fprintf(w, "%-44s  %-14s  %s\n", n.Addr, n.Kind, n.Summary)
	return err
}

// Follow calls yield with what's depth levels under n, in order, without
// going through views: 0 is what's directly under it.
func Follow(ctx context.Context, n *Node, depth int, yield func(*Node) error) error {
	return n.Children(ctx, func(c *Node) error {
		if depth <= 0 {
			return yield(c)
		}
		if c.View {
			return nil
		}
		return Follow(ctx, c, depth-1, yield)
	})
}

// valid is a hash, for making a path of: as what a repository says may be
// anything, one that can't be anything is made one that can't be there.
func valid(sha string) string {
	if repo.ValidHash(sha) {
		return sha
	}
	return strings.Repeat("0", 64)
}

// errFound stops going through something, having found what was wanted.
var errFound = errors.New("found it")

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}
