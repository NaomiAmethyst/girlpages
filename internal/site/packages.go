// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// packageSample is how many of a package's pages its entry names.
const packageSample = 6

// pkgEntry is a package in a list: its name (as it's first written), and
// its pages (as indexes into the build's pages).
type pkgEntry struct {
	name  string
	pages []int
}

// packageListsURL is the list of packages from a source ("" for all of
// them, "debian", "debian/trixie"): packages/, packages/debian/,
// packages/debian/trixie/. (A distribution can't have a release called
// "page", which its list's later pages are under.)
func (b *builder) packageListsURL(src string) string {
	parts := []string{"packages"}
	if src != "" {
		parts = append(parts, strings.Split(src, "/")...)
	}
	return b.url(parts...)
}

// packages writes the lists of packages: every package any page comes
// from, A to Z, and those from each source. Each links to the search for
// its pages (pkg:debian/coreutils), which lists them all.
func (b *builder) packages(pages []repo.PageRef) error {
	// source key ("" for all) → lower-cased package name → entry
	lists := map[string]map[string]*pkgEntry{"": {}}
	add := func(src, name string, i int) {
		m := lists[src]
		if m == nil {
			m = map[string]*pkgEntry{}
			lists[src] = m
		}
		k := strings.ToLower(name)
		e := m[k]
		if e == nil {
			e = &pkgEntry{name: name}
			m[k] = e
		}
		if n := len(e.pages); n == 0 || e.pages[n-1] != i {
			e.pages = append(e.pages, i)
		}
	}
	for i, p := range pages {
		for _, v := range p.Listing.Versions {
			for _, pk := range v.Packages {
				if pk.Name == "" || strings.ContainsAny(pk.Name, " \t\n") {
					continue
				}
				add("", pk.Name, i)
				if d := pk.Source(); d != "" {
					add(d, pk.Name, i)
					if pk.Release != "" && d != repo.Upstream {
						add(d+"/"+pk.Release, pk.Name, i)
					}
				}
			}
		}
	}
	if len(lists[""]) == 0 {
		return nil
	}
	b.packageLists = map[string]int{}
	var jobs []func() error
	for src, m := range lists {
		u := b.packageListsURL(src)
		b.packageLists[u] = len(m)
		items := make([]item, 0, len(m))
		for _, e := range m {
			items = append(items, b.packageItem(src, e, pages))
		}
		sort.Slice(items, func(i, j int) bool {
			return manpage.ListingLess(items[i].Name, "", items[j].Name, "")
		})
		where, label := "", "every source"
		if src != "" {
			where = " in " + sourceLabelOf(src)
			label = sourceLabelOf(src)
		}
		v := &view{
			Title:       "packages" + where,
			Description: fmt.Sprintf("Every package%s with man pages here, A to Z, each with its pages, girlified.", where),
			Heading:     "📦 packages" + where,
			Sub:         fmt.Sprintf("%s from %s", pluralOf(len(m), "package"), label),
			Sources:     b.packageChips(src),
		}
		lp, letters := paginate(u, items)
		for i, gs := range lp {
			pv := *v
			n := i + 1
			pv.Groups, pv.Letters = gs, letters
			pv.ListPage, pv.ListPages, pv.ListFirst = n, len(lp), u
			if len(lp) > 1 {
				pv.Pager = pager(u, n, len(lp))
				pv.Sub = fmt.Sprintf("%s · page %d of %d", v.Sub, n, len(lp))
			}
			if n > 1 {
				pv.Title = fmt.Sprintf("%s · page %d", v.Title, n)
				pv.Prev = listPageURL(u, n-1)
			}
			if n < len(lp) {
				pv.Next = listPageURL(u, n+1)
			}
			pu := listPageURL(u, n)
			jobs = append(jobs, func() error { return b.render(pu, "packages", &pv) })
		}
	}
	return parallel(b.o.Ctx, b.o.Jobs, jobs)
}

// packageItem is a package's entry in src's list: a link to the search for
// its pages (within src), saying how many there are and naming a few.
func (b *builder) packageItem(src string, e *pkgEntry, pages []repo.PageRef) item {
	key := strings.ToLower(e.name)
	q := url.Values{}
	if src != "" {
		distro, _, _ := strings.Cut(src, "/")
		if distro != repo.Upstream {
			key = distro + "/" + key
		}
		if src != distro || distro == repo.Upstream {
			q.Set("src", src)
		}
	}
	q.Set("q", repo.PackagePrefix+key)
	names := make([]item, len(e.pages))
	for i, pi := range e.pages {
		names[i] = item{Name: pages[pi].Pointer.Name, Section: pages[pi].Pointer.Section}
	}
	sort.Slice(names, func(i, j int) bool {
		return manpage.ListingLess(names[i].Name, names[i].Section, names[j].Name, names[j].Section)
	})
	var some []string
	for _, n := range names[:min(len(names), packageSample)] {
		some = append(some, n.Name+"("+n.Section+")")
	}
	desc := pluralOf(len(names), "page") + ": " + strings.Join(some, ", ")
	if len(names) > packageSample {
		desc += ", …"
	}
	return item{Name: e.name, Desc: desc, URL: b.o.Base + "search/?" + q.Encode()}
}

// packageChips lead to the lists of packages from each source, as
// sourceChips lead to their pages.
func (b *builder) packageChips(on string) []sourceMenu {
	if len(b.m.Sources) == 0 {
		return nil
	}
	out := []sourceMenu{{Label: "✨ every source", URL: b.packageListsURL(""), On: on == ""}}
	byDistro := map[string]int{}
	for _, src := range b.m.Sources {
		u := b.packageListsURL(src.Key())
		i, ok := byDistro[src.Distro]
		if !ok {
			i = len(out)
			byDistro[src.Distro] = i
			out = append(out, sourceMenu{Label: src.Distro})
		}
		if on == src.Key() || strings.HasPrefix(on, src.Distro+"/") {
			out[i].On = true
		}
		if src.Release == "" {
			out[i].URL = u
			out[i].Items = append([]chip{{Label: "all of " + src.Distro, URL: u, On: on == src.Key()}}, out[i].Items...)
		} else {
			out[i].Items = append(out[i].Items, chip{Label: src.Release, URL: u, On: on == src.Key()})
		}
	}
	for i := range out {
		if len(out[i].Items) == 1 && out[i].URL != "" {
			out[i].Items = nil
		}
	}
	return out
}

// sourceLabelOf is how a source key is written: "debian trixie".
func sourceLabelOf(key string) string { return strings.ReplaceAll(key, "/", " ") }

func pluralOf(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}
