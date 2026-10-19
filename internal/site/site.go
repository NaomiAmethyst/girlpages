// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package site builds a static website from a girlpages repository.
//
// Only what search engines should see is static: the home page, the
// section listings, and the latest version (by version number) of every
// page, girlified if it has been, with canonical links, OpenGraph and
// JSON-LD. Everything else (originals, older versions, browsing by
// distribution or release, and search) is done in the browser by
// the home page's hash and /search/, straight from the repository. The site only reads
// the repository, so anyone can build one from a published repository.
package site

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/render"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
	"github.com/NaomiAmethyst/girlpages/internal/rfc"
	"github.com/NaomiAmethyst/girlpages/internal/web"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*.js
var staticFS embed.FS

// SearchJS is the browser search module.
func SearchJS() string { return staticJS("search.js") }

func staticJS(name string) string {
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		panic(err)
	}
	return string(b)
}

var templates = map[string]*template.Template{}

func init() {
	funcs := template.FuncMap{"pages": func(n int) string {
		if n == 1 {
			return "1 page"
		}
		return fmt.Sprintf("%d pages", n)
	}}
	for _, name := range []string{"home", "list", "page", "search", "browse", "notfound", "offline"} {
		templates[name] = template.Must(template.New(name).Funcs(funcs).ParseFS(templateFS,
			"templates/layout.html", "templates/"+name+".html"))
	}
}

// Options controls a build.
type Options struct {
	// Out is the site's root directory. The repository is expected at
	// Out/repo, so the browser can find it.
	Out string
	// Base is the URL path the site is served from, such as "/" or
	// "/girlpages/".
	Base string
	// URL is the site's absolute URL, for canonical links, OpenGraph,
	// JSON-LD and the sitemap. Sites meant for search engines need it.
	URL     string
	Version string
	Log     io.Writer
	// Progress, if set, is told how the build is going.
	Progress repo.Progress
	// Written, if set, is told of each file written, so it can be
	// compressed straight away.
	Written func(path string)
	// Ctx, if set, stops the build early when it's done.
	Ctx context.Context
	// Jobs is how many files are written at once (one per CPU if 0).
	Jobs int
}

// Stats summarises a build.
type Stats struct {
	Written, Unchanged, Removed int
}

type builder struct {
	o       Options
	mu      sync.Mutex // guards st and written
	st      Stats
	s       *repo.Store
	m       *repo.Manifest
	written map[string]bool
	byName  map[string][]string // name → sections
}

// Build writes the site.
func Build(s *repo.Store, pages []repo.PageRef, m *repo.Manifest, o Options) (Stats, error) {
	if o.Base == "" {
		o.Base = "/"
	}
	if !strings.HasSuffix(o.Base, "/") {
		o.Base += "/"
	}
	o.URL = strings.TrimSuffix(o.URL, "/")
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Ctx == nil {
		o.Ctx = context.Background()
	}
	if o.Jobs <= 0 {
		o.Jobs = runtime.NumCPU()
	}
	b := &builder{o: o, s: s, m: m, written: map[string]bool{}, byName: map[string][]string{}}
	for _, p := range pages {
		b.byName[p.Pointer.Name] = append(b.byName[p.Pointer.Name], p.Pointer.Section)
	}
	steps := []func() error{
		b.static,
		func() error { return b.home(pages) },
		func() error { return b.sections(pages) },
		func() error { return b.pages(pages) },
		b.search,
		b.browse,
		func() error { return b.sitemap(pages) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return b.st, err
		}
	}
	return b.st, b.prune()
}

// view is everything a template might want.
type view struct {
	Title, Description, Canonical string
	Base, Version, Query          string
	NoIndex                       bool
	// OpenGraph: the kind of thing ("website", "article"), an absolute
	// image URL, and when an article changed.
	OGType, OGImage, Modified string
	// JSONLD is structured data about the page, already JSON.
	JSONLD template.JS

	// home
	Sections         []card
	Total, Girlified int
	Generated        string

	// Sources lead to browsing by distribution or release.
	Sources []sourceMenu

	// list
	Heading, Sub string
	Chips        []chip
	// SaveSection, on a section's listing, is the section a button saves
	// for offline reading (in browsers that can).
	SaveSection string
	Letters     []letter
	Groups      []group
	// A long listing is split into pages of listPage items: this is one
	// of them (ListPage, from 1, of ListPages), with links around it.
	ListPage, ListPages int
	ListFirst           string // the first page's URL, which the others go under
	// ListKey and ListSub are the chapter and section (or "") listed, for
	// the browse app to make the listing again from the repository.
	ListKey, ListSub string
	Pager            []pagerLink
	Prev, Next       string

	// page
	Page *pageView
}

