// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package crawl

import (
	"archive/tar"
	"bufio"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/NaomiAmethyst/girlpages/internal/version"
)

// APK is an Alpine repository: the directory holding APKINDEX.tar.gz, such
// as https://dl-cdn.alpinelinux.org/alpine/v3.22/main/x86_64.
//
// Its index lists no files, so which packages have man pages is Alpine's
// convention: they're split into -doc packages (and the man-pages project's
// are man-pages and man-pages-posix).
type APK struct {
	URL string
}

// alpineRelease reads the release from a repository's URL: v3.22 is 3.22,
// edge is edge.
var alpineRelease = regexp.MustCompile(`/(v[0-9]+\.[0-9]+|edge)/`)

// apkHasMan says whether, by Alpine's convention, a package holds man pages.
func apkHasMan(name string) bool {
	return strings.HasSuffix(name, "-doc") || strings.HasPrefix(name, "man-pages")
}

// Index reads APKINDEX.tar.gz.
func (a *APK) Index(ctx context.Context, f *Fetcher) (*Index, error) {
	r, u, err := f.openIndex(ctx, join(a.URL, "APKINDEX.tar.gz"))
	if err != nil {
		return nil, fmt.Errorf("reading the APKINDEX: %w", err)
	}
	defer r.Close()
	// The index is a signature's tarball, then the index's, run together.
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s: no APKINDEX in it", u)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", u, err)
		}
		if h.Name == "APKINDEX" {
			break
		}
	}
	idx := &Index{Distro: "alpine", Compare: version.RPM}
	if m := alpineRelease.FindStringSubmatch(a.URL + "/"); m != nil {
		idx.Release = strings.TrimPrefix(m[1], "v")
	}
	add := func(e map[string]string) {
		if e["P"] == "" {
			return
		}
		idx.Total++
		if !apkHasMan(e["P"]) {
			return
		}
		size, _ := strconv.ParseInt(e["S"], 10, 64)
		idx.Packages = append(idx.Packages, Package{Name: e["P"], Version: e["V"], Arch: e["A"],
			URL: join(a.URL, e["P"]+"-"+e["V"]+".apk"), Size: size})
	}
	e := map[string]string{}
	sc := bufio.NewScanner(tr)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		l := sc.Text()
		if l == "" {
			add(e)
			e = map[string]string{}
			continue
		}
		if k, v, ok := strings.Cut(l, ":"); ok && len(k) == 1 {
			e[k] = v
		}
	}
	add(e)
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return idx, nil
}
