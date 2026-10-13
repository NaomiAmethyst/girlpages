// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
)

// A chapter's index lists every page in it (all its sections: 3, 3ssl,
// 3pm…), as its listing shows them: sorted by manpage.ListingLess, so a
// listing reads it in order. It's split into shards starting with letters
// where that's near enough (see letterShards), so a listing shows its
// start, or a letter's, from one small file. The first shard's pages are
// in the index itself, with the others' ranges and where each letter
// starts.

// ChapterEntry is a chapter in the manifest: its index, and how many
// pages it has.
type ChapterEntry struct {
	Chapter string `json:"chapter"`
	SHA256  string `json:"sha256"`
	Pages   int    `json:"pages"`
}

// ChapterIndex is section/<chapter>-<sha>.json.
type ChapterIndex struct {
	Format  int        `json:"format"`
	Chapter string     `json:"chapter"`
	Count   int        `json:"count"`
	Pages   IndexPages `json:"pages"`
	// Shards are the rest of the pages, in order: chunk n (from 1) of the
	// listing is Pages for 1, else Shards[n-2].
	Shards ChapterShards `json:"shards"`
	// Letters are where each first letter starts, by chunk.
	Letters IndexLetters `json:"letters"`
}

// ChapterShard is section/shard-ab/<sha>.json.
type ChapterShard struct {
	Format  int        `json:"format"`
	Chapter string     `json:"chapter"`
	Pages   IndexPages `json:"pages"`
}

// IndexPage is a page in a chapter's index.
type IndexPage struct {
	Name, Section      string
	Title, Description string
	// Page is the SHA-256 of the page's Listing.
	Page string
	// Version is the latest version's upstream version number, if known,
	// and Variants the variants it has.
	Version  string
	Variants []string
	// Versions is how many versions the page has.
	Versions int
	// Sources are the keys of the sources the page has versions from
	// ("debian", "debian/trixie", "upstream"), and Girl those whose
	// version of it has a girly variant.
	Sources, Girl []string
}

// IndexPages are pages in a chapter's index, as a Table whose records
// refer to dictionaries beside its keys, of the values that appear in it:
// name, section (an index into sections), title, description, page (a
// hash in basE91), version (an index into versions), versions, and
// sources, variants and girl (bitmasks: bit i for sources[i], variants[i],
// and sources[i] again).
type IndexPages []IndexPage

var indexPageKeys = []string{"name", "section", "title", "description", "page", "version", "versions", "sources", "variants", "girl"}

// dict numbers strings, in order, so lists decoded from bitmasks are
// sorted.
type dict struct {
	vals []string
	idx  map[string]int
}

func newDict(set map[string]bool) dict {
	d := dict{idx: map[string]int{}}
	for s := range set {
		d.vals = append(d.vals, s)
	}
	sort.Strings(d.vals)
	for i, s := range d.vals {
		d.idx[s] = i
	}
	return d
}

func (d *dict) of(s string) int { return d.idx[s] }

// maxBits is how many values a bitmask can have: JavaScript's numbers are
// exact up to 2^53.
const maxBits = 53

func (d *dict) mask(ss []string) (uint64, error) {
	var m uint64
	for _, s := range ss {
		i := d.of(s)
		if i >= maxBits {
			return 0, fmt.Errorf("more than %d distinct values for a bitmask", maxBits)
		}
		m |= 1 << i
	}
	return m, nil
}

func unmask(m uint64, vals []string) ([]string, error) {
	var out []string
	for i := 0; m != 0; i, m = i+1, m>>1 {
		if m&1 == 0 {
			continue
		}
		if i >= len(vals) {
			return nil, fmt.Errorf("bit %d set, but there are %d values", i, len(vals))
		}
		out = append(out, vals[i])
	}
	return out, nil
}

type indexPagesJSON struct {
	Keys     []string          `json:"keys"`
	Sections []string          `json:"sections"`
	Sources  []string          `json:"sources"`
	Variants []string          `json:"variants"`
	Versions []string          `json:"versions"`
	Data     []json.RawMessage `json:"data"`
}

