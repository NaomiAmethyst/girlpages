// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package remote reads pages straight from a published girlpages
// repository, as man reads them from the system: by name and section,
// whether or not they're installed here, in any version and variant the
// repository has. girl -r and girlc both use it.
package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/render"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// Repo reads from a repository.
type Repo struct {
	Client *repo.Client
	// Mode is how searches match: repo.ModeWords ("" too) or
	// repo.ModeNgrams.
	Mode string

	search *repo.Searcher
}

// New reads from the repository c.
func New(c *repo.Client) *Repo { return &Repo{Client: c} }

func (r *Repo) searcher() *repo.Searcher {
	if r.search == nil {
		r.search = &repo.Searcher{Client: r.Client, Mode: r.Mode}
	}
	return r.search
}

// Request is a page asked for: its name, and section if one was given.
type Request struct{ Name, Section string }

func (q Request) String() string {
	switch {
	case q.Section == "":
		return q.Name
	case manpage.IsSeries(q.Section):
		return strings.ToUpper(q.Section) + " " + q.Name
	}
	return q.Name + "(" + q.Section + ")"
}

// notFound is an error saying what isn't in the repository, that's
// repo.ErrNotFound.
type notFound string

func (e notFound) Error() string        { return string(e) }
func (e notFound) Is(target error) bool { return target == repo.ErrNotFound }

func missing(format string, args ...any) error { return notFound(fmt.Sprintf(format, args...)) }

// Requests reads man's arguments: "[section] page ...", where pages can
// also be written page(section), and a document in a series "rfc 9110"
// or "rfc9110".
func Requests(args []string) []Request {
	if len(args) == 2 && manpage.IsSeries(strings.ToLower(args[0])) && number(args[1]) {
		return []Request{{Name: strings.TrimLeft(args[1], "0"), Section: strings.ToLower(args[0])}}
	}
	sec := ""
	if len(args) > 1 && manpage.IsSection(args[0]) {
		sec, args = args[0], args[1:]
	}
	var out []Request
	for _, a := range args {
		q := Request{Name: a, Section: sec}
		if i := strings.LastIndexByte(a, '('); i > 0 && strings.HasSuffix(a, ")") && manpage.IsSection(a[i+1:len(a)-1]) {
			q = Request{Name: a[:i], Section: a[i+1 : len(a)-1]}
		} else if n, ok := strings.CutPrefix(strings.ToLower(a), "rfc"); ok && number(n) {
			q = Request{Name: strings.TrimLeft(n, "0"), Section: "rfc"}
		}
		out = append(out, q)
	}
	return out
}

func number(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != "" && strings.TrimLeft(s, "0") != ""
}

// Options say which pages to find, and which of each to show.
type Options struct {
	// Sections limits pages without a section of their own to these (as
	// man -S does: 3 is 3, 3p, 3ssl and so on).
	Sections []string
	// All finds every page by the name, not just the first.
	All bool
	// Version picks a version (see PickVersion); "" is the latest.
	Version string
	// Variant picks a variant; "" is the girly one, or the original if
	// there isn't one.
	Variant string
}

// Page is a page found, in the version and variant picked.
type Page struct {
	Listing *repo.Listing
	Version *repo.Version
	Variant *repo.Variant
	// Fallback is set when the girly variant was wanted (by default) but
	// there isn't one yet, so it's the original.
	Fallback bool
}

// Title is the page's NAME(SECTION).
func (p *Page) Title() string {
	if manpage.IsSeries(p.Listing.Section) {
		return strings.ToUpper(p.Listing.Section) + " " + p.Listing.Name
	}
	return strings.ToUpper(p.Listing.Name) + "(" + p.Listing.Section + ")"
}

// Latest reports whether the version shown is the page's latest.
func (p *Page) Latest() bool { return p.Version.Source == p.Listing.CurrentVersion().Source }

