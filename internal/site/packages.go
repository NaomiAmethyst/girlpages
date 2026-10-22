// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"fmt"
	"slices"
	"strings"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// packagesURL is the list of packages from a source ("" for all of them,
// "debian", "debian/trixie"): packages/, packages/debian/,
// packages/debian/trixie/. (A distribution can't have a release called
// "page", which its list's later pages are under.)
func (b *builder) packagesURL(src string) string {
	parts := []string{"packages"}
	if src != "" {
		parts = append(parts, strings.Split(src, "/")...)
	}
	return b.url(parts...)
}

// packageURL is a package's pages: package/<name>/ for every source's, or
// in the browse app, one source's (#/debian/trixie/package/<name>), as
// sections from a source are.
func (b *builder) packageURL(name, src string) string {
	if src != "" {
		return b.browseURL(src, "package", name)
	}
	return b.url("package", name)
}

// PackageDesc is what a package's entry in src's list says: how many pages
// it has there, and if those are all of them, the first few. (static's
// packageDesc says the same.)
func PackageDesc(r repo.PackageRow, src string) string {
	n := r.Pages
	if src != "" {
		n = r.Sources[src]
	}
	d := pluralOf(n, "page")
	if n == r.Pages && r.Sample != "" {
		d += ": " + r.Sample
	}
	return d
}

// packages writes the lists of packages, from the repository's package
// index: every package any page comes from, A to Z, and those from each
// source, each linking to its pages; and each package's pages, listed as a
// section's are.
func (b *builder) packages() error {
	_, rows, err := repo.PackageIndexOf(b.m, b.s.Read)
	if err != nil || len(rows) == 0 {
		return err
	}
	b.packageLists = map[string]int{}
	var jobs []func() error
	srcs := []string{""}
	for _, e := range b.m.Sources {
		srcs = append(srcs, e.Key())
	}
	for _, src := range srcs {
		var items []item
		for _, r := range rows {
			if src == "" || r.Sources[src] > 0 {
				items = append(items, item{Name: r.Name, Desc: PackageDesc(r, src), URL: b.packageURL(r.Name, src)})
			}
		}
		if len(items) == 0 {
			continue
		}
		where, from := "", "every source"
		if src != "" {
			where, from = " in "+sourceLabelOf(src), sourceLabelOf(src)
		}
		u := b.packagesURL(src)
		b.packageLists[u] = len(items)
		v := &view{
			Title:       "packages" + where,
			Description: fmt.Sprintf("Every package%s with man pages here, A to Z, each with its pages, girlified.", where),
			Heading:     "📦 packages" + where,
			Sub:         fmt.Sprintf("%s from %s", pluralOf(len(items), "package"), from),
			Sources:     b.packageChips(src),
			ListSource:  src,
		}
		jobs = append(jobs, b.listPages(u, v, items, "packages")...)
	}
	for _, r := range rows {
		jobs = append(jobs, func() error { return b.packagePage(r) })
	}
	return parallel(b.o.Ctx, b.o.Jobs, jobs)
}

// packagePage writes a package's listing: its pages, from every source.
func (b *builder) packagePage(r repo.PackageRow) error {
	var pp repo.PackagePages
	if err := b.s.Read(repo.ShardPath("package", r.List), &pp); err != nil {
		return err
	}
	items := make([]item, 0, len(pp.Pages))
	for _, p := range pp.Pages {
		items = append(items, item{Name: p.Name, Section: p.Section, Desc: p.Description,
			Series: manpage.IsSeries(p.Section), Prefix: seriesPrefix(p.Section),
			URL: b.pageURL(p.Name, p.Section), Girlified: slices.Contains(p.Variants, repo.VariantGirl)})
	}
	u := b.url("package", r.Name)
	b.mu.Lock()
	b.packageLists[u] = len(items)
	b.mu.Unlock()
	v := &view{
		Title:       r.Name + " · packages",
		Description: fmt.Sprintf("The man pages in the %s package, girlified: %s.", r.Name, PackageDesc(r, "")),
		Heading:     "📦 " + r.Name,
		Sub:         pluralOf(len(items), "page"),
		Sources:     b.packageSourceChips(r),
		ListPackage: r.Name,
		Parent:      &chip{Label: "📦 packages", URL: b.packagesURL("")},
	}
	for _, job := range b.listPages(u, v, items, "list") {
		if err := job(); err != nil {
			return err
		}
	}
	return nil
}

// listPages renders a listing of items at u, a page of listPage at a time,
// with template name.
func (b *builder) listPages(u string, v *view, items []item, name string) []func() error {
	lp, letters := paginate(u, items)
	var jobs []func() error
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
		jobs = append(jobs, func() error { return b.render(pu, name, &pv) })
	}
	return jobs
}

// packageChips lead to the lists of packages from each source, as
// sourceChips lead to their pages.
func (b *builder) packageChips(on string) []sourceMenu {
	return b.chipsFor(on, func(src string) string { return b.packagesURL(src) }, nil, "✨ every source")
}

// packageSourceChips lead to a package's pages from each source it's in.
func (b *builder) packageSourceChips(r repo.PackageRow) []sourceMenu {
	return b.chipsFor("", func(src string) string { return b.packageURL(r.Name, src) },
		func(src string) bool { return r.Sources[src] > 0 }, "✨ every source")
}

// chipsFor makes chips for the sources (those has takes, if it's given),
// linking to url(src), with on's on, everything first.
func (b *builder) chipsFor(on string, url func(src string) string, has func(string) bool, all string) []sourceMenu {
	if len(b.m.Sources) == 0 {
		return nil
	}
	out := []sourceMenu{{Label: all, URL: url(""), On: on == ""}}
	byDistro := map[string]int{}
	for _, src := range b.m.Sources {
		if has != nil && !has(src.Key()) {
			continue
		}
		u := url(src.Key())
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
