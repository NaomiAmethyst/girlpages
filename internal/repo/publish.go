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
	"sync"
	"sync/atomic"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/precompress"
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
	var mu sync.Mutex
	c := s.counter("reading listings", 0)
	err := s.eachPage(func(p Pointer) error {
		defer c.add()
		l, err := s.ListingByHash(p.SHA256)
		if err != nil {
			return err
		}
		if l.CurrentVersion() == nil {
			return nil
		}
		mu.Lock()
		out = append(out, PageRef{Pointer: p, Listing: l})
		mu.Unlock()
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
	ids, count := s.assignIDs(pages)

	// The search index wants all of every page's text: every version's,
	// as people may be looking for an option only an old one has.
	docs := make([]IndexDoc, len(pages))
	errs := make([]error, len(pages))
	c := s.counter("reading pages for the search index", len(pages))
	parallel(len(pages), func(i int) {
		defer c.add()
		p := pages[i]
		keys, _ := p.Listing.SourceKeys()
		d := IndexDoc{ID: ids[i], Name: p.Pointer.Name, Section: p.Pointer.Section, Description: p.Current().Description,
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
	sc := s.counter("writing section indexes", len(order))
	for _, sec := range order {
		sc.add()
		si := bySection[sec]
		sha, err := s.writeSectionIndex(si)
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

	s.counter("building the search index", 0)
	bi := BuildIndex(docs)
	s.counter("writing the search index", 0)
	var err error
	if m.Index.Words, err = s.writeShards("words", bi.WordRanges, func(i int) any { return &bi.Words[i] }); err != nil {
		return nil, err
	}
	if m.Index.Grams, err = s.writeShards("grams", bi.GramRanges, func(i int) any { return &bi.Grams[i] }); err != nil {
		return nil, err
	}
	if m.Index.Names, m.Index.Docs, err = s.writePageTable(docs, count); err != nil {
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

// The page table's shards hold fixed runs of ids, about a megabyte each,
// so a publish that adds pages (at the end) leaves the others as they
// were, and clients' copies of them good.
const (
	namesPerShard = 50000
	docsPerShard  = 10000
)

// assignIDs gives each page its doc id: the one it had in the last
// published index, if it was in it, or a new one after the rest. Ids of
// pages that have gone stay unused, until there are so many that it's
// worth numbering everything afresh (in name order, so ties in scores come
// out alphabetical).
func (s *Store) assignIDs(pages []PageRef) ([]int, int) {
	key := func(name, sec string) string { return sec + "\x00" + name }
	old := map[string]int{}
	count := 0
	if m, err := s.Manifest(); err == nil && m.Index.Names != "" {
		var nm TableManifest
		if s.Read(IndexManifestPath("names", m.Index.Names), &nm) == nil {
			for _, r := range nm.Shards {
				var sh NameShard
				if s.Read(ShardPath("names", r.SHA256), &sh) != nil {
					old, count = map[string]int{}, 0
					break
				}
				for i, n := range sh.Names {
					if n.Name != "" {
						old[key(n.Name, n.Section)] = sh.First + i
					}
				}
			}
			count = nm.Count
		}
	}
	kept := 0
	for _, p := range pages {
		if _, ok := old[key(p.Pointer.Name, p.Pointer.Section)]; ok {
			kept++
		}
	}
	if count > 0 && kept*10 < count*7 {
		old, count = map[string]int{}, 0 // over 30% unused: start again
	}
	order := make([]int, len(pages))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		pa, pb := pages[order[a]].Pointer, pages[order[b]].Pointer
		if pa.Name != pb.Name {
			return pa.Name < pb.Name
		}
		return pa.Section < pb.Section
	})
	ids := make([]int, len(pages))
	for _, i := range order {
		p := pages[i].Pointer
		if id, ok := old[key(p.Name, p.Section)]; ok {
			ids[i] = id
			continue
		}
		ids[i] = count
		count++
	}
	return ids, count
}

// writePageTable writes the page table: names and docs, in fixed runs of
// ids, returning their manifests' hashes.
func (s *Store) writePageTable(docs []IndexDoc, count int) (string, string, error) {
	names := make([]PageName, count)
	table := make([]PageDoc, count)
	for _, d := range docs {
		names[d.ID] = PageName{Name: d.Name, Section: d.Section}
		table[d.ID] = PageDoc{Description: d.Description, Sources: strings.Join(d.Sources, " ")}
	}
	nm := TableManifest{Format: Format, Count: count}
	for i := 0; i < count; i += namesPerShard {
		j := min(i+namesPerShard, count)
		sha, n, err := s.writeSized(func(sha string) string { return ShardPath("names", sha) },
			&NameShard{Format: Format, First: i, Names: names[i:j]})
		if err != nil {
			return "", "", err
		}
		nm.Shards = append(nm.Shards, IDRange{First: i, Last: j - 1, SHA256: sha, Bytes: n})
	}
	dm := TableManifest{Format: Format, Count: count}
	for i := 0; i < count; i += docsPerShard {
		j := min(i+docsPerShard, count)
		sha, n, err := s.writeSized(func(sha string) string { return ShardPath("docs", sha) },
			&DocShard{Format: Format, First: i, Docs: table[i:j]})
		if err != nil {
			return "", "", err
		}
		dm.Shards = append(dm.Shards, IDRange{First: i, Last: j - 1, SHA256: sha, Bytes: n})
	}
	nsha, err := s.WriteAddressed(func(sha string) string { return IndexManifestPath("names", sha) }, &nm)
	if err != nil {
		return "", "", err
	}
	dsha, err := s.WriteAddressed(func(sha string) string { return IndexManifestPath("docs", sha) }, &dm)
	return nsha, dsha, err
}

// sectionShardBytes is about how big a section index (or a shard of one)
// grows before it's split.
var sectionShardBytes = 1 << 20

// writeSectionIndex writes a section's index, split into shards if it's
// long, returning its hash.
func (s *Store) writeSectionIndex(si *SectionIndex) (string, error) {
	// By name, so each shard is a range of them.
	sort.SliceStable(si.Pages, func(i, j int) bool { return manpage.ListLess(si.Pages[i].Name, si.Pages[j].Name) })
	write := func(v *SectionIndex) (string, error) {
		return s.WriteAddressed(func(sha string) string { return SectionIndexPath(si.Section, sha) }, v)
	}
	var sizes []int
	total := 0
	for _, p := range si.Pages {
		b, err := marshal(p.values())
		if err != nil {
			return "", err
		}
		sizes = append(sizes, len(b))
		total += len(b)
	}
	if total <= sectionShardBytes {
		return write(si)
	}
	idx := &SectionIndex{Format: Format, Section: si.Section, Count: len(si.Pages)}
	for i := 0; i < len(si.Pages); {
		j, n := i, 0
		for j < len(si.Pages) && (j == i || n+sizes[j] <= sectionShardBytes) {
			n += sizes[j]
			j++
		}
		part := si.Pages[i:j]
		sha, size, err := s.writeSized(SectionShardPath, &SectionIndex{Format: Format, Section: si.Section, Pages: part})
		if err != nil {
			return "", err
		}
		idx.Shards = append(idx.Shards, ShardRange{First: part[0].Name, Last: part[len(part)-1].Name, SHA256: sha, Bytes: size, Count: len(part)})
		i = j
	}
	return write(idx)
}

// SectionPages reads every page in a section's index, shards and all.
func (s *Store) SectionPages(e SectionEntry) ([]IndexPage, error) {
	var si SectionIndex
	if err := s.Read(SectionIndexPath(e.Section, e.SHA256), &si); err != nil {
		return nil, err
	}
	pages := si.Pages
	for _, r := range si.Shards {
		var sh SectionIndex
		if err := s.Read(SectionShardPath(r.SHA256), &sh); err != nil {
			return nil, err
		}
		pages = append(pages, sh.Pages...)
	}
	return pages, nil
}

// sourceShardSize is how many keys go in each variant index shard: about
// a megabyte (fetched once, for a bulk run).
var sourceShardSize = 55000

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
			sha, n, err := s.writeSized(func(sha string) string { return ShardPath(kind, sha) },
				&SourceShard{Format: Format, Length: SourceKeyLength, Sources: part})
			if err != nil {
				return nil, err
			}
			sm.Shards = append(sm.Shards, ShardRange{First: part[0], Last: part[len(part)-1], SHA256: sha, Bytes: n})
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
	errs := make([]error, len(ranges))
	parallel(len(ranges), func(i int) {
		r := ranges[i]
		r.SHA256, r.Bytes, errs[i] = s.writeSized(func(sha string) string { return ShardPath(kind, sha) }, shard(i))
		sm.Shards[i] = r
	})
	if err := errors.Join(errs...); err != nil {
		return "", err
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
		var si SectionIndex
		if err := s.Read(SectionIndexPath(e.Section, e.SHA256), &si); err != nil {
			return st, err
		}
		for _, r := range si.Shards {
			live[SectionShardPath(r.SHA256)] = true
		}
	}
	for kind, sha := range map[string]string{"names": m.Index.Names, "docs": m.Index.Docs} {
		live[IndexManifestPath(kind, sha)] = true
		var tm TableManifest
		if err := s.Read(IndexManifestPath(kind, sha), &tm); err != nil {
			return st, err
		}
		for _, r := range tm.Shards {
			live[ShardPath(kind, r.SHA256)] = true
		}
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
	seenSource := map[string]bool{}
	gc := s.counter("finding what's in use", 0)
	var mu sync.Mutex
	err = s.eachPage(func(p Pointer) error {
		defer gc.add()
		l, err := s.ListingByHash(p.SHA256)
		if err != nil {
			return err
		}
		mu.Lock()
		live[NamePath(p.Name, p.Section)] = true
		live[ListingPath(p.SHA256)] = true
		var sources []string
		for _, v := range l.Versions {
			if !seenSource[v.Source] {
				seenSource[v.Source] = true
				sources = append(sources, v.Source)
			}
		}
		mu.Unlock()
		for _, src := range sources {
			cs, err := s.Contents(src)
			if err != nil {
				return err
			}
			mu.Lock()
			// Every generation stays, as clients may ask for any.
			live[ContentsPath(src)] = true
			for variant, gens := range cs.Variants {
				for _, g := range gens {
					live[VariantPath(src, variant, g.SHA256)] = true
				}
			}
			mu.Unlock()
		}
		return nil
	})
	if err != nil {
		return st, err
	}

	// Then everything else goes, each part of the tree swept at once.
	var roots []string
	for _, top := range []string{"contents", "page", "section", "index"} {
		root := filepath.Join(s.Dir, top)
		ents, err := os.ReadDir(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return st, err
		}
		for _, e := range ents {
			roots = append(roots, filepath.Join(root, e.Name()))
		}
	}
	var files, bytes atomic.Int64
	errs := make([]error, len(roots))
	parallel(len(roots), func(i int) {
		errs[i] = filepath.WalkDir(roots[i], func(path string, d fs.DirEntry, err error) error {
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
			if base, ok := precompress.IsCopy(rel); ok {
				rel = base // a compressed copy lives and dies with its original
			}
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
				bytes.Add(fi.Size())
			}
			files.Add(1)
			if dryRun {
				return nil
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil
		})
		if fi, err := os.Stat(roots[i]); errs[i] == nil && !dryRun && err == nil && fi.IsDir() {
			removeEmptyDirs(roots[i])
			_ = os.Remove(roots[i]) // if it's empty now
		}
	})
	st.Files, st.Bytes = int(files.Load()), bytes.Load()
	return st, errors.Join(errs...)
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