// Find finds the pages asked for: the first by man's section order, or
// with All, every one. If there aren't any, the error wraps
// repo.ErrNotFound.
func (r *Repo) Find(ctx context.Context, q Request, o Options) ([]Page, error) {
	listings, err := r.Listings(ctx, q, o)
	if err != nil {
		return nil, err
	}
	var out []Page
	for _, l := range listings {
		p, err := r.Pick(ctx, l, o)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// Listings finds the listings of the pages asked for (see Find).
func (r *Repo) Listings(ctx context.Context, q Request, o Options) ([]*repo.Listing, error) {
	if q.Name == "" {
		return nil, fmt.Errorf("no page name")
	}
	// A page by its own name and section is where its pointer says, without
	// the search index; for -a, or 3 meaning 3p too, it's looked up there.
	if q.Section != "" && !o.All {
		l, err := r.Client.Page(ctx, q.Name, q.Section)
		if err == nil {
			return []*repo.Listing{l}, nil
		}
		if !errors.Is(err, repo.ErrNotFound) {
			return nil, err
		}
	}
	hits, err := r.searcher().Named(ctx, q.Name)
	if err != nil {
		return nil, err
	}
	want := o.Sections
	if q.Section != "" {
		want = []string{q.Section}
	}
	var picked []repo.Hit
	for _, h := range hits {
		if len(want) == 0 || manpage.SectionMatches(h.Section, want) {
			picked = append(picked, h)
		}
	}
	// Spelled as asked before any other way.
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].Name == q.Name && picked[j].Name != q.Name })
	if len(picked) == 0 {
		return nil, missing("there's no %s in the repo", q)
	}
	if !o.All {
		picked = picked[:1]
	}
	var out []*repo.Listing
	for _, h := range picked {
		l, err := r.Client.Page(ctx, h.Name, h.Section)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// Pick fetches the version and variant of a page the options ask for.
func (r *Repo) Pick(ctx context.Context, l *repo.Listing, o Options) (*Page, error) {
	v, err := PickVersion(l, o.Version)
	if err != nil {
		return nil, err
	}
	variant, fallback, err := pickVariant(l, v, o.Variant)
	if err != nil {
		return nil, err
	}
	vv, err := r.Client.VariantAt(ctx, v.Source, variant, v.Variants[variant].SHA256)
	if err != nil {
		return nil, err
	}
	return &Page{Listing: l, Version: v, Variant: vv, Fallback: fallback}, nil
}

// PickVersion picks a version of a page: by its upstream version number
// (9.7), where it's from (debian, debian/trixie or "debian trixie"), or
// the start of its source hash; "" is the latest. If more than one is
// from the same place, it's the latest of them.
func PickVersion(l *repo.Listing, want string) (*repo.Version, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		if v := l.CurrentVersion(); v != nil {
			return v, nil
		}
		return nil, missing("%s(%s) has no versions", l.Name, l.Section)
	}
	var best *repo.Version
	better := func(v *repo.Version) {
		if best == nil || v.Newer(best) {
			best = v
		}
	}
	for i := range l.Versions {
		if v := &l.Versions[i]; v.Version == want || v.Upstream() == want {
			better(v)
		}
	}
	if best == nil && len(want) >= 4 {
		for i := range l.Versions {
			if v := &l.Versions[i]; strings.HasPrefix(v.Source, strings.ToLower(want)) {
				better(v)
			}
		}
	}
	if best == nil {
		distro, release, _ := strings.Cut(strings.Join(strings.Fields(strings.ReplaceAll(want, "/", " ")), " "), " ")
		best = l.VersionFrom(distro, release)
	}
	if best == nil {
		return nil, missing("%s(%s) has no version %q (--versions lists them)", l.Name, l.Section, want)
	}
	return best, nil
}

// pickVariant picks a variant of a version: the one wanted, or by default
// the girly one, or failing that the original.
func pickVariant(l *repo.Listing, v *repo.Version, want string) (variant string, fallback bool, err error) {
	if want != "" {
		if _, ok := v.Variants[want]; ok {
			return want, false, nil
		}
		return "", false, missing("%s(%s) %s has no %q variant, only %s", l.Name, l.Section, slug(l, v), want,
			strings.Join(variantNames(v), ", "))
	}
	if _, ok := v.Variants[repo.VariantGirl]; ok {
		return repo.VariantGirl, false, nil
	}
	if _, ok := v.Variants[repo.VariantMan]; ok {
		return repo.VariantMan, true, nil
	}
	if names := variantNames(v); len(names) > 0 {
		return names[0], true, nil
	}
	return "", false, missing("%s(%s) %s has nothing to read", l.Name, l.Section, slug(l, v))
}

