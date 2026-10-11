// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package ingest

import (
	"context"
	"sort"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// Export writes girl's cached conversions into a repository and publishes
// it (without a website), so it can be imported or served straight away.
// Only pages whose conversion is up to date with the installed page are
// exported, each with the installed package it came from, as for System.
func Export(ctx context.Context, s *repo.Store, caches cache.Set, o Options) (Stats, *repo.Manifest, error) {
	seen := map[string]bool{}
	var pages []manpage.Page
	for _, d := range caches {
		err := d.Entries(func(e *cache.Entry) error {
			if seen[e.Source] || !e.Fresh() || !caches.HasObject(e.Hash) ||
				!manpage.SectionMatches(e.Section, o.Sections) {
				return nil
			}
			seen[e.Source] = true
			pages = append(pages, manpage.Page{Path: e.Source, Name: e.Name, Section: e.Section})
			return nil
		})
		if err != nil {
			return Stats{}, nil, err
		}
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Path < pages[j].Path })
	roots, _ := manpage.ManPath(ctx, o.Man) // best effort, for .so includes
	o.Cache = caches
	st, err := InstalledPages(ctx, s, pages, roots, o)
	if err != nil || o.DryRun {
		return st, nil, err
	}
	unlock, err := s.Lock()
	if err != nil {
		return st, nil, err
	}
	defer unlock()
	all, err := s.AllPages()
	if err != nil {
		return st, nil, err
	}
	if n, err := s.Refresh(all); err != nil {
		return st, nil, err
	} else if n > 0 {
		if all, err = s.AllPages(); err != nil {
			return st, nil, err
		}
	}
	m, err := s.Publish(all)
	return st, m, err
}
