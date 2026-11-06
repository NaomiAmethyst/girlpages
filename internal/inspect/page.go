// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package inspect

import (
	"context"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// pageAddr is where a page is.
func pageAddr(name, section string) string {
	return "pages/" + strings.ReplaceAll(name, "/", "%2F") + "(" + section + ")"
}

// pageRow is a page, from a list of them.
func (r *Repo) pageRow(p repo.IndexPage) *Node {
	n := r.page(p.Name, p.Section, p.Page)
	n.Summary = pageLine(p)
	return n
}

// page is a page's listing, by its hash.
func (r *Repo) page(name, section, sha string) *Node {
	return r.listing(name, section, sha, false)
}

// listing is a page's listing. One asked for by its file may be an old one,
// which isn't a problem; one the indexes lead to should be the current one.
func (r *Repo) listing(name, section, sha string, byFile bool) *Node {
	addr := pageAddr(name, section)
	read := func(ctx context.Context) (*repo.Listing, error) {
		var l repo.Listing
		return &l, r.get(ctx, repo.ListingPath(valid(sha)), &l)
	}
	return &Node{
		r: r, Kind: "page", Addr: addr, File: repo.ListingPath(valid(sha)), Hash: sha,
		children: func(ctx context.Context, yield func(*Node) error) error {
			l, err := read(ctx)
			if err != nil {
				return err
			}
			if err := yield(r.pagePointer(name, section)); err != nil {
				return err
			}
			for _, v := range versionsOf(l) {
				if err := yield(r.version(addr, l, v)); err != nil {
					return err
				}
			}
			return nil
		},
		describe: func(ctx context.Context, w io.Writer) error {
			l, err := read(ctx)
			if err != nil {
				return err
			}
			describePage(w, l)
			return nil
		},
		cat: func(ctx context.Context, w io.Writer) error {
			l, err := read(ctx)
			if err != nil {
				return err
			}
			return r.catVersion(ctx, w, l.CurrentVersion())
		},
		check: func(ctx context.Context, data []byte, report Report) {
			var l repo.Listing
			if !decode(data, &l, report) {
				return
			}
			checkListing(&l, name, section, report)
			if name == "" {
				return
			}
			var pointer repo.Pointer
			if err := r.get(ctx, repo.NamePath(l.Name, l.Section), &pointer); err != nil {
				report(false, "its pointer: %v", err)
			} else if pointer.SHA256 != sha {
				report(byFile, "its pointer says %s", pointer.SHA256)
			}
		},
	}
}

// pagePointer is page/name-<section>/<name>.json.
func (r *Repo) pagePointer(name, section string) *Node {
	return &Node{
		r: r, Kind: "pointer", Addr: pageAddr(name, section) + "/pointer", File: repo.NamePath(name, section),
		Summary: "to its current listing",
		check: func(_ context.Context, data []byte, report Report) {
			var p repo.Pointer
			if !decode(data, &p, report) {
				return
			}
			if p.Name != name || p.Section != section {
				report(false, "it's for %s(%s)", p.Name, p.Section)
			}
			if !repo.ValidHash(p.SHA256) {
				report(false, "it has a bad hash %q", p.SHA256)
			}
		},
	}
}

// versionsOf is a page's versions, the latest first and then newest first.
func versionsOf(l *repo.Listing) []*repo.Version {
	vs := make([]*repo.Version, len(l.Versions))
	for i := range l.Versions {
		vs[i] = &l.Versions[i]
	}
	latest := l.CurrentVersion()
	sort.SliceStable(vs, func(i, j int) bool {
		if (vs[i] == latest) != (vs[j] == latest) {
			return vs[i] == latest
		}
		return vs[i].Newer(vs[j])
	})
	return vs
}

// slug is how a version's addressed: its upstream version if that's unique,
// or the start of its source hash.
func slug(l *repo.Listing, v *repo.Version) string {
	if v.Version != "" && !strings.Contains(v.Version, "/") {
		n := 0
		for i := range l.Versions {
			if l.Versions[i].Version == v.Version {
				n++
			}
		}
		if n == 1 {
			return v.Version
		}
	}
	return v.Source[:min(12, len(v.Source))]
}

func variantsOf(v *repo.Version) []string {
	names := slices.Sorted(maps.Keys(v.Variants))
	sort.SliceStable(names, func(i, j int) bool {
		rank := func(s string) int {
			switch s {
			case repo.VariantGirl:
				return 0
			case repo.VariantMan:
				return 2
			}
			return 1
		}
		return rank(names[i]) < rank(names[j])
	})
	return names
}

func describePage(w io.Writer, l *repo.Listing) {
	latest := l.CurrentVersion()
	if latest == nil {
		fmt.Fprintln(w, "it has no versions")
		return
	}
	stored := "(none)"
	if l.Current != "" {
		stored = l.Current
		if v := l.Find(l.Current); v != nil {
			stored = slug(l, v)
		}
	}
	if l.Current == latest.Source {
		fmt.Fprintf(w, "current: %s (%s)\n", slug(l, latest), latest.Version)
	} else {
		fmt.Fprintf(w, "current: %s (stored: %s, which differs: publishing again updates it)\n", slug(l, latest), stored)
	}
	if votes := l.PackageVotes(nil); len(votes) > 0 {
		fmt.Fprintf(w, "main package: %s\n", voteLine(votes[0]))
		if len(votes) > 1 {
			fmt.Fprintln(w, "other packages:")
			for _, v := range votes[1:] {
				fmt.Fprintf(w, "  %s\n", voteLine(v))
			}
		}
	}
	fmt.Fprintf(w, "\nversions (%d):\n", len(l.Versions))
	for _, v := range versionsOf(l) {
		mark := ""
		if v == latest {
			mark = "  ← current"
		}
		fmt.Fprintf(w, "  %-14s %-14s %s  [%s]%s\n", slug(l, v), v.Version, strings.Join(versionSources(v), ", "), strings.Join(variantsOf(v), " "), mark)
	}
	if keys := slices.Sorted(maps.Keys(l.Sources)); len(keys) > 0 {
		fmt.Fprintln(w, "\nwhat each source shows:")
		for _, k := range keys {
			label := l.Sources[k]
			if v := l.Find(l.Sources[k]); v != nil {
				label = slug(l, v)
			}
			fmt.Fprintf(w, "  %-28s %s\n", k, label)
		}
	}
}

func voteLine(v repo.PackageVote) string {
	best := v.Best
	if best == "" {
		best = "no version"
	}
	return fmt.Sprintf("%s %s (families: %s; systems: %s)", v.Name, best, strings.Join(v.Families, " "), strings.Join(v.Systems, " "))
}

// versionSources says where a version came from, shortly.
func versionSources(v *repo.Version) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range v.Packages {
		s := p.Source()
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"nowhere said"}
	}
	return out
}

