// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
)

// ImportOptions controls an Import.
type ImportOptions struct {
	// Sections and Match limit what's imported.
	Sections []string
	Match    *regexp.Regexp
	// PreferRemote takes the other repository's variant when both have one
	// of the same version and they differ (usually two girl conversions).
	// Otherwise the local one stays.
	PreferRemote bool
	// Jobs is how many pages are fetched at once.
	Jobs int
	// Progress, if set, is told how many pages are done, of how many.
	Progress Progress
	DryRun   bool
	Log      io.Writer
}

// ImportStats summarises an Import.
type ImportStats struct {
	// Pages in the other repository (that matched); Same ones needed
	// nothing; New ones weren't here at all.
	Pages, Same, New int
	// Versions and Variants added; Conflicts were variants both had that
	// differ (Replaced if PreferRemote took the other one).
	Versions, Variants, Conflicts, Replaced int
	// Packages is how many places-found were added to versions here.
	Packages int
	Failed   int
}

// Import merges a published repository into this one: pages and versions
// it doesn't have are added, versions both have gain each other's
// packages and missing variants, and every file fetched is checked against
// the hash its listing gives. The caller must hold Lock.
func (s *Store) Import(ctx context.Context, c *Client, o ImportOptions) (ImportStats, error) {
	m, err := c.Manifest(ctx)
	if err != nil {
		return ImportStats{}, err
	}
	var todo []IndexPage
	for _, ce := range m.Chapters {
		wanted := false
		for _, se := range m.Sections {
			wanted = wanted || (manpage.Chapter(se.Section) == ce.Chapter && manpage.SectionMatches(se.Section, o.Sections))
		}
		if !wanted {
			continue
		}
		pages, err := c.ChapterPages(ctx, ce)
		if err != nil {
			return ImportStats{}, err
		}
		for _, p := range pages {
			if !manpage.SectionMatches(p.Section, o.Sections) || (o.Match != nil && !o.Match.MatchString(p.Name)) {
				continue
			}
			todo = append(todo, p)
		}
	}
	return s.importPages(ctx, c, todo, o)
}

// ImportFrom merges the repository files in tree (page/ and contents/, as
// replaying packs gives them) into this one, as Import does: every page
// there, or if only is set, those whose pointer files (by their paths
// from tree) it names. The caller must hold Lock.
func (s *Store) ImportFrom(ctx context.Context, tree string, only map[string]bool, o ImportOptions) (ImportStats, error) {
	if only != nil {
		// A repository with nothing in it wants everything, whatever's
		// changed in the tree (it may have been emptied since the last
		// sync).
		if paths, err := s.pointerPaths(); err == nil && len(paths) == 0 {
			only = nil
		}
	}
	src := &Store{Dir: tree}
	var todo []IndexPage
	var mu sync.Mutex
	err := src.eachPage(ctx, func(p Pointer) error {
		if only != nil && !only[NamePath(p.Name, p.Section)] {
			return nil
		}
		if !manpage.SectionMatches(p.Section, o.Sections) || (o.Match != nil && !o.Match.MatchString(p.Name)) {
			return nil
		}
		mu.Lock()
		todo = append(todo, IndexPage{Name: p.Name, Section: p.Section, Page: p.SHA256})
		mu.Unlock()
		return nil
	})
	if err != nil {
		return ImportStats{}, err
	}
	sort.Slice(todo, func(i, j int) bool {
		if todo[i].Section != todo[j].Section {
			return todo[i].Section < todo[j].Section
		}
		return todo[i].Name < todo[j].Name
	})
	return s.importPages(ctx, NewClient(tree, 0), todo, o)
}