func (ps IndexPages) MarshalJSON() ([]byte, error) {
	sets := [4]map[string]bool{{}, {}, {}, {}}
	for _, p := range ps {
		sets[0][p.Section] = true
		for _, s := range append(append([]string(nil), p.Sources...), p.Girl...) {
			sets[1][s] = true
		}
		for _, v := range p.Variants {
			sets[2][v] = true
		}
		sets[3][p.Version] = true
	}
	secs, srcs, vars, vers := newDict(sets[0]), newDict(sets[1]), newDict(sets[2]), newDict(sets[3])
	data := make([]any, 0, len(ps)*len(indexPageKeys))
	for _, p := range ps {
		sm, err := srcs.mask(p.Sources)
		if err != nil {
			return nil, err
		}
		vm, err := vars.mask(p.Variants)
		if err != nil {
			return nil, err
		}
		gm, err := srcs.mask(p.Girl)
		if err != nil {
			return nil, err
		}
		data = append(data, p.Name, secs.of(p.Section), p.Title, p.Description, hash91(p.Page), vers.of(p.Version), p.Versions, sm, vm, gm)
	}
	orEmpty := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	return marshal(struct {
		Keys     []string `json:"keys"`
		Sections []string `json:"sections"`
		Sources  []string `json:"sources"`
		Variants []string `json:"variants"`
		Versions []string `json:"versions"`
		Data     []any    `json:"data"`
	}{indexPageKeys, orEmpty(secs.vals), orEmpty(srcs.vals), orEmpty(vars.vals), orEmpty(vers.vals), data})
}

func (ps *IndexPages) UnmarshalJSON(b []byte) error {
	var j indexPagesJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*ps = nil
	pick := func(i int, vals []string, what string) (string, error) {
		if i < 0 || i >= len(vals) {
			return "", fmt.Errorf("%s %d of %d", what, i, len(vals))
		}
		return vals[i], nil
	}
	t := Table{Keys: j.Keys, Data: j.Data}
	return t.rows(func(get func(string) json.RawMessage) error {
		var p IndexPage
		var page string
		sec, ver := -1, -1
		var sm, vm, gm uint64
		if err := fields(get, map[string]any{"name": &p.Name, "section": &sec, "title": &p.Title, "description": &p.Description,
			"page": &page, "version": &ver, "versions": &p.Versions, "sources": &sm, "variants": &vm, "girl": &gm}); err != nil {
			return fmt.Errorf("chapter index: %w", err)
		}
		var err error
		if p.Page, err = unhash91(page); err != nil {
			return err
		}
		if p.Section, err = pick(sec, j.Sections, "section"); err != nil {
			return err
		}
		if ver >= 0 {
			if p.Version, err = pick(ver, j.Versions, "version"); err != nil {
				return err
			}
		}
		if p.Sources, err = unmask(sm, j.Sources); err != nil {
			return err
		}
		if p.Variants, err = unmask(vm, j.Variants); err != nil {
			return err
		}
		if p.Girl, err = unmask(gm, j.Sources); err != nil {
			return err
		}
		*ps = append(*ps, p)
		return nil
	})
}

// size guesses how many bytes a page adds to an index, for sharding.
func (p IndexPage) size() int {
	n := 0
	for _, s := range []string{p.Name, p.Title, p.Description} {
		b, _ := json.Marshal(s)
		n += len(b) + 1
	}
	return n + 42 + 25 // the page's hash, and the numbers
}

// ChapterShardRef is a shard in a chapter's index: its pages' first and
// last names, and the letter of the page before it, so a shard shown on
// its own knows whether its first letter carries on.
type ChapterShardRef struct {
	SHA256      string
	Bytes       int64
	Count       int
	First, Last string
	Prev        string
}

// ChapterShards are a table: sha256 (in basE91), bytes, count, first,
// last, and prev.
type ChapterShards []ChapterShardRef

func (s ChapterShards) MarshalJSON() ([]byte, error) {
	t := newTable("sha256", "bytes", "count", "first", "last", "prev")
	for _, r := range s {
		t.add(hash91(r.SHA256), r.Bytes, r.Count, r.First, r.Last, r.Prev)
	}
	return t.MarshalJSON()
}

func (s *ChapterShards) UnmarshalJSON(b []byte) error {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	*s = nil
	return t.rows(func(get func(string) json.RawMessage) error {
		var r ChapterShardRef
		var sha string
		if err := fields(get, map[string]any{"sha256": &sha, "bytes": &r.Bytes, "count": &r.Count,
			"first": &r.First, "last": &r.Last, "prev": &r.Prev}); err != nil {
			return fmt.Errorf("shards: %w", err)
		}
		var err error
		if r.SHA256, err = unhash91(sha); err != nil {
			return err
		}
		*s = append(*s, r)
		return nil
	})
}

// IndexLetter is where a letter's pages start: in chunk Chunk (from 1).
type IndexLetter struct {
	Letter string
	Chunk  int
}

// IndexLetters are a chapter's letters, in order, as a table: letter,
// chunk.
type IndexLetters []IndexLetter

func (ls IndexLetters) MarshalJSON() ([]byte, error) {
	t := newTable("letter", "chunk")
	for _, l := range ls {
		t.add(l.Letter, l.Chunk)
	}
	return t.MarshalJSON()
}