func checkListing(l *repo.Listing, name, section string, report Report) {
	if name != "" && (l.Name != name || l.Section != section) {
		report(false, "it's %s(%s)'s", l.Name, l.Section)
	}
	if len(l.Versions) == 0 {
		report(false, "it has no versions")
		return
	}
	seen := map[string]bool{}
	for i := range l.Versions {
		v := &l.Versions[i]
		if !repo.ValidHash(v.Source) {
			report(false, "a version has a bad source hash %q", v.Source)
		}
		if seen[v.Source] {
			report(false, "version %s is there twice", v.Source)
		}
		seen[v.Source] = true
		if v.Version != v.Upstream() {
			report(false, "version %s says it's %q, but its packages make it %q", v.Source, v.Version, v.Upstream())
		}
		for variant, info := range v.Variants {
			if !repo.ValidVariant(variant) {
				report(false, "version %s has a variant called %q", v.Source, variant)
			}
			if !repo.ValidHash(info.SHA256) {
				report(false, "version %s's %s has a bad hash %q", v.Source, variant, info.SHA256)
			}
		}
	}
	if latest := l.Latest(); latest != nil && l.Current != latest.Source {
		report(false, "current is %s, but its latest is %s: publish again to update it", l.Current, latest.Source)
	}
	want := map[string]string{}
	for k, source := range derivedSources(l) {
		want[k] = source
	}
	if !reflect.DeepEqual(want, nonNil(l.Sources)) {
		report(false, "its sources aren't what its versions make them: publish again to update them")
	}
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// derivedSources is what a listing's sources should be.
func derivedSources(l *repo.Listing) map[string]string {
	copied := *l
	copied.Versions = append([]repo.Version(nil), l.Versions...)
	copied.Derive()
	return copied.Sources
}

// version is a version of a page: part of its listing.
func (r *Repo) version(page string, l *repo.Listing, v *repo.Version) *Node {
	addr := page + "/" + slug(l, v)
	bits := []string{v.Version, strings.Join(versionSources(v), ", "), "[" + strings.Join(variantsOf(v), " ") + "]"}
	if v.Source == l.Current || v == l.CurrentVersion() {
		bits = append(bits, "← current")
	}
	return &Node{
		r: r, Kind: "version", Addr: addr, Summary: strings.Join(bits, " "),
		value: func(context.Context) (any, error) { return v, nil },
		children: func(ctx context.Context, yield func(*Node) error) error {
			if err := yield(r.contents(addr, v.Source)); err != nil {
				return err
			}
			for _, name := range variantsOf(v) {
				if err := yield(r.variant(addr+"/"+name, v.Source, name, v.Variants[name])); err != nil {
					return err
				}
			}
			return nil
		},
		describe: func(_ context.Context, w io.Writer) error {
			fmt.Fprintf(w, "source: %s\n", v.Source)
			if v.Version != "" {
				fmt.Fprintf(w, "upstream version: %s\n", v.Version)
			}
			if v.Title != "" {
				fmt.Fprintf(w, "title: %s\n", v.Title)
			}
			if v.Description != "" {
				fmt.Fprintf(w, "description: %s\n", v.Description)
			}
			if !v.Added.IsZero() {
				fmt.Fprintf(w, "added: %s\n", v.Added.Format(time.RFC3339))
			}
			if v.License != "" {
				fmt.Fprintf(w, "license: %s\n", v.License)
			}
			for _, c := range v.Copyright {
				fmt.Fprintf(w, "copyright: %s\n", c)
			}
			fmt.Fprintf(w, "\npackages (%d):\n", len(v.Packages))
			for _, p := range v.Packages {
				line := strings.TrimSpace(strings.Join([]string{p.Source(), p.Release, p.Name, p.Version}, " "))
				if p.Arch != "" {
					line += " " + p.Arch
				}
				if p.License != "" {
					line += " (" + p.License + ")"
				}
				if p.URL != "" {
					line += " " + p.URL
				}
				fmt.Fprintf(w, "  %s\n", line)
			}
			fmt.Fprintf(w, "\nvariants (%d):\n", len(v.Variants))
			for _, name := range variantsOf(v) {
				info := v.Variants[name]
				fmt.Fprintf(w, "  %-10s %s, generation %d, %s\n", name, size(info.Size), info.Generation, info.SHA256)
			}
			return nil
		},
		cat: func(ctx context.Context, w io.Writer) error { return r.catVersion(ctx, w, v) },
		check: func(ctx context.Context, _ []byte, report Report) {
			var cs repo.Contents
			if err := r.get(ctx, repo.ContentsPath(valid(v.Source)), &cs); err != nil {
				report(false, "its variants.json: %v", err)
				return
			}
			for name, info := range v.Variants {
				if !repo.ValidVariant(name) {
					continue
				}
				g := cs.Latest(name)
				if g == nil {
					report(false, "its listing has %s, but its variants.json doesn't", name)
				} else if *g != info {
					report(false, "its listing has %s's generation %d (%s), but its variants.json's latest is %d (%s)", name, info.Generation, info.SHA256, g.Generation, g.SHA256)
				}
			}
		},
	}
}

// catVersion prints the variant of a version a reader would see first.
func (r *Repo) catVersion(ctx context.Context, w io.Writer, v *repo.Version) error {
	if v == nil {
		return fmt.Errorf("it has no versions")
	}
	names := variantsOf(v)
	if len(names) == 0 {
		return fmt.Errorf("it has no variants")
	}
	return r.catVariant(ctx, w, v.Source, names[0], v.Variants[names[0]].SHA256)
}

func (r *Repo) catVariant(ctx context.Context, w io.Writer, source, name, sha string) error {
	var doc repo.Variant
	if err := r.get(ctx, repo.VariantPath(valid(source), name, valid(sha)), &doc); err != nil {
		return err
	}
	_, err := io.WriteString(w, doc.Text())
	return err
}

// contents is a version's variants.json.
func (r *Repo) contents(version, source string) *Node {
	read := func(ctx context.Context) (*repo.Contents, error) {
		var cs repo.Contents
		return &cs, r.get(ctx, repo.ContentsPath(valid(source)), &cs)
	}
	return &Node{
		r: r, Kind: "contents", Addr: version + "/contents", File: repo.ContentsPath(valid(source)),
		Summary: "its variants, and every generation of them",
		describe: func(ctx context.Context, w io.Writer) error {
			cs, err := read(ctx)
			if err != nil {
				return err
			}
			for _, name := range slices.Sorted(maps.Keys(cs.Variants)) {
				fmt.Fprintf(w, "%s:\n", name)
				for _, g := range cs.Variants[name] {
					fmt.Fprintf(w, "  generation %d: %s, %s\n", g.Generation, size(g.Size), g.SHA256)
				}
			}
			return nil
		},
		check: func(_ context.Context, data []byte, report Report) {
			var cs repo.Contents
			if !decode(data, &cs, report) {
				return
			}
			if source != "" && cs.Source != source {
				report(false, "it's for %s", cs.Source)
			}
			for name, gens := range cs.Variants {
				seen := map[int]bool{}
				for _, g := range gens {
					if !repo.ValidHash(g.SHA256) {
						report(false, "%s has a bad hash %q", name, g.SHA256)
					}
					if seen[g.Generation] {
						report(false, "%s has generation %d twice", name, g.Generation)
					}
					seen[g.Generation] = true
				}
			}
		},
	}
}

// variant is a generation of a variant: the latest, unless it's addressed.
func (r *Repo) variant(addr, source, name string, info repo.VariantInfo) *Node {
	read := func(ctx context.Context) (*repo.Variant, error) {
		var doc repo.Variant
		return &doc, r.get(ctx, repo.VariantPath(valid(source), name, valid(info.SHA256)), &doc)
	}
	summary := size(info.Size)
	if info.Generation > 0 {
		summary += fmt.Sprintf(", generation %d", info.Generation)
	}
	return &Node{
		r: r, Kind: "variant", Addr: addr, File: repo.VariantPath(valid(source), name, valid(info.SHA256)), Hash: info.SHA256, Bytes: info.Size,
		Summary: summary,
		children: func(ctx context.Context, yield func(*Node) error) error {
			doc, err := read(ctx)
			if err != nil {
				return err
			}
			for i := range doc.Sections {
				if err := yield(r.heading(addr, doc, i)); err != nil {
					return err
				}
			}
			var cs repo.Contents
			if err := r.get(ctx, repo.ContentsPath(valid(source)), &cs); err != nil {
				return err
			}
			for _, g := range cs.Variants[name] {
				if g.SHA256 == info.SHA256 {
					continue
				}
				gen := r.variant(fmt.Sprintf("%s/generations/%d", addr, g.Generation), source, name, g)
				if err := yield(gen); err != nil {
					return err
				}
			}
			return nil
		},
		describe: func(ctx context.Context, w io.Writer) error {
			doc, err := read(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "source: %s\nsyntax: %s\n", doc.Source, doc.Syntax)
			if doc.Title != "" {
				fmt.Fprintf(w, "title: %s\n", doc.Title)
			}
			if g := doc.Generator; g != nil {
				fmt.Fprintf(w, "made by: %s %s, prompt version %d, %s\n", g.Backend, g.Model, g.PromptVersion, g.Generated.Format(time.RFC3339))
			}
			if info.PromptVersion > 0 {
				fmt.Fprintf(w, "prompt version (as listed): %d\n", info.PromptVersion)
			}
			if doc.Header != "" {
				fmt.Fprintf(w, "header: %s\n", doc.Header)
			}
			if doc.Footer != "" {
				fmt.Fprintf(w, "footer: %s\n", doc.Footer)
			}
			fmt.Fprintf(w, "\nheadings (%d):\n", len(doc.Sections))
			for i, s := range doc.Sections {
				fmt.Fprintf(w, "  %3d  %-40s %s\n", i, headingName(s), size(int64(len(s.Body))))
			}
			return nil
		},
		cat: func(ctx context.Context, w io.Writer) error {
			return r.catVariant(ctx, w, source, name, info.SHA256)
		},
		check: func(_ context.Context, data []byte, report Report) {
			var doc repo.Variant
			if !decode(data, &doc, report) {
				return
			}
			if source != "" && doc.Source != source {
				report(false, "it's %s's", doc.Source)
			}
			if name != "" && doc.Variant != name {
				report(false, "it's the %s variant", doc.Variant)
			}
			if doc.Syntax != repo.SyntaxMarkup && doc.Syntax != repo.SyntaxText {
				report(false, "its syntax is %q", doc.Syntax)
			}
			if len(doc.Sections) == 0 {
				report(false, "it has no sections")
			}
			if doc.Variant == repo.VariantMan && doc.Syntax != repo.SyntaxText {
				report(false, "the original isn't in %s", repo.SyntaxText)
			}
		},
	}
}

func headingName(s repo.Section) string {
	if s.Heading == "" {
		return "(before the first heading)"
	}
	return s.Heading
}

// heading is a section of a variant.
func (r *Repo) heading(variant string, doc *repo.Variant, i int) *Node {
	s := doc.Sections[i]
	return &Node{
		r: r, Kind: "heading", Addr: fmt.Sprintf("%s/headings/%d", variant, i),
		Summary: fmt.Sprintf("%s, %s", headingName(s), size(int64(len(s.Body)))),
		value:   func(context.Context) (any, error) { return s, nil },
		cat: func(_ context.Context, w io.Writer) error {
			_, err := io.WriteString(w, s.Body)
			return err
		},
	}
}
