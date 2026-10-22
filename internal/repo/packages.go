// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
)

// The package index lists every package pages come from, A to Z (by
// manpage.ListingLess), in chunks of PackageChunk, as the website's lists
// of packages show them a page at a time: with how many pages each has
// (from each source), the first few of them, and the file listing them
// all, as a chapter's index lists pages.

// PackageChunk is how many packages each chunk of the package index has:
// as many as each page of the website's lists.
const PackageChunk = 200

// packageSample is how many of a package's pages its entry names.
const packageSample = 6

// PackageIndex is index/packages/manifest-<sha>.json.
type PackageIndex struct {
	Format int `json:"format"`
	Count  int `json:"count"`
	// Sources counts the packages from each source.
	Sources map[string]int `json:"sources"`
	// Chunks are the packages, PackageChunk to a chunk, in order (each
	// index/packages/shard-ab/<sha>.json, a PackageShard), with their first
	// and last names, and the letter of the package before each.
	Chunks ChapterShards `json:"chunks"`
	// Letters are where each first letter starts, by chunk.
	Letters IndexLetters `json:"letters"`
}

// PackageShard is a chunk of the package index.
type PackageShard struct {
	Format   int         `json:"format"`
	Packages PackageRows `json:"packages"`
}

// PackageRow is a package in the package index.
type PackageRow struct {
	// Name is the package's name, in lower case: its key (see PackageKeys).
	Name string
	// Pages is how many pages come from it, and Sources how many from each
	// source ("debian", "debian/trixie", "upstream").
	Pages   int
	Sources map[string]int
	// Sample names its first few pages ("ls(1), dir(1), …").
	Sample string
	// List is the hash of its pages' list (a PackagePages, at
	// index/package/shard-ab/<sha>.json).
	List string
}

// PackagePages lists a package's pages, sorted as a listing shows them:
// each page's Sources (and Girl) are those it has from this package.
type PackagePages struct {
	Format  int        `json:"format"`
	Package string     `json:"package"`
	Pages   IndexPages `json:"pages"`
}

// PackageRows are a table: name, pages, sources ("debian=3 debian/trixie=3"),
// sample, list (in basE91).
type PackageRows []PackageRow

func (rs PackageRows) MarshalJSON() ([]byte, error) {
	t := newTable("name", "pages", "sources", "sample", "list")
	for _, r := range rs {
		t.add(r.Name, r.Pages, encodeCounts(r.Sources), r.Sample, hash91(r.List))
	}
	return t.MarshalJSON()
}

func (rs *PackageRows) UnmarshalJSON(b []byte) error {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	*rs = nil
	return t.rows(func(get func(string) json.RawMessage) error {
		var r PackageRow
		var srcs, list string
		if err := fields(get, map[string]any{"name": &r.Name, "pages": &r.Pages, "sources": &srcs,
			"sample": &r.Sample, "list": &list}); err != nil {
			return fmt.Errorf("packages: %w", err)
		}
		var err error
		if r.List, err = unhash91(list); err != nil {
			return err
		}
		if r.Sources, err = decodeCounts(srcs); err != nil {
			return err
		}
		*rs = append(*rs, r)
		return nil
	})
}

func encodeCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k + "=" + strconv.Itoa(m[k]))
	}
	return b.String()
}

func decodeCounts(s string) (map[string]int, error) {
	out := map[string]int{}
	for _, f := range strings.Fields(s) {
		k, v, ok := strings.Cut(f, "=")
		n, err := strconv.Atoi(v)
		if !ok || err != nil {
			return nil, fmt.Errorf("bad source count %q", f)
		}
		out[k] = n
	}
	return out, nil
}

// packageSourceKeys are the keys of the sources a package came from:
// its distribution, and that and its release.
func packageSourceKeys(p Package) []string {
	d := p.Source()
	if d == "" {
		return nil
	}
	if p.Release == "" || d == Upstream {
		return []string{d}
	}
	return []string{d, d + "/" + p.Release}
}

