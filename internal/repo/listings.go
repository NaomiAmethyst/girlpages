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

// Section listings are what the website's listing pages show, for an app
// to show without reading whole section indexes (which say much more, and
// for a big chapter run to tens of megabytes): each listing (a chapter,
// all of it or one of its sections) is chunks of ListingChunk pages, as the
// website's listing pages are, so a listing shows its first chunk as soon
// as that's fetched. The listings manifest (index/listings/manifest-<sha>)
// names each listing's file (a SectionListing), which names its chunks
// (ListingChunks); all of them are index/listings/shard-ab/<sha>.json.

// ListingChunk is how many pages are in a listing's chunk, as on a page of
// the website's listings.
const ListingChunk = 200

// listingChunk is ListingChunk, but tests make it smaller.
var listingChunk = ListingChunk

// ListingsManifest lists the section listings.
type ListingsManifest struct {
	Format   int         `json:"format"`
	Listings ListingRefs `json:"listings"`
}

// ListingRef names a listing: Key is its chapter, Sub the section if it's
// just one ("" for the whole chapter).
type ListingRef struct {
	Key, Sub string
	SHA256   string
	Count    int
}

type ListingRefs []ListingRef

func (rs ListingRefs) MarshalJSON() ([]byte, error) {
	t := newTable("key", "sub", "sha256", "count")
	for _, r := range rs {
		t.add(r.Key, r.Sub, hash91(r.SHA256), r.Count)
	}
	return t.MarshalJSON()
}

func (rs *ListingRefs) UnmarshalJSON(b []byte) error {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	*rs = nil
	return t.rows(func(get func(string) json.RawMessage) error {
		var r ListingRef
		var sha string
		if err := fields(get, map[string]any{"key": &r.Key, "sub": &r.Sub, "sha256": &sha, "count": &r.Count}); err != nil {
			return fmt.Errorf("listings: %w", err)
		}
		var err error
		if r.SHA256, err = unhash91(sha); err != nil {
			return err
		}
		*rs = append(*rs, r)
		return nil
	})
}

// Find is the listing for key and sub, if there is one.
func (rs ListingRefs) Find(key, sub string) (ListingRef, bool) {
	for _, r := range rs {
		if r.Key == key && r.Sub == sub {
			return r, true
		}
	}
	return ListingRef{}, false
}

// SectionListing is a listing: how many pages it has, the chunk each
// letter starts in, and its chunks. (How many each of a chapter's
// sections has is in the manifest, as its listings' counts.)
type SectionListing struct {
	Format  int            `json:"format"`
	Key     string         `json:"key"`
	Sub     string         `json:"sub,omitempty"`
	Count   int            `json:"count"`
	Letters ListingLetters `json:"letters"`
	Chunks  ListingChunks  `json:"chunks"`
}

// ListingLetter is where a letter's pages start: in chunk Chunk (from 1).
type ListingLetter struct {
	Letter string
	Chunk  int
}

// ListingLetters are a listing's letters, in order.
type ListingLetters []ListingLetter

func (ls ListingLetters) MarshalJSON() ([]byte, error) {
	t := newTable("letter", "chunk")
	for _, l := range ls {
		t.add(l.Letter, l.Chunk)
	}
	return t.MarshalJSON()
}

func (ls *ListingLetters) UnmarshalJSON(b []byte) error {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	*ls = nil
	return t.rows(func(get func(string) json.RawMessage) error {
		var l ListingLetter
		if err := fields(get, map[string]any{"letter": &l.Letter, "chunk": &l.Chunk}); err != nil {
			return fmt.Errorf("letters: %w", err)
		}
		*ls = append(*ls, l)
		return nil
	})
}

// ListingChunkRef names a chunk of a listing: its first page's name, and
// the letter of the page before it ("" for the first), so a chunk shown
// on its own knows whether its first letter carries on.
type ListingChunkRef struct {
	SHA256 string
	First  string
	Prev   string
}

type ListingChunks []ListingChunkRef

func (cs ListingChunks) MarshalJSON() ([]byte, error) {
	t := newTable("sha256", "first", "prev")
	for _, c := range cs {
		t.add(hash91(c.SHA256), c.First, c.Prev)
	}
	return t.MarshalJSON()
}

func (cs *ListingChunks) UnmarshalJSON(b []byte) error {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	*cs = nil
	return t.rows(func(get func(string) json.RawMessage) error {
		var c ListingChunkRef
		var sha string
		if err := fields(get, map[string]any{"sha256": &sha, "first": &c.First, "prev": &c.Prev}); err != nil {
			return fmt.Errorf("chunks: %w", err)
		}
		var err error
		if c.SHA256, err = unhash91(sha); err != nil {
			return err
		}
		*cs = append(*cs, c)
		return nil
	})
}

