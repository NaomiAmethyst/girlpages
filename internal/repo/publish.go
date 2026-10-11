// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PageRef is a page with its current listing, as gathered for publishing.
type PageRef struct {
	Pointer Pointer
	Listing *Listing
}

// Current is the page's current version.
func (p *PageRef) Current() *Version { return p.Listing.CurrentVersion() }

// variantNames lists a version's variants, sorted.
func variantNames(v *Version) []string {
	var out []string
	for name := range v.Variants {
		if ValidVariant(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Girlified reports whether the current version has a girl variant.
func (p *PageRef) Girlified() bool {
	v := p.Current()
	if v == nil {
		return false
	}
	_, ok := v.Variants[VariantGirl]
	return ok
}

// AllPages reads every page's listing, sorted by section then name.
func (s *Store) AllPages() ([]PageRef, error) {
	var out []PageRef
	err := s.Pages(func(p Pointer) error {
		l, err := s.ListingByHash(p.SHA256)
		if err != nil {
			return err
		}
		if l.CurrentVersion() == nil {
			return nil
		}
		out = append(out, PageRef{Pointer: p, Listing: l})
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Pointer, out[j].Pointer
		if a.Section != b.Section {
			return a.Section < b.Section
		}
		return a.Name < b.Name
	})
	return out, err
}

// Publish writes the section indexes, the search index and finally
// manifest.json from the pages in the store. The caller must hold Lock.
func (s *Store) Publish(pages []PageRef) (*Manifest, error) {
	m := &Manifest{Format: Format, Generated: now(), Sources: Sources(pages), Variants: map[string]int{}}

	// The search index wants all of every page's text: every version's,
	// as people may be looking for an option only an old one has.
	docs := make([]IndexDoc, len(pages))
	errs := make([]error, len(pages))
	parallel(len(pages), func(i int) {
		p := pages[i]
		keys, _ := p.Listing.SourceKeys()
		d := IndexDoc{Name: p.Pointer.Name, Section: p.Pointer.Section, Description: p.Current().Description,
			Sources: keys}
		var body strings.Builder
		descs := map[string]bool{d.Description: true}
		for _, v := range p.Listing.Versions {
			if v.Description != "" && !descs[v.Description] {
				descs[v.Description] = true
				body.WriteString(v.Description)
				body.WriteByte('\n')
			}
			variant := VariantMan
			if _, ok := v.Variants[variant]; !ok {
				variant = VariantGirl
				if _, ok := v.Variants[variant]; !ok {
					continue
				}
			}
			doc, err := s.Variant(v.Source, variant)
			if err != nil {
				errs[i] = fmt.Errorf("%s(%s): %w", p.Pointer.Name, p.Pointer.Section, err)
				return
			}
			for _, sct := range doc.Sections {
				body.WriteString(sct.Heading)
				body.WriteByte('\n')
				body.WriteString(sct.Body)
			}
		}
		d.Body = body.String()
		docs[i] = d
	})
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	bySection := map[string]*SectionIndex{}
	var order []string
	for _, p := range pages {
		v := p.Current()
		sec := p.Pointer.Section
		si := bySection[sec]
		if si == nil {
			si = &SectionIndex{Format: Format, Section: sec}
			bySection[sec] = si
			order = append(order, sec)
		}
		keys, other := p.Listing.SourceKeys()
		ip := IndexPage{
			Name: p.Pointer.Name, Title: v.Title, Description: v.Description,
			Page: p.Pointer.SHA256, Source: v.Source, Variants: variantNames(v),
			Version: v.Upstream(), Sources: keys, Other: other,
		}
		if n := len(p.Listing.Versions); n > 1 {
			ip.Versions = n
		}
		si.Pages = append(si.Pages, ip)
		m.Pages++
		for _, variant := range variantNames(v) {
			m.Variants[variant]++
		}
	}

	sort.Strings(order)
	for _, sec := range order {
		si := bySection[sec]
		sha, err := s.WriteAddressed(func(sha string) string { return SectionIndexPath(sec, sha) }, si)
		if err != nil {
			return nil, err
		}
		if _, _, err := s.Write(SectionPointerPath(sec), &Pointer{Format: Format, Section: sec, SHA256: sha}); err != nil {
			return nil, err
		}
		e := SectionEntry{Section: sec, SHA256: sha, Pages: len(si.Pages)}
		for _, ip := range si.Pages {
			for _, variant := range ip.Variants {
				if e.Variants == nil {
					e.Variants = map[string]int{}
				}
				e.Variants[variant]++
			}
		}
		m.Sections = append(m.Sections, e)
	}

	bi := BuildIndex(docs)
	var err error
	if m.Index.Words, err = s.writeShards("words", bi.WordRanges, func(i int) any { return &bi.Words[i] }); err != nil {
		return nil, err
	}
	if m.Index.Grams, err = s.writeShards("grams", bi.GramRanges, func(i int) any { return &bi.Grams[i] }); err != nil {
		return nil, err
	}
	dm := DocsManifest{Format: Format, Count: bi.DocCount, Size: docShardSize}
	for i := range bi.Docs {
		sha, err := s.WriteAddressed(func(sha string) string { return ShardPath("docs", sha) }, &bi.Docs[i])
		if err != nil {
			return nil, err
		}
		dm.Shards = append(dm.Shards, sha)
	}
	if m.Index.Docs, err = s.WriteAddressed(func(sha string) string { return IndexManifestPath("docs", sha) }, &dm); err != nil {
		return nil, err
	}

	if m.Index.Variants, err = s.writeVariantIndexes(pages); err != nil {
		return nil, err
	}

	if _, _, err := s.Write(ManifestPath(), m); err != nil {
		return nil, err
	}
	return m, nil
}

// sourceShardSize is how many keys go in each variant index shard: about
// 64 KB.
const sourceShardSize = 3500

// writeVariantIndexes writes an index of the versions that have each
// variant, returning each one's manifest hash.
func (s *Store) writeVariantIndexes(pages []PageRef) (map[string]string, error) {
	keys := map[string]map[string]bool{}
	for _, p := range pages {
		for _, v := range p.Listing.Versions {
			for variant := range v.Variants {
				if !ValidVariant(variant) {
					continue
				}
				if keys[variant] == nil {
					keys[variant] = map[string]bool{}
				}
				keys[variant][v.Source[:SourceKeyLength]] = true
			}
		}
	}
	out := map[string]string{}
	for variant, set := range keys {
		var sorted []string
		for k := range set {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		kind := VariantIndexKind(variant)
		sm := ShardManifest{Format: Format, Shards: []ShardRange{}}
		for i := 0; i < len(sorted); i += sourceShardSize {
			part := sorted[i:min(i+sourceShardSize, len(sorted))]
			sha, err := s.WriteAddressed(func(sha string) string { return ShardPath(kind, sha) },
				&SourceShard{Format: Format, Length: SourceKeyLength, Sources: part})
			if err != nil {
				return nil, err
			}
			sm.Shards = append(sm.Shards, ShardRange{First: part[0], Last: part[len(part)-1], SHA256: sha})
		}
		sha, err := s.WriteAddressed(func(sha string) string { return IndexManifestPath(kind, sha) }, &sm)
		if err != nil {
			return nil, err
		}
		out[variant] = sha
	}
	return out, nil
}

func (s *Store) writeShards(kind string, ranges []ShardRange, shard func(int) any) (string, error) {
	sm := ShardManifest{Format: Format, Shards: make([]ShardRange, len(ranges))}
	for i, r := range ranges {
		sha, err := s.WriteAddressed(func(sha string) string { return ShardPath(kind, sha) }, shard(i))
		if err != nil {
			return "", err
		}
		r.SHA256 = sha
		sm.Shards[i] = r
	}
	return s.WriteAddressed(func(sha string) string { return IndexManifestPath(kind, sha) }, &sm)
}

// GCStats counts what GC removed.
type GCStats struct {
	Files int
	Bytes int64
}

// GC deletes everything nothing refers to any more: superseded listings,
// section indexes and index shards, and contents of versions no listing
// mentions. It keeps what the current manifest needs, so run it after
// Publish. The caller must hold Lock.
func (s *Store) GC(dryRun bool) (GCStats, error) {
	var st GCStats
	live := map[string]bool{ManifestPath(): true}
	m, err := s.Manifest()
	if err != nil {
		return st, fmt.Errorf("publish before collecting garbage: %w", err)
	}
	for _, e := range m.Sections {
		live[SectionPointerPath(e.Section)] = true
		live[SectionIndexPath(e.Section, e.SHA256)] = true
	}
	live[IndexManifestPath("docs", m.Index.Docs)] = true
	var dm DocsManifest
	if err := s.Read(IndexManifestPath("docs", m.Index.Docs), &dm); err != nil {
		return st, err
	}
	for _, sha := range dm.Shards {
		live[ShardPath("docs", sha)] = true
	}
	kinds := map[string]string{"words": m.Index.Words, "grams": m.Index.Grams}
	for variant, sha := range m.Index.Variants {
		if ValidVariant(variant) {
			kinds[VariantIndexKind(variant)] = sha
		}
	}
	for kind, sha := range kinds {
		if sha == "" {
			continue
		}
		live[IndexManifestPath(kind, sha)] = true
		var sm ShardManifest
		if err := s.Read(IndexManifestPath(kind, sha), &sm); err != nil {
			return st, err
		}
		for _, r := range sm.Shards {
			live[ShardPath(kind, r.SHA256)] = true
		}
	}
	err = s.Pages(func(p Pointer) error {
		live[NamePath(p.Name, p.Section)] = true
		live[ListingPath(p.SHA256)] = true
		l, err := s.ListingByHash(p.SHA256)
		if err != nil {
			return err
		}
		for _, v := range l.Versions {
			for variant := range v.Variants {
				live[ContentPath(v.Source, variant)] = true
			}
		}
		return nil
	})
	if err != nil {
		return st, err
	}
	for _, top := range []string{"contents", "page", "section", "index"} {
		root := filepath.Join(s.Dir, top)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(s.Dir, path)
			rel = filepath.ToSlash(rel)
			if live[rel] {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".tmp-") {
				// Someone else's write in progress, unless it's old.
				if fi, err := d.Info(); err == nil && now().Sub(fi.ModTime()) < 3600e9 {
					return nil
				}
			}
			if fi, err := d.Info(); err == nil {
				st.Bytes += fi.Size()
			}
			st.Files++
			if dryRun {
				return nil
			}
			return os.Remove(path)
		})
		if err != nil {
			return st, err
		}
		if !dryRun {
			removeEmptyDirs(root)
		}
	}
	return st, nil
}

func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // fails, harmlessly, if not empty
	}
}
