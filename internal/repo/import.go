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
	Jobs   int
	DryRun bool
	Log    io.Writer
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
	var st ImportStats
	logf := func(format string, a ...any) {
		if o.Log != nil {
			fmt.Fprintf(o.Log, format, a...)
		}
	}
	m, err := c.Manifest(ctx)
	if err != nil {
		return st, err
	}
	var todo []IndexPage
	var secs []string
	for _, se := range m.Sections {
		if !manpage.SectionMatches(se.Section, o.Sections) {
			continue
		}
		var si SectionIndex
		if err := c.Get(ctx, SectionIndexPath(se.Section, se.SHA256), &si); err != nil {
			return st, err
		}
		for _, p := range si.Pages {
			if o.Match != nil && !o.Match.MatchString(p.Name) {
				continue
			}
			todo = append(todo, p)
			secs = append(secs, se.Section)
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
				ip, sec := todo[i], secs[i]
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
			if variant != VariantGirl && variant != VariantMan {
				return nil, fmt.Errorf("unknown variant %q", variant)
			}
		}
	}
	return &l, nil
}

// importVariant makes sure this repository has a variant, fetching it if
// need be. It returns what's stored here afterwards, whether it was added,
// and whether the two differed.
func (s *Store) importVariant(ctx context.Context, c *Client, source, variant string, remote VariantInfo, o ImportOptions) (VariantInfo, bool, bool, error) {
	rel := ContentPath(source, variant)
	have, err := s.ReadRaw(rel)
	switch {
	case err == nil && Hash(have) == remote.SHA256:
		return remote, false, false, nil
	case err == nil && !o.PreferRemote:
		return VariantInfo{SHA256: Hash(have), Size: int64(len(have))}, false, true, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return VariantInfo{}, false, false, err
	}
	conflict := err == nil
	if o.DryRun {
		return remote, !conflict, conflict, nil
	}
	raw, err := c.Raw(ctx, rel)
	if err != nil {
		return VariantInfo{}, false, false, err
	}
	if Hash(raw) != remote.SHA256 {
		return VariantInfo{}, false, false, fmt.Errorf("%s doesn't match the hash its listing gives", rel)
	}
	var v Variant
	if err := json.Unmarshal(raw, &v); err != nil {
		return VariantInfo{}, false, false, fmt.Errorf("%s: %w", rel, err)
	}
	if v.Source != source || v.Variant != variant {
		return VariantInfo{}, false, false, fmt.Errorf("%s claims to be %s/%s", rel, v.Source, v.Variant)
	}
	if err := s.WriteRaw(rel, raw); err != nil {
		return VariantInfo{}, false, false, err
	}
	return VariantInfo{SHA256: remote.SHA256, Size: int64(len(raw))}, !conflict, conflict, nil
}

func hasPackage(ps []Package, p Package) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}