// importPages merges pages (by name, section and listing hash) from a
// repository c reads into this one.
func (s *Store) importPages(ctx context.Context, c *Client, todo []IndexPage, o ImportOptions) (ImportStats, error) {
	var st ImportStats
	logf := func(format string, a ...any) {
		if o.Log != nil {
			fmt.Fprintf(o.Log, format, a...)
		}
	}
	st.Pages = len(todo)

	// What's here: each page's listing, and which pages share each source
	// (aliases share variant files, so changing one changes them all).
	local := map[string]string{}
	bySource := map[string][]Pointer{}
	pages, err := s.AllPages()
	if err != nil {
		return st, err
	}
	for _, p := range pages {
		local[p.Pointer.Name+"\x00"+p.Pointer.Section] = p.Pointer.SHA256
		for _, v := range p.Listing.Versions {
			bySource[v.Source] = append(bySource[v.Source], p.Pointer)
		}
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		next     atomic.Int64
		fetching sync.Map // source+variant → *variantFetch
		replaced = map[string]VariantInfo{}
	)
	type result struct {
		listing *Listing
		have    map[string]map[string]VariantInfo // source → variant → what's stored here
	}
	for w := 0; w < max(o.Jobs, 1); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(todo) || ctx.Err() != nil {
					return
				}
				if o.Progress != nil {
					o.Progress("merging pages", i, len(todo))
				}
				ip := todo[i]
				sec := ip.Section
				key := ip.Name + "\x00" + sec
				title := ip.Name + "(" + sec + ")"
				if local[key] == ip.Page {
					mu.Lock()
					st.Same++
					mu.Unlock()
					continue
				}
				rl, err := s.fetchListing(ctx, c, ip.Page)
				if err != nil {
					mu.Lock()
					st.Failed++
					mu.Unlock()
					logf("💔 %s: %v\n", title, err)
					continue
				}
				have := map[string]map[string]VariantInfo{}
				var failed error
				for _, v := range rl.Versions {
					have[v.Source] = map[string]VariantInfo{}
					for variant, info := range v.Variants {
						fi, _ := fetching.LoadOrStore(v.Source+"/"+variant, &variantFetch{})
						f := fi.(*variantFetch)
						f.once.Do(func() { f.info, f.added, f.conflict, f.err = s.importVariant(ctx, c, v.Source, variant, info, o) })
						if f.err != nil {
							failed = f.err
							continue
						}
						if f.info.SHA256 != "" {
							have[v.Source][variant] = f.info
						}
						f.mu.Lock()
						if !f.counted {
							f.counted = true
							mu.Lock()
							if f.added {
								st.Variants++
							}
							if f.conflict {
								st.Conflicts++
								if o.PreferRemote {
									st.Replaced++
									replaced[v.Source+"/"+variant] = f.info
								}
							}
							mu.Unlock()
						}
						f.mu.Unlock()
					}
				}
				if failed != nil {
					logf("💔 %s: %v\n", title, failed)
					mu.Lock()
					st.Failed++
					mu.Unlock()
				}
				r := result{rl, have}
				var newPage bool
				var versions, pkgs int
				merge := func(l *Listing) error {
					newPage = len(l.Versions) == 0
					for _, rv := range r.listing.Versions {
						if len(r.have[rv.Source]) == 0 {
							continue // nothing of it could be had
						}
						lv := l.Find(rv.Source)
						if lv == nil {
							nv := rv
							nv.Variants = r.have[rv.Source]
							nv.Packages = append([]Package(nil), rv.Packages...)
							l.Versions = append(l.Versions, nv)
							versions++
							continue
						}
						for variant, info := range r.have[rv.Source] {
							lv.Variants[variant] = info
						}
						for _, p := range rv.Packages {
							if !hasPackage(lv.Packages, p) {
								lv.Packages = append(lv.Packages, p)
								pkgs++
							}
						}
						sort.Slice(lv.Packages, func(i, j int) bool { return lv.Packages[i].String() < lv.Packages[j].String() })
						if lv.Title == "" {
							lv.Title = rv.Title
						}
						if lv.Description == "" {
							lv.Description = rv.Description
						}
					}
					return nil
				}
				if o.DryRun {
					l, _, err := s.Listing(ip.Name, sec)
					if err == nil {
						if l == nil {
							l = &Listing{}
						}
						cp := *l
						cp.Versions = append([]Version(nil), l.Versions...)
						for i := range cp.Versions {
							cp.Versions[i].Variants = map[string]VariantInfo{}
						}
						err = merge(&cp)
					}
				} else {
					err = s.UpdateListing(ip.Name, sec, merge)
				}
				mu.Lock()
				if err != nil {
					st.Failed++
					logf("💔 %s: %v\n", title, err)
				} else {
					if newPage {
						st.New++
					}
					st.Versions += versions
					st.Packages += pkgs
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return st, err
	}

	// A replaced variant file is shared by every page with that version;
	// tell their listings.
	if !o.DryRun {
		for key, info := range replaced {
			src, variant := key[:64], key[65:]
			for _, p := range bySource[src] {
				err := s.UpdateListing(p.Name, p.Section, func(l *Listing) error {
					if v := l.Find(src); v != nil {
						v.Variants[variant] = info
					}
					return nil
				})
				if err != nil {
					return st, err
				}
			}
		}
	}
	return st, nil
}

type variantFetch struct {
	once     sync.Once
	info     VariantInfo
	added    bool
	conflict bool
	err      error

	mu      sync.Mutex
	counted bool
}

// fetchListing fetches a listing and checks it hashes to its name.
func (s *Store) fetchListing(ctx context.Context, c *Client, sha string) (*Listing, error) {
	if !ValidHash(sha) {
		return nil, fmt.Errorf("bad listing hash %q", sha)
	}
	raw, err := c.Raw(ctx, ListingPath(sha))
	if err != nil {
		return nil, err
	}
	if Hash(raw) != sha {
		return nil, errors.New("its listing doesn't match its hash")
	}
	var l Listing
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, err
	}
	for _, v := range l.Versions {
		if !ValidHash(v.Source) {
			return nil, fmt.Errorf("bad source hash %q", v.Source)
		}
		for variant := range v.Variants {
			if !ValidVariant(variant) {
				return nil, fmt.Errorf("bad variant name %q", variant)
			}
		}
	}
	return &l, nil
}

// importVariant makes sure this repository has a variant, fetching it if
// need be (as a new generation here, if this repository has another). It
// returns what's stored here afterwards, whether it was added, and whether
// the two differed.
func (s *Store) importVariant(ctx context.Context, c *Client, source, variant string, remote VariantInfo, o ImportOptions) (VariantInfo, bool, bool, error) {
	cs, err := s.Contents(source)
	if err != nil {
		return VariantInfo{}, false, false, err
	}
	have := cs.Latest(variant)
	switch {
	case have != nil && have.SHA256 == remote.SHA256:
		return *have, false, false, nil
	case have != nil && !o.PreferRemote:
		return *have, false, true, nil
	}
	conflict := have != nil
	if o.DryRun {
		return remote, !conflict, conflict, nil
	}
	if !ValidHash(remote.SHA256) {
		return VariantInfo{}, false, false, fmt.Errorf("bad variant hash %q", remote.SHA256)
	}
	rel := VariantPath(source, variant, remote.SHA256)
	raw, err := c.Raw(ctx, rel)
	if err != nil {
		return VariantInfo{}, false, false, err
	}
	if Hash(raw) != remote.SHA256 {
		return VariantInfo{}, false, false, fmt.Errorf("%s doesn't match its hash", rel)
	}
	info, err := s.PutVariantRaw(source, variant, raw)
	if err != nil {
		return VariantInfo{}, false, false, fmt.Errorf("%s: %w", rel, err)
	}
	return info, !conflict, conflict, nil
}

func hasPackage(ps []Package, p Package) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}