type card struct {
	Key, Name, Emoji, URL string
	Count, Girlified      int
}

type chip struct {
	Label, URL string
	On         bool
}

// sourceMenu is a chip among the sources: a link, or for a distribution
// with releases, a menu of them (Items).
type sourceMenu struct {
	Label, URL string
	On         bool
	Items      []chip
}

type group struct {
	Letter string
	// Cont is a group carried on from the page before.
	Cont  bool
	Items []item
}

// A letter jumps to where its names start in a listing, on whichever page.
type letter struct {
	Letter, URL string
}

type pagerLink struct {
	Label, URL string
	On         bool
}

// listPage is how many pages of the manual each page of a listing shows.
var listPage = 200

// listPageURL is page n (from 1) of the listing at u.
func listPageURL(u string, n int) string {
	if n <= 1 {
		return u
	}
	return fmt.Sprintf("%spage/%d/", u, n)
}

type item struct {
	Name, Section, Desc, URL string
	Girlified                bool
	// Series items are shown by Prefix and name ("RFC 791"), not with
	// their section.
	Series bool
	Prefix string
}

// sectionTitle names a section in titles: "section 3", or for a series of
// documents, nothing but its name follows.
func sectionTitle(sec string) string {
	if manpage.IsSeries(sec) {
		name, _ := web.SectionName(sec)
		return name
	}
	return "section " + sec
}

// listTitle titles a listing: "section 3 · Library functions 📚", or a
// series' name alone.
func listTitle(sec, name, emoji string) string {
	t := name
	if !manpage.IsSeries(sec) {
		t = "section " + sec + " · " + name
	}
	if emoji != "" {
		t += " " + emoji
	}
	return t
}

// seriesPrefix is how a series' documents are named before their number.
func seriesPrefix(sec string) string {
	if manpage.IsSeries(sec) {
		return strings.ToUpper(sec) + " "
	}
	return ""
}

// versionLinks lists a page's versions for switching between them: the
// latest (whose page is u) first, then the newest added, each named by its
// upstream version and where it's from. The others are routes in the
// browse app, by version (as its slugs are: the upstream version, if no
// other version has it, else the start of the source hash).
func versionLinks(l *repo.Listing, u string, route func(slug string) string) []pagerLink {
	vs := append([]repo.Version(nil), l.Versions...)
	sort.SliceStable(vs, func(i, j int) bool {
		if (vs[i].Source == l.Current) != (vs[j].Source == l.Current) {
			return vs[i].Source == l.Current
		}
		return vs[i].Added.After(vs[j].Added)
	})
	uses := map[string]int{}
	for _, v := range vs {
		uses[v.Version]++
	}
	var out []pagerLink
	for _, v := range vs {
		label := strings.Join(v.SourceLabels(), ", ")
		if v.Version != "" {
			label = v.Version + " · " + label
		}
		if v.Source == l.Current {
			out = append(out, pagerLink{Label: label + " (latest)", URL: u, On: true})
			continue
		}
		slug := v.Source[:12]
		if v.Version != "" && uses[v.Version] == 1 {
			slug = v.Version
		}
		out = append(out, pagerLink{Label: label, URL: route(slug)})
	}
	return out
}

// published is a "2022-06" date as "June 2022".
func published(d string) string {
	if t, err := time.Parse("2006-01", d); err == nil {
		return t.Format("January 2006")
	}
	return d
}

type pageView struct {
	Name, Section, Title, Description string
	SectionURL, URL                   string
	// BrowseURL is the page in the browser app, for originals and other
	// versions.
	BrowseURL       string
	Body            template.HTML
	TOC             []render.Heading
	Girlified       bool
	Model, Modified string
	Packages        []string
	// In lists the sources this version is in, each linking to the page as
	// that source has it; Versions is how many versions there are.
	In []chip
	// SectionLabel names the page's section in breadcrumbs ("section 1",
	// "Requests for Comments"). A document in a series shows its
	// description under its title, and when it was published, not a
	// version (Published: "June 2022").
	SectionLabel string
	Series       bool
	Published    string
	// VersionLinks switch between its versions (if it has more than one):
	// this one first, then the newest added.
	VersionLinks []pagerLink
	Versions     int
	Version      string // upstream version number
}