// ListingPagesChunk is a chunk of a listing.
type ListingPagesChunk struct {
	Format int          `json:"format"`
	Pages  ListingPages `json:"pages"`
}

// ListingPage is a page as a listing shows it.
type ListingPage struct {
	Name, Section, Description string
	Girl                       bool
}

type ListingPages []ListingPage

func (ps ListingPages) MarshalJSON() ([]byte, error) {
	t := newTable("name", "section", "description", "girl")
	for _, p := range ps {
		girl := 0
		if p.Girl {
			girl = 1
		}
		t.add(p.Name, p.Section, p.Description, girl)
	}
	return t.MarshalJSON()
}

func (ps *ListingPages) UnmarshalJSON(b []byte) error {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	*ps = nil
	return t.rows(func(get func(string) json.RawMessage) error {
		var p ListingPage
		var girl int
		if err := fields(get, map[string]any{"name": &p.Name, "section": &p.Section, "description": &p.Description, "girl": &girl}); err != nil {
			return fmt.Errorf("pages: %w", err)
		}
		p.Girl = girl != 0
		*ps = append(*ps, p)
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

// writeListings writes the section listings: each chapter's, and each of
// its sections' if it has more than one, as the website lists them.
func (s *Store) writeListings(pages []PageRef) (string, error) {
	byKey := map[string][]ListingPage{}
	for _, p := range pages {
		v := p.Current()
		if v == nil {
			continue
		}
		key := manpage.Chapter(p.Pointer.Section)
		_, girl := v.Variants[VariantGirl]
		byKey[key] = append(byKey[key], ListingPage{Name: p.Pointer.Name, Section: p.Pointer.Section,
			Description: v.Description, Girl: girl})
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var jobs []func() (ListingRef, error)
	for _, key := range keys {
		ps := byKey[key]
		sort.SliceStable(ps, func(i, j int) bool {
			return manpage.ListingLess(ps[i].Name, ps[i].Section, ps[j].Name, ps[j].Section)
		})
		subs := map[string]bool{}
		for _, p := range ps {
			subs[p.Section] = true
		}
		if len(subs) < 2 {
			subs = nil
		}
		jobs = append(jobs, func() (ListingRef, error) { return s.writeListing(key, "", ps) })
		for sub := range subs {
			var sp []ListingPage
			for _, p := range ps {
				if p.Section == sub {
					sp = append(sp, p)
				}
			}
			jobs = append(jobs, func() (ListingRef, error) { return s.writeListing(key, sub, sp) })
		}
	}
	refs := make(ListingRefs, len(jobs))
	errs := make([]error, len(jobs))
	parallel(len(jobs), func(i int) { refs[i], errs[i] = jobs[i]() })
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Key != refs[j].Key {
			return refs[i].Key < refs[j].Key
		}
		return refs[i].Sub < refs[j].Sub
	})
	return s.WriteAddressed(func(sha string) string { return IndexManifestPath("listings", sha) },
		&ListingsManifest{Format: Format, Listings: refs})
}

// writeListing writes a listing of ps (in order) and its chunks.
func (s *Store) writeListing(key, sub string, ps []ListingPage) (ListingRef, error) {
	sl := SectionListing{Format: Format, Key: key, Sub: sub, Count: len(ps), Letters: ListingLetters{}, Chunks: ListingChunks{}}
	seen := map[string]bool{}
	for i, p := range ps {
		if l := manpage.Letter(p.Name); !seen[l] {
			seen[l] = true
			sl.Letters = append(sl.Letters, ListingLetter{Letter: l, Chunk: i/listingChunk + 1})
		}
	}
	for i := 0; i < len(ps); i += listingChunk {
		part := ps[i:min(i+listingChunk, len(ps))]
		sha, err := s.WriteAddressed(func(sha string) string { return ShardPath("listings", sha) },
			&ListingPagesChunk{Format: Format, Pages: part})
		if err != nil {
			return ListingRef{}, err
		}
		c := ListingChunkRef{SHA256: sha, First: part[0].Name}
		if i > 0 {
			c.Prev = manpage.Letter(ps[i-1].Name)
		}
		sl.Chunks = append(sl.Chunks, c)
	}
	sha, err := s.WriteAddressed(func(sha string) string { return ShardPath("listings", sha) }, &sl)
	if err != nil {
		return ListingRef{}, err
	}
	return ListingRef{Key: key, Sub: sub, SHA256: sha, Count: len(ps)}, nil
}
