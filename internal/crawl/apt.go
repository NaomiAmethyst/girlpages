// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package crawl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Apt is a Debian-style repository: https://deb.debian.org/debian, say,
// with a suite such as trixie.
type Apt struct {
	URL   string
	Suite string
	// Components defaults to all those the Release file lists.
	Components []string
	// Arch defaults to amd64. Architecture-independent packages are
	// included either way.
	Arch string
	// All fetches every package when the repository has no Contents
	// index to say which have man pages.
	All bool
}

func (a *Apt) dist(rel string) string {
	return join(a.URL, "dists/"+a.Suite+"/"+rel)
}

func (a *Apt) Index(ctx context.Context, f *Fetcher) (*Index, error) {
	if a.Suite == "" {
		return nil, errors.New("apt repositories need a suite, such as trixie or noble")
	}
	arch := a.Arch
	if arch == "" {
		arch = "amd64"
	}
	idx := &Index{Release: a.Suite, Compare: CompareDeb}
	rel, _, err := f.OpenFirst(ctx, a.dist("InRelease"), a.dist("Release"))
	if err != nil {
		return nil, fmt.Errorf("reading the suite's Release file: %w", err)
	}
	fields := releaseFields(rel)
	rel.Close()
	idx.Distro = strings.ToLower(fields["Origin"])
	if c := fields["Codename"]; c != "" {
		idx.Release = c
	}
	comps := a.Components
	if len(comps) == 0 {
		comps = strings.Fields(fields["Components"])
	}
	if len(comps) == 0 {
		comps = []string{"main"}
	}

	// Which packages have man pages. Debian splits Contents by component
	// and keeps architecture-independent files in Contents-all; older
	// Ubuntu has one Contents per architecture for the whole suite.
	withMan := map[string]bool{}
	found := false
	for _, c := range comps {
		for _, a2 := range []string{arch, "all"} {
			if err := a.contents(ctx, f, withMan, a.dist(c+"/Contents-"+a2)); err == nil {
				found = true
			} else if !errors.Is(err, ErrNotFound) {
				return nil, err
			}
		}
	}
	if !found {
		if err := a.contents(ctx, f, withMan, a.dist("Contents-"+arch)); err == nil {
			found = true
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	if !found && !a.All {
		return nil, errors.New("this repository has no Contents index to say which packages have man pages; --all downloads every package instead")
	}

	for _, c := range comps {
		base := a.dist(c + "/binary-" + arch + "/Packages")
		r, _, err := f.openIndex(ctx, base+".xz", base+".gz", base)
		if err != nil {
			return nil, fmt.Errorf("component %s: %w", c, err)
		}
		err = stanzas(r, func(s map[string]string) {
			idx.Total++
			name := s["Package"]
			if found && !withMan[name] {
				return
			}
			size, _ := strconv.ParseInt(s["Size"], 10, 64)
			idx.Packages = append(idx.Packages, Package{Name: name, Version: s["Version"], Arch: s["Architecture"],
				URL: join(a.URL, s["Filename"]), SHA256: s["SHA256"], Size: size})
		})
		r.Close()
		if err != nil {
			return nil, fmt.Errorf("component %s: %w", c, err)
		}
	}
	return idx, nil
}

// contents adds packages with English man pages from a Contents index.
func (a *Apt) contents(ctx context.Context, f *Fetcher, withMan map[string]bool, base string) error {
	r, _, err := f.openIndex(ctx, base+".gz", base+".xz", base)
	if err != nil {
		return err
	}
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "/man") {
			continue // most lines; skip them cheaply
		}
		// "path   section/pkg,section/pkg2"; paths may contain spaces.
		i := strings.LastIndexAny(line, " \t")
		if i < 0 {
			continue
		}
		if !IsManPath(strings.TrimSpace(line[:i])) {
			continue
		}
		for _, q := range strings.Split(line[i+1:], ",") {
			withMan[q[strings.LastIndexByte(q, '/')+1:]] = true
		}
	}
	return sc.Err()
}

// releaseFields reads the top-level fields of a Release or InRelease file.
func releaseFields(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		l := sc.Text()
		if l == "" || l[0] == ' ' || strings.HasPrefix(l, "-----") {
			continue
		}
		if k, v, ok := strings.Cut(l, ":"); ok && out[k] == "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// stanzas calls fn for each paragraph of a Debian control-style file.
func stanzas(r io.Reader, fn func(map[string]string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	cur := map[string]string{}
	last := ""
	for sc.Scan() {
		l := sc.Text()
		switch {
		case l == "":
			if len(cur) > 0 {
				fn(cur)
				cur = map[string]string{}
			}
		case l[0] == ' ' || l[0] == '\t':
			if last != "" {
				cur[last] += "\n" + l
			}
		default:
			k, v, ok := strings.Cut(l, ":")
			if ok {
				last = k
				cur[k] = strings.TrimSpace(v)
			}
		}
	}
	if len(cur) > 0 {
		fn(cur)
	}
	return sc.Err()
}