func (b *builder) url(parts ...string) string {
	u := b.o.Base
	for _, p := range parts {
		u += url.PathEscape(SegmentName(p)) + "/"
	}
	return u
}

// SegmentName is the directory a page name or section gets on the site:
// itself, unless it starts with a dot (which hosts tend to hide), when it's
// escaped like a repository file name.
func SegmentName(s string) string {
	if strings.HasPrefix(s, ".") {
		return repo.EscapeName(s)
	}
	return s
}

// pageURL is a page's: man/<sec>/<name>/, or <series>/<name>/ for a
// series of documents (rfc/9110/).
func (b *builder) pageURL(name, sec string) string {
	if manpage.IsSeries(sec) {
		return b.url(sec, name)
	}
	return b.url("man", sec, name)
}

// listURL is a listing: a chapter's (section/3/, for sec ""), one of its
// sections' (section/3/3ssl/, or section/3/3/ for just 3), or a series'
// (rfc/).
func (b *builder) listURL(key, sec string) string {
	switch {
	case manpage.IsSeries(key):
		return b.url(key)
	case sec != "":
		return b.url("section", key, sec)
	}
	return b.url("section", key)
}

// sectionURL is the listing a page's section is on: its chapter's, or for
// a section like 3ssl, its own.
func (b *builder) sectionURL(sec string) string {
	if key := mainKey(sec); key != sec {
		return b.listURL(key, sec)
	}
	return b.listURL(sec, "")
}

// browseURL is a view in the browse app, which lives in the home page's
// hash: #/[<source>/]<segments>, as browseURL in static/girlpages.js makes
// them (<sec>/<name> for a page, section/<key> for a listing).
func (b *builder) browseURL(src string, segs ...string) string {
	var out []string
	if src != "" {
		segs = append(strings.Split(src, "/"), segs...)
	}
	for _, s := range segs {
		out = append(out, url.PathEscape(s))
	}
	return b.o.Base + "#/" + strings.Join(out, "/")
}

// file maps a site URL path to the file that serves it.
func (b *builder) file(u string) string {
	rel := strings.TrimPrefix(u, b.o.Base)
	if rel == "" || strings.HasSuffix(rel, "/") {
		rel += "index.html"
	}
	var parts []string
	for _, p := range strings.Split(rel, "/") {
		if q, err := url.PathUnescape(p); err == nil {
			p = q
		}
		parts = append(parts, p)
	}
	return filepath.Join(b.o.Out, filepath.Join(parts...))
}

// abs makes a site path absolute, if the site's URL is known.
func (b *builder) abs(u string) string {
	if b.o.URL == "" {
		return ""
	}
	return b.o.URL + strings.TrimPrefix(u, strings.TrimSuffix(b.o.Base, "/"))
}

func (b *builder) write(path string, data []byte) error {
	b.mu.Lock()
	b.written[path] = true
	b.mu.Unlock()
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		b.mu.Lock()
		b.st.Unchanged++
		b.mu.Unlock()
		return nil
	}
	b.mu.Lock()
	b.st.Written++
	b.mu.Unlock()
	if err := repo.WriteFile(path, data); err != nil {
		return err
	}
	if b.o.Written != nil {
		b.o.Written(path)
	}
	return nil
}

func (b *builder) render(u, name string, v *view) error {
	v.Base, v.Version = b.o.Base, b.o.Version
	if v.Canonical == "" && !v.NoIndex {
		v.Canonical = b.abs(u)
	}
	if v.OGType == "" {
		v.OGType = "website"
	}
	v.OGImage = b.abs(b.o.Base + "static/icon-512.png")
	var buf bytes.Buffer
	if err := templates[name].ExecuteTemplate(&buf, "layout", v); err != nil {
		return err
	}
	return b.write(b.file(u), buf.Bytes())
}

