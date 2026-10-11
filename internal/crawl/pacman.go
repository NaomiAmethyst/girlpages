// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package crawl

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// Pacman is an Arch-style repository: the directory holding the database,
// such as https://geo.mirror.pkgbuild.com/core/os/x86_64.
type Pacman struct {
	URL string
	// Repo is the database's name; it defaults to the directory two above
	// URL (core, for …/core/os/x86_64).
	Repo string
}

func (p *Pacman) name() string {
	if p.Repo != "" {
		return p.Repo
	}
	parts := strings.Split(strings.TrimRight(p.URL, "/"), "/")
	if len(parts) >= 3 && parts[len(parts)-2] == "os" {
		return parts[len(parts)-3]
	}
	return parts[len(parts)-1]
}

// Index reads the .files database, which holds each package's desc and
// file list.
func (p *Pacman) Index(ctx context.Context, f *Fetcher) (*Index, error) {
	db := p.name()
	r, u, err := f.openIndex(ctx, join(p.URL, db+".files"), join(p.URL, db+".files.tar.gz"),
		join(p.URL, db+".files.tar.zst"))
	if err != nil {
		return nil, fmt.Errorf("reading the %s files database: %w", db, err)
	}
	defer r.Close()
	type entry struct {
		desc   map[string][]string
		hasMan bool
	}
	byDir := map[string]*entry{}
	var order []string
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", u, err)
		}
		dir, file := path.Split(strings.TrimSuffix(h.Name, "/"))
		if file != "desc" && file != "files" {
			continue
		}
		e := byDir[dir]
		if e == nil {
			e = &entry{}
			byDir[dir] = e
			order = append(order, dir)
		}
		b, err := io.ReadAll(io.LimitReader(tr, 64<<20))
		if err != nil {
			return nil, err
		}
		fields := pacmanFields(string(b))
		if file == "desc" {
			e.desc = fields
			continue
		}
		for _, f := range fields["%FILES%"] {
			if IsManPath(f) {
				e.hasMan = true
				break
			}
		}
	}
	idx := &Index{Distro: "arch", Release: "rolling", Compare: CompareRPM}
	for _, dir := range order {
		e := byDir[dir]
		if e.desc == nil {
			continue
		}
		idx.Total++
		if !e.hasMan {
			continue
		}
		get := func(k string) string {
			if v := e.desc[k]; len(v) > 0 {
				return v[0]
			}
			return ""
		}
		size, _ := strconv.ParseInt(get("%CSIZE%"), 10, 64)
		idx.Packages = append(idx.Packages, Package{Name: get("%NAME%"), Version: get("%VERSION%"), Arch: get("%ARCH%"),
			URL: join(p.URL, get("%FILENAME%")), SHA256: get("%SHA256SUM%"), Size: size})
	}
	return idx, nil
}

func pacmanFields(s string) map[string][]string {
	out := map[string][]string{}
	key := ""
	for _, l := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(l, "%") && strings.HasSuffix(l, "%"):
			key = l
		case l == "":
			key = ""
		case key != "":
			out[key] = append(out[key], l)
		}
	}
	return out
}