func (ls *IndexLetters) UnmarshalJSON(b []byte) error {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	*ls = nil
	return t.rows(func(get func(string) json.RawMessage) error {
		var l IndexLetter
		if err := fields(get, map[string]any{"letter": &l.Letter, "chunk": &l.Chunk}); err != nil {
			return fmt.Errorf("letters: %w", err)
		}
		*ls = append(*ls, l)
		return nil
	})
}

// fields decodes a table row's fields into the values given for them.
func fields(get func(string) json.RawMessage, vs map[string]any) error {
	for k, v := range vs {
		if err := field(get(k), v); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	return nil
}

// Shard sizes for chapter indexes: the first shard's, and the others'
// (variables, so tests can make them small).
var shardFirst, shardMore = 64 << 10, 256 << 10

// letterShards splits records, given their sizes and letters in order,
// into shards, returning where each starts. A shard is cut once it's its
// target size (shardFirst for the first, shardMore for the rest), unless
// it can end with a letter instead: by going on to the letter's end, if
// that's no more than 20% over, or else by stopping at its start, if
// that's no less than half. A shard ending with a letter is followed by a
// shardFirst one, so a letter's first shard (which jumping to it fetches)
// is small.
func letterShards(sizes []int, letters []string) []int {
	var starts []int
	target := shardFirst
	for i := 0; i < len(sizes); {
		starts = append(starts, i)
		sum, e := 0, i
		for e < len(sizes) && sum < target {
			sum += sizes[e]
			e++
		}
		if e == len(sizes) {
			break
		}
		// On to the end of the letter.
		b, over := e, sum
		for b < len(sizes) && letters[b] == letters[b-1] {
			over += sizes[b]
			b++
		}
		if over*10 <= target*12 {
			i, target = b, shardFirst
			continue
		}
		// Back to its start.
		b, under := e, sum
		for b > i && letters[b] == letters[b-1] {
			b--
			under -= sizes[b]
		}
		if b > i && under*2 >= target {
			i, target = b, shardFirst
			continue
		}
		i, target = e, shardMore
	}
	return starts
}

// writeChapterIndex writes a chapter's index of ps (in any order),
// returning its hash.
func (s *Store) writeChapterIndex(chapter string, ps []IndexPage) (string, error) {
	sort.SliceStable(ps, func(i, j int) bool {
		return manpage.ListingLess(ps[i].Name, ps[i].Section, ps[j].Name, ps[j].Section)
	})
	sizes := make([]int, len(ps))
	letters := make([]string, len(ps))
	for i, p := range ps {
		sizes[i] = p.size()
		letters[i] = manpage.Letter(p.Name)
	}
	starts := letterShards(sizes, letters)
	part := func(k int) []IndexPage {
		if k >= len(starts) {
			return nil
		}
		end := len(ps)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		return ps[starts[k]:end]
	}
	ci := ChapterIndex{Format: Format, Chapter: chapter, Count: len(ps), Pages: part(0),
		Shards: make(ChapterShards, max(0, len(starts)-1)), Letters: IndexLetters{}}
	if ci.Pages == nil {
		ci.Pages = IndexPages{}
	}
	chunk := 0
	for i, l := range letters {
		for chunk+1 < len(starts) && starts[chunk+1] <= i {
			chunk++
		}
		if i == 0 || l != letters[i-1] {
			ci.Letters = append(ci.Letters, IndexLetter{Letter: l, Chunk: chunk + 1})
		}
	}
	errs := make([]error, len(ci.Shards))
	parallel(len(ci.Shards), func(k int) {
		pp := part(k + 1)
		sha, n, err := s.writeSized(SectionShardPath, &ChapterShard{Format: Format, Chapter: chapter, Pages: pp})
		ci.Shards[k] = ChapterShardRef{SHA256: sha, Bytes: n, Count: len(pp), First: pp[0].Name, Last: pp[len(pp)-1].Name,
			Prev: letters[starts[k+1]-1]}
		errs[k] = err
	})
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	return s.WriteAddressed(func(sha string) string { return SectionIndexPath(chapter, sha) }, &ci)
}

// ChapterPages reads every page in a chapter's index, shards and all.
func (s *Store) ChapterPages(e ChapterEntry) ([]IndexPage, error) {
	return chapterPages(func(path string, v any) error { return s.Read(path, v) }, e)
}

func chapterPages(read func(path string, v any) error, e ChapterEntry) ([]IndexPage, error) {
	var ci ChapterIndex
	if err := read(SectionIndexPath(e.Chapter, e.SHA256), &ci); err != nil {
		return nil, err
	}
	pages := append([]IndexPage(nil), ci.Pages...)
	for _, r := range ci.Shards {
		var sh ChapterShard
		if err := read(SectionShardPath(r.SHA256), &sh); err != nil {
			return nil, err
		}
		pages = append(pages, sh.Pages...)
	}
	return pages, nil
}