func (b *builder) static() error {
	files := map[string][]byte{"style.css": []byte(web.StyleCSS())}
	for name, data := range appIcons() {
		files[name] = data
	}
	scripts, err := fs.ReadDir(staticFS, "static")
	if err != nil {
		return err
	}
	for _, e := range scripts {
		if e.Name() != "sw.js" {
			files[e.Name()] = []byte(staticJS(e.Name()))
		}
	}
	// What the service worker keeps for offline use. Changing any of it
	// changes its version, so browsers fetch the new ones.
	shell := []string{"", "search/", "browse/", "offline/", "manifest.webmanifest"}
	h := sha256.New()
	io.WriteString(h, b.o.Version)
	sw := staticJS("sw.js")
	io.WriteString(h, sw)
	for _, name := range sortedKeys(files) {
		if err := b.write(filepath.Join(b.o.Out, "static", name), files[name]); err != nil {
			return err
		}
		shell = append(shell, "static/"+name)
		h.Write(files[name])
	}

	manifest, err := json.MarshalIndent(map[string]any{
		"name":             "girlpages",
		"short_name":       "girlpages",
		"description":      "the manual, but make it girly 💅",
		"start_url":        b.o.Base,
		"scope":            b.o.Base,
		"display":          "standalone",
		"background_color": "#fff6fb",
		"theme_color":      "#ff3e9a",
		"icons": []map[string]string{
			{"src": "static/icon-192.png", "sizes": "192x192", "type": "image/png"},
			{"src": "static/icon-512.png", "sizes": "512x512", "type": "image/png"},
			{"src": "static/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
			{"src": "static/icon.svg", "sizes": "any", "type": "image/svg+xml"},
		},
		"shortcuts": []map[string]string{{"name": "search", "url": b.o.Base + "search/"}},
	}, "", "  ")
	if err != nil {
		return err
	}
	h.Write(manifest)
	if err := b.write(filepath.Join(b.o.Out, "manifest.webmanifest"), manifest); err != nil {
		return err
	}
	if err := b.render(b.o.Base+"offline/", "offline", &view{Title: "offline", NoIndex: true}); err != nil {
		return err
	}

	js := func(v any) string {
		out, _ := json.Marshal(v)
		return string(out)
	}
	sw = strings.NewReplacer(
		`/*BASE*/"/"`, js(b.o.Base),
		`/*VERSION*/"dev"`, js(hex.EncodeToString(h.Sum(nil))[:16]),
		`/*SHELL*/[]`, js(shell),
	).Replace(sw)
	if err := b.write(filepath.Join(b.o.Out, "sw.js"), []byte(sw)); err != nil {
		return err
	}

	// GitHub Pages would otherwise hide files starting with _ or .
	if err := b.write(filepath.Join(b.o.Out, ".nojekyll"), nil); err != nil {
		return err
	}
	robots := "User-agent: *\nDisallow: " + b.o.Base + "repo/\nDisallow: " + b.o.Base + "browse/\n"
	if b.o.URL != "" {
		robots += "Sitemap: " + b.o.URL + "/sitemap.xml\n"
	}
	if err := b.write(filepath.Join(b.o.Out, "robots.txt"), []byte(robots)); err != nil {
		return err
	}
	return b.render(b.o.Base+"404.html", "notfound", &view{Title: "not found", NoIndex: true})
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mainKey(sec string) string { return manpage.Chapter(sec) }

func girlified(v *repo.Version) bool {
	_, ok := v.Variants[repo.VariantGirl]
	return ok
}

// sourceChips lead to browsing everything, or a section, by source.
// sourceChips are the sources to browse by (those with pages in section,
// if it's given): everything, then each distribution, with a menu of its
// releases if it has any. web's sourceNav makes the same.
func (b *builder) sourceChips(section string) []sourceMenu {
	if len(b.m.Sources) == 0 {
		return nil
	}
	all := sourceMenu{Label: "✨ latest of everything", URL: b.url(), On: true}
	if section != "" {
		all.URL = b.listURL(section, "")
	}
	out := []sourceMenu{all}
	byDistro := map[string]int{} // index in out
	for _, src := range b.m.Sources {
		if section != "" && src.Sections[section] == 0 {
			continue
		}
		u := b.browseURL(src.Key())
		if section != "" {
			u = b.browseURL(src.Key(), "section", section)
		}
		i, ok := byDistro[src.Distro]
		if !ok {
			i = len(out)
			byDistro[src.Distro] = i
			out = append(out, sourceMenu{Label: src.Distro})
		}
		if src.Release == "" {
			out[i].URL = u
			out[i].Items = append([]chip{{Label: "all of " + src.Distro, URL: u}}, out[i].Items...)
		} else {
			out[i].Items = append(out[i].Items, chip{Label: src.Release, URL: u})
		}
	}
	// A distribution with no releases is just a link.
	for i := range out {
		if len(out[i].Items) == 1 && out[i].URL != "" {
			out[i].Items = nil
		}
	}
	return out
}

func (b *builder) home(pages []repo.PageRef) error {
	cards := map[string]*card{}
	v := &view{Title: "the manual, but make it girly",
		Description: "Every Unix man page, rewritten by an LLM in the voice of a Gen Z girl, with every detail kept. 💅",
		Generated:   b.m.Generated.Format(time.DateOnly), Sources: b.sourceChips("")}
	for _, p := range pages {
		key := mainKey(p.Pointer.Section)
		c := cards[key]
		if c == nil {
			name, emoji := web.SectionName(key)
			c = &card{Key: key, Name: name, Emoji: emoji, URL: b.listURL(key, "")}
			cards[key] = c
		}
		c.Count++
		v.Total++
		if p.Girlified() {
			c.Girlified++
			v.Girlified++
		}
	}
	for _, c := range cards {
		v.Sections = append(v.Sections, *c)
	}
	sort.Slice(v.Sections, func(i, j int) bool { return sectionLess(v.Sections[i].Key, v.Sections[j].Key) })
	v.JSONLD = b.ldWebSite(v)
	return b.render(b.o.Base, "home", v)
}

func isDigit(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

func sectionLess(a, b string) bool {
	if isDigit(a) != isDigit(b) {
		return isDigit(a)
	}
	return a < b
}

// sections writes a page for each main section (1, 3…) listing all its
// subsections' pages, and one for each subsection (3p, 3ssl…).
func (b *builder) sections(pages []repo.PageRef) error {
	byKey := map[string][]repo.PageRef{}
	subs := map[string]map[string]int{}
	for _, p := range pages {
		key := mainKey(p.Pointer.Section)
		byKey[key] = append(byKey[key], p)
		if subs[key] == nil {
			subs[key] = map[string]int{}
		}
		subs[key][p.Pointer.Section]++
	}
	var jobs []func() error // rendering, done in parallel at the end
	for key, ps := range byKey {
		var secs []string
		for s := range subs[key] {
			secs = append(secs, s)
		}
		sort.Strings(secs)
		name, emoji := web.SectionName(key)
		chipsFor := func(on string) []chip {
			if len(secs) < 2 {
				return nil
			}
			cs := []chip{{Label: "all", URL: b.listURL(key, ""), On: on == ""}}
			for _, s := range secs {
				cs = append(cs, chip{Label: fmt.Sprintf("%s (%d)", s, subs[key][s]), URL: b.listURL(key, s), On: on == s})
			}
			return cs
		}
		list := func(sec string, ps []repo.PageRef) error {
			label, u := key, b.listURL(key, "")
			if sec != "" {
				label, u = sec, b.listURL(key, sec)
			}
			v := &view{
				Title:       listTitle(label, strings.ToLower(name), ""),
				Description: fmt.Sprintf("%s: every page in section %s of the manual, girlified.", name, label),
				Heading:     listTitle(label, name, emoji),
				Sub:         pluralPages(len(ps)),
				Chips:       chipsFor(sec),
				Sources:     b.sourceChips(key),
				SaveSection: label,
			}
			var items []item
			for _, p := range ps {
				cur := p.Current()
				items = append(items, item{Name: p.Pointer.Name, Section: p.Pointer.Section, Desc: cur.Description,
					Series: manpage.IsSeries(p.Pointer.Section), Prefix: seriesPrefix(p.Pointer.Section),
					URL: b.pageURL(p.Pointer.Name, p.Pointer.Section), Girlified: p.Girlified()})
			}
			sort.SliceStable(items, func(i, j int) bool {
				return manpage.ListingLess(items[i].Name, items[i].Section, items[j].Name, items[j].Section)
			})
			pages, letters := paginate(u, items)
			if len(pages) == 0 {
				pages = [][]group{nil}
			}
			for i, gs := range pages {
				pv := *v
				n := i + 1
				pv.Groups, pv.Letters = gs, letters
				pv.ListPage, pv.ListPages, pv.ListFirst = n, len(pages), u
				pv.ListKey, pv.ListSub = key, sec
				if len(pages) > 1 {
					pv.Pager = pager(u, n, len(pages))
					pv.Sub = fmt.Sprintf("%s · page %d of %d", v.Sub, n, len(pages))
				}
				if n > 1 {
					pv.Title = fmt.Sprintf("%s · page %d", v.Title, n)
					pv.Prev = listPageURL(u, n-1)
				}
				if n < len(pages) {
					pv.Next = listPageURL(u, n+1)
				}
				var its []item
				for _, g := range gs {
					its = append(its, g.Items...)
				}
				pu := listPageURL(u, n)
				jobs = append(jobs, func() error {
					pv.JSONLD = b.ldCollection(&pv, pu, its)
					return b.render(pu, "list", &pv)
				})
			}
			return nil
		}
		if err := list("", ps); err != nil {
			return err
		}
		if len(secs) > 1 {
			for _, sec := range secs {
				var sp []repo.PageRef
				for _, p := range ps {
					if p.Pointer.Section == sec {
						sp = append(sp, p)
					}
				}
				if err := list(sec, sp); err != nil {
					return err
				}
			}
		}
	}
	return parallel(b.o.Ctx, b.o.Jobs, jobs)
}

// parallel runs jobs, workers at a time, and joins their errors; if
// ctx is done, it runs no more, and says so.
func parallel(ctx context.Context, workers int, jobs []func() error) error {
	errs := make([]error, len(jobs))
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(jobs) || ctx.Err() != nil {
					return
				}
				errs[i] = jobs[i]()
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.Join(errs...)
}

func pluralPages(n int) string {
	if n == 1 {
		return "1 page"
	}
	return fmt.Sprintf("%d pages", n)
}

// letterOf is the group a name is listed under.
func letterOf(name string) string { return manpage.Letter(name) }

// paginate splits a listing at u into pages of listPage items, grouped by
// first letter; letters link to the page each starts on.
func paginate(u string, items []item) (pages [][]group, letters []letter) {
	prev := ""
	for i, it := range items {
		n := i / listPage
		if i%listPage == 0 {
			pages = append(pages, nil)
		}
		l := letterOf(it.Name)
		gs := pages[n]
		if len(gs) == 0 || gs[len(gs)-1].Letter != l {
			gs = append(gs, group{Letter: l, Cont: l == prev})
			if l != prev {
				letters = append(letters, letter{Letter: l, URL: listPageURL(u, n+1) + "#letter-" + l})
			}
		}
		gs[len(gs)-1].Items = append(gs[len(gs)-1].Items, it)
		pages[n] = gs
		prev = l
	}
	return pages, letters
}

// pager is the links between a listing's pages: all of them if there are
// few, else the first, last, and those around this one.
func pager(u string, n, total int) []pagerLink {
	var out []pagerLink
	gap := false
	for i := 1; i <= total; i++ {
		if total > 9 && i != 1 && i != total && (i < n-2 || i > n+2) {
			if !gap {
				out = append(out, pagerLink{Label: "…"})
			}
			gap = true
			continue
		}
		gap = false
		out = append(out, pagerLink{Label: strconv.Itoa(i), URL: listPageURL(u, i), On: i == n})
	}
	return out
}

// link is the URL for a reference like printf(3), or "" if there's no such
// page. A bare section number finds subsections too (3 → 3ssl).
func (b *builder) link(name, sec string) string {
	secs := b.byName[name]
	for _, s := range secs {
		if s == sec {
			return b.pageURL(name, s)
		}
	}
	for _, s := range secs {
		if strings.HasPrefix(s, sec) {
			return b.pageURL(name, s)
		}
	}
	return ""
}

// pages writes each page's latest version.
func (b *builder) pages(pages []repo.PageRef) error {
	errs := make([]error, len(pages))
	var next, done atomic.Int64
	report := func(n int) {
		if b.o.Progress != nil {
			b.o.Progress("writing pages", n, len(pages))
		}
	}
	report(0)
	var wg sync.WaitGroup
	for w := 0; w < b.o.Jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(pages) || b.o.Ctx.Err() != nil {
					return
				}
				err := b.page(pages[i])
				report(int(done.Add(1)))
				if errors.Is(err, repo.ErrNotFound) {
					b.mu.Lock()
					fmt.Fprintf(b.o.Log, "💔 %s(%s): %v\n", pages[i].Pointer.Name, pages[i].Pointer.Section, err)
					b.mu.Unlock()
					err = nil
				}
				errs[i] = err
			}
		}()
	}
	wg.Wait()
	if err := b.o.Ctx.Err(); err != nil {
		return err
	}
	return errors.Join(errs...)
}

