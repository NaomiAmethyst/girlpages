// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package crawl

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strings"
)

// FreeBSD is a FreeBSD release's distribution sets: the directory holding
// their MANIFEST, such as
// https://download.freebsd.org/releases/amd64/15.1-RELEASE. The base
// system's man pages (its commands, libraries, file formats, and section
// 9's kernel) are in base.txz, which is what's taken: third-party
// software's, from the pkg repositories, aren't, as their indexes list no
// files.
type FreeBSD struct {
	URL string
}

var freebsdRelease = regexp.MustCompile(`/([a-z0-9_]+)/([0-9]+\.[0-9]+)-(RELEASE|[A-Z]+[0-9]*)/?$`)

// freebsdSets are the distribution sets with man pages in.
var freebsdSets = map[string]bool{"base.txz": true}

// Index reads the release's MANIFEST: each set's file, SHA-256 and how
// many files it holds.
func (b *FreeBSD) Index(ctx context.Context, f *Fetcher) (*Index, error) {
	r, u, err := f.OpenFirst(ctx, join(b.URL, "MANIFEST"))
	if err != nil {
		return nil, fmt.Errorf("reading the release's MANIFEST: %w", err)
	}
	defer r.Close()
	idx := &Index{Distro: "freebsd"}
	var arch, ver string
	if m := freebsdRelease.FindStringSubmatch(strings.TrimSuffix(b.URL, "/")); m != nil {
		arch, ver = m[1], m[2]
		idx.Release = ver
		if m[3] != "RELEASE" {
			ver += "-" + m[3] // 15.1-RC1
			idx.Release = ver
		}
	}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), "\t")
		if len(fields) < 2 || !strings.HasSuffix(fields[0], ".txz") {
			continue
		}
		idx.Total++
		if !freebsdSets[fields[0]] {
			continue
		}
		idx.Packages = append(idx.Packages, Package{Name: strings.TrimSuffix(fields[0], ".txz"), Version: ver, Arch: arch,
			URL: join(b.URL, fields[0]), SHA256: fields[1]})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	if len(idx.Packages) == 0 {
		return nil, fmt.Errorf("%s lists no base.txz: is this a FreeBSD release's directory?", u)
	}
	return idx, nil
}