// packagePages gathers each package's pages from pages (with their index
// entries, ips), by key: each page's entry with just the sources it has
// from that package.
func packagePages(pages []PageRef, ips []IndexPage) map[string][]IndexPage {
	out := map[string][]IndexPage{}
	for i, p := range pages {
		srcs := map[string]map[string]bool{} // key → source keys
		var order []string
		for _, v := range p.Listing.Versions {
			for _, pk := range v.Packages {
				k := strings.ToLower(pk.Name)
				if k == "" || strings.ContainsAny(k, " \t\n/") {
					continue
				}
				if srcs[k] == nil {
					srcs[k] = map[string]bool{}
					order = append(order, k)
				}
				for _, s := range packageSourceKeys(pk) {
					srcs[k][s] = true
				}
			}
		}
		for _, k := range order {
			ip := ips[i]
			ip.Sources, ip.Girl = keep(ip.Sources, srcs[k]), keep(ip.Girl, srcs[k])
			out[k] = append(out[k], ip)
		}
	}
	return out
}

func keep(ss []string, in map[string]bool) []string {
	var out []string
	for _, s := range ss {
		if in[s] {
			out = append(out, s)
		}
	}
	return out
}

// writePackageIndex writes the package index for pages (with their index
// entries, ips), returning its hash, or "" if no page has a package.
func (s *Store) writePackageIndex(pages []PageRef, ips []IndexPage) (string, error) {
	byKey := packagePages(pages, ips)
	if len(byKey) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return manpage.ListingLess(keys[i], "", keys[j], "") })
	rows := make(PackageRows, len(keys))
	errs := make([]error, len(keys))
	parallel(len(keys), func(i int) {
		ps := byKey[keys[i]]
		sort.SliceStable(ps, func(a, b int) bool {
			return manpage.ListingLess(ps[a].Name, ps[a].Section, ps[b].Name, ps[b].Section)
		})
		r := PackageRow{Name: keys[i], Pages: len(ps), Sources: map[string]int{}}
		var some []string
		for j, p := range ps {
			for _, src := range p.Sources {
				r.Sources[src]++
			}
			if j < packageSample {
				some = append(some, p.Name+"("+p.Section+")")
			}
		}
		if len(ps) > packageSample {
			some = append(some, "…")
		}
		r.Sample = strings.Join(some, ", ")
		r.List, _, errs[i] = s.writeSized(func(sha string) string { return ShardPath("package", sha) },
			&PackagePages{Format: Format, Package: keys[i], Pages: ps})
		rows[i] = r
	})
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	pi := PackageIndex{Format: Format, Count: len(rows), Sources: map[string]int{}, Chunks: ChapterShards{}, Letters: IndexLetters{}}
	for _, r := range rows {
		for src := range r.Sources {
			pi.Sources[src]++
		}
	}
	prev := ""
	for i, r := range rows {
		if l := manpage.Letter(r.Name); i == 0 || l != manpage.Letter(rows[i-1].Name) {
			pi.Letters = append(pi.Letters, IndexLetter{Letter: l, Chunk: i/PackageChunk + 1})
		}
		if i%PackageChunk != 0 {
			continue
		}
		part := rows[i:min(i+PackageChunk, len(rows))]
		sha, n, err := s.writeSized(func(sha string) string { return ShardPath("packages", sha) },
			&PackageShard{Format: Format, Packages: part})
		if err != nil {
			return "", err
		}
		pi.Chunks = append(pi.Chunks, ChapterShardRef{SHA256: sha, Bytes: n, Count: len(part),
			First: part[0].Name, Last: part[len(part)-1].Name, Prev: prev})
		prev = manpage.Letter(part[len(part)-1].Name)
	}
	return s.WriteAddressed(func(sha string) string { return IndexManifestPath("packages", sha) }, &pi)
}

// PackageIndexOf reads a published repository's package index (nil if it
// hasn't one), its chunks and each package's pages, as read(path, v)
// reads its files.
func PackageIndexOf(m *Manifest, read func(rel string, v any) error) (*PackageIndex, []PackageRow, error) {
	if m.Index.Packages == "" {
		return nil, nil, nil
	}
	var pi PackageIndex
	if err := read(IndexManifestPath("packages", m.Index.Packages), &pi); err != nil {
		return nil, nil, err
	}
	var rows []PackageRow
	for _, c := range pi.Chunks {
		var sh PackageShard
		if err := read(ShardPath("packages", c.SHA256), &sh); err != nil {
			return nil, nil, err
		}
		rows = append(rows, sh.Packages...)
	}
	return &pi, rows, nil
}