func originalSections(v *repo.Variant) []render.OriginalSection {
	out := make([]render.OriginalSection, len(v.Sections))
	for i, s := range v.Sections {
		out[i] = render.OriginalSection{Heading: s.Heading, Body: s.Body}
	}
	return out
}

func (b *builder) page(p repo.PageRef) error {
	name, sec := p.Pointer.Name, p.Pointer.Section
	ver := p.Current()
	u := b.pageURL(name, sec)
	title := ver.Title
	if title == "" {
		title = strings.ToUpper(name) + "(" + sec + ")"
	}
	pv := &pageView{Name: name, Section: sec, Title: title, Description: ver.Description,
		SectionURL: b.sectionURL(sec), URL: u, BrowseURL: b.browseURL("", sec, name),
		Versions: len(p.Listing.Versions), Version: ver.Upstream(),
		SectionLabel: sectionTitle(sec), Series: manpage.IsSeries(sec)}
	if pv.Series && len(ver.Packages) > 0 {
		pv.Published = published(ver.Packages[0].Version)
	}
	for _, pk := range ver.Packages {
		pv.Packages = append(pv.Packages, pk.String())
	}
	for _, src := range sourcesIn(ver) {
		pv.In = append(pv.In, chip{Label: src.Label(), URL: b.browseURL(src.Key(), sec, name)})
	}
	if len(p.Listing.Versions) > 1 {
		pv.VersionLinks = versionLinks(p.Listing, u, func(slug string) string { return b.browseURL("", sec, name, slug) })
	}

	man, err := b.s.Variant(ver.Source, repo.VariantMan)
	if err != nil {
		return err
	}
	modified := ver.Added
	if girlified(ver) {
		girl, err := b.s.Variant(ver.Source, repo.VariantGirl)
		if err != nil {
			return err
		}
		body, toc := render.HTML(girl.Text(), render.HTMLOptions{Link: b.link})
		pv.Body, pv.TOC, pv.Girlified = template.HTML(body), toc, true // render.HTML escapes everything
		if g := girl.Generator; g != nil {
			pv.Model = g.Model
			if pv.Model == "" {
				pv.Model = g.Backend
			}
			if !g.Generated.IsZero() {
				modified = g.Generated
			}
		}
	} else {
		o := render.HTMLOptions{Link: b.link}
		if sec == rfc.Section {
			o.Indent, o.Width, o.TitleBlock = rfc.Indent, rfc.Width, true
		}
		body, toc := render.OriginalHTML(man.Header, man.Footer, originalSections(man), o)
		pv.Body, pv.TOC = template.HTML(body), toc // so does OriginalHTML
	}
	if !modified.IsZero() {
		pv.Modified = modified.UTC().Format(time.DateOnly)
	}
	desc := ver.Description
	if desc == "" {
		desc = "the " + title + " man page"
	}
	desc = name + "(" + sec + "): " + desc + ", rewritten in the voice of a Gen Z girl with every detail kept 💅"
	v := &view{Title: pageTitle(name, sec, ver.Description), Description: desc, Page: pv, OGType: "article"}
	if !modified.IsZero() {
		v.Modified = modified.UTC().Format(time.RFC3339)
	}
	v.JSONLD = b.ldArticle(ver, pv, v, modified)
	return b.render(u, "page", v)
}