// variantNames lists a version's variants, girl and man first.
func variantNames(v *repo.Version) []string {
	var out []string
	for name := range v.Variants {
		out = append(out, name)
	}
	rank := func(s string) int {
		switch s {
		case repo.VariantGirl:
			return 0
		case repo.VariantMan:
			return 1
		}
		return 2
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

// slug is how a version is picked (as the website's browse app names
// them): its upstream version number, if no other version of the page has
// it, else the start of its source hash.
func slug(l *repo.Listing, v *repo.Version) string {
	if v.Version != "" {
		n := 0
		for _, w := range l.Versions {
			if w.Version == v.Version {
				n++
			}
		}
		if n == 1 {
			return v.Version
		}
	}
	return v.Source[:12]
}

// WriteVersions lists a page's versions, latest first then the newest
// added, each with how to pick it, where it's from, and its variants.
func WriteVersions(w io.Writer, l *repo.Listing) error {
	vs := make([]*repo.Version, len(l.Versions))
	for i := range l.Versions {
		vs[i] = &l.Versions[i]
	}
	cur := l.CurrentVersion()
	sort.SliceStable(vs, func(i, j int) bool {
		if (vs[i] == cur) != (vs[j] == cur) {
			return vs[i] == cur
		}
		return vs[i].Added.After(vs[j].Added)
	})
	title := Request{Name: l.Name, Section: l.Section}.String()
	n := "1 version"
	if len(vs) != 1 {
		n = fmt.Sprintf("%d versions", len(vs))
	}
	fmt.Fprintf(w, "%s: %s\n", title, n)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, v := range vs {
		from := strings.Join(v.SourceLabels(), ", ")
		if from == "" {
			from = "-"
		}
		note := ""
		if v == cur {
			note = "  (latest)"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s%s\n", slug(l, v), from, strings.Join(variantNames(v), " "), note)
	}
	return tw.Flush()
}

// Text is a page ready to read: rendered, if it's in girlpages markup,
// else as it is.
func (p *Page) Text(o render.Options) string {
	if p.Variant.Syntax != repo.SyntaxMarkup {
		return render.StripControls(p.Variant.Text())
	}
	if o.Title == "" {
		o.Title = p.Title()
		if p.Variant.Title != "" {
			o.Title = p.Variant.Title
		}
	}
	return render.Render(p.Variant.Text(), o)
}

// About says which version a page is, if it isn't the latest ("version
// 9.4 from debian bookworm"), and which variant, if it's another girlpages
// markup one than the girly one ("pirate variant").
func (p *Page) About() string {
	var parts []string
	if !p.Latest() {
		s := slug(p.Listing, p.Version)
		if from := p.Version.SourceLabels(); len(from) > 0 {
			s += " from " + strings.Join(from, ", ")
		}
		parts = append(parts, "version "+s)
	}
	if v := p.Variant; v.Syntax == repo.SyntaxMarkup && v.Variant != repo.VariantGirl {
		parts = append(parts, v.Variant+" variant")
	}
	return strings.Join(parts, ", ")
}

// Girly is how to pick a version of the page that's girlified, if the one
// shown isn't and another is: its slug, for --page-version.
func (p *Page) Girly() string {
	if _, ok := p.Version.Variants[repo.VariantGirl]; ok {
		return ""
	}
	var best *repo.Version
	for i := range p.Listing.Versions {
		v := &p.Listing.Versions[i]
		if _, ok := v.Variants[repo.VariantGirl]; ok && (best == nil || v.Newer(best)) {
			best = v
		}
	}
	if best == nil {
		return ""
	}
	return slug(p.Listing, best)
}

// Search finds pages by what they're about, in the given sections (all
// if none).
func (r *Repo) Search(ctx context.Context, query string, sections []string, limit int) ([]repo.Hit, error) {
	n := limit
	if len(sections) > 0 && n > 0 {
		n *= 10 // some are left out
	}
	hits, err := r.searcher().Search(ctx, query, n)
	if err != nil {
		return nil, err
	}
	var out []repo.Hit
	for _, h := range hits {
		if len(sections) == 0 || manpage.SectionMatches(h.Section, sections) {
			out = append(out, h)
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// Whatis finds the pages asked for by name, for their one-line
// descriptions, as man -f does.
func (r *Repo) Whatis(ctx context.Context, q Request, sections []string) ([]repo.Hit, error) {
	hits, err := r.searcher().Named(ctx, q.Name)
	if err != nil {
		return nil, err
	}
	want := sections
	if q.Section != "" {
		want = []string{q.Section}
	}
	var out []repo.Hit
	for _, h := range hits {
		if len(want) == 0 || manpage.SectionMatches(h.Section, want) {
			out = append(out, h)
		}
	}
	return out, nil
}

// WriteHits writes pages found as apropos and whatis do: "ls (1) - list
// directory contents".
func WriteHits(w io.Writer, hits []repo.Hit) error {
	for _, h := range hits {
		desc := h.Description
		if desc == "" {
			desc = "(no description)"
		}
		if _, err := fmt.Fprintf(w, "%-20s - %s\n", h.Name+" ("+h.Section+")", desc); err != nil {
			return err
		}
	}
	return nil
}