// sourcesIn lists the distribution releases (or upstream) a version came
// from.
func sourcesIn(v *repo.Version) []repo.SourceEntry {
	seen := map[string]bool{}
	var out []repo.SourceEntry
	for _, p := range v.Packages {
		e := repo.SourceEntry{Distro: p.Source(), Release: p.Release}
		if e.Distro == "" {
			continue
		}
		if e.Distro == repo.Upstream {
			e.Release = ""
		}
		if !seen[e.Key()] {
			seen[e.Key()] = true
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func pageTitle(name, sec, desc string) string {
	t := name + "(" + sec + ")"
	if desc != "" {
		t += " — " + desc
	}
	return t
}

func (b *builder) search() error {
	return b.render(b.o.Base+"search/", "search", &view{Title: "search", NoIndex: true})
}

func (b *builder) browse() error {
	return b.render(b.o.Base+"browse/", "browse", &view{Title: "browse", NoIndex: true})
}

func (b *builder) sitemap(pages []repo.PageRef) error {
	path := filepath.Join(b.o.Out, "sitemap.xml")
	if b.o.URL == "" {
		// Without an absolute URL there's no sitemap; don't leave an old one.
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	loc := func(u string, mod time.Time) {
		buf.WriteString("<url><loc>")
		template.HTMLEscape(&buf, []byte(b.abs(u)))
		buf.WriteString("</loc>")
		if !mod.IsZero() {
			buf.WriteString("<lastmod>" + mod.UTC().Format(time.DateOnly) + "</lastmod>")
		}
		buf.WriteString("</url>\n")
	}
	loc(b.o.Base, b.m.Generated)
	// Every page of every listing: a section's, and its subsections' if
	// it has more than one.
	count := map[string]int{}
	var keys []string
	subs := map[string][]string{}
	for _, p := range pages {
		key, sec := mainKey(p.Pointer.Section), p.Pointer.Section
		if count[key] == 0 {
			keys = append(keys, key)
		}
		count[key]++
		if count[key+"/"+sec] == 0 {
			subs[key] = append(subs[key], sec)
		}
		count[key+"/"+sec]++
	}
	listing := func(u string, n int) {
		for i := 1; (i-1)*listPage < n; i++ {
			loc(listPageURL(u, i), time.Time{})
		}
	}
	for _, key := range keys {
		listing(b.listURL(key, ""), count[key])
		if len(subs[key]) > 1 {
			for _, sec := range subs[key] {
				listing(b.listURL(key, sec), count[key+"/"+sec])
			}
		}
	}
	for _, p := range pages {
		loc(b.pageURL(p.Pointer.Name, p.Pointer.Section), p.Current().Added)
	}
	buf.WriteString("</urlset>\n")
	return b.write(path, buf.Bytes())
}

// prune removes pages from earlier builds that no longer exist (including
// the per-version, per-original and per-source pages earlier versions of
// girlrepo made).
func (b *builder) prune() error {
	dirs := []string{"man", "section", "src"}
	for _, e := range b.m.Sections {
		if manpage.IsSeries(e.Section) {
			dirs = append(dirs, SegmentName(e.Section))
		}
	}
	// Each directory under those is walked on its own, in parallel.
	var jobs []func() error
	var mu sync.Mutex
	emptied := map[string]bool{} // directories something was removed from
	for _, dir := range dirs {
		root := filepath.Join(b.o.Out, dir)
		ents, err := os.ReadDir(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		emptied[root] = true // tried last, in case it's empty now
		for _, e := range ents {
			sub := filepath.Join(root, e.Name())
			jobs = append(jobs, func() error {
				return filepath.WalkDir(sub, func(path string, d fs.DirEntry, err error) error {
					if err != nil {
						if errors.Is(err, fs.ErrNotExist) {
							return nil
						}
						return err
					}
					b.mu.Lock()
					keep := d.IsDir() || b.written[path] || d.Name() != "index.html"
					if !keep {
						b.st.Removed++
					}
					b.mu.Unlock()
					if keep {
						return nil
					}
					mu.Lock()
					for p := filepath.Dir(path); p != root; p = filepath.Dir(p) {
						emptied[p] = true
					}
					mu.Unlock()
					return os.Remove(path)
				})
			})
		}
	}
	if err := parallel(b.o.Ctx, b.o.Jobs, jobs); err != nil {
		return err
	}
	// Remove whichever of those are empty now, deepest first.
	gone := make([]string, 0, len(emptied))
	for d := range emptied {
		gone = append(gone, d)
	}
	sort.Slice(gone, func(i, j int) bool { return len(gone[i]) > len(gone[j]) })
	for _, d := range gone {
		_ = os.Remove(d) // fails if it isn't empty
	}
	return nil
}
