// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package site builds a static website from a girlpages repository.
//
// Only what search engines should see is static: the home page, the
// section listings, and the latest version (by version number) of every
// page, girlified if it has been, with canonical links, OpenGraph and
// JSON-LD. Everything else (originals, older versions, browsing by
// distribution or release, and search) is done in the browser by
// /browse/ and /search/, straight from the repository. The site only reads
// the repository, so anyone can build one from a published repository.
package site

import (
	"bytes"
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
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/NaomiAmethyst/girlpages/internal/render"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
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
	Sources []chip

	// list
	Heading, Sub string
	Chips        []chip
	// SaveSection, on a section's listing, is the section a button saves
	// for offline reading (in browsers that can).
	SaveSection string
	Letters     []string
	Groups      []group

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

type group struct {
	Letter string
	Items  []item
}

type item struct {
	Name, Section, Desc, URL string
	Girlified                bool
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
	In       []chip
	Versions int
	Version  string // upstream version number
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

func (b *builder) pageURL(name, sec string) string { return b.url("man", sec, name) }

// sectionURL is the listing a page's section is on: section/3/ for 3, and
// section/3/3ssl/ for 3ssl.
func (b *builder) sectionURL(sec string) string {
	if key := mainKey(sec); key != sec {
		return b.url("section", key, sec)
	}
	return b.url("section", sec)
}

// browseURL is a view in the browser app, from name, value pairs.
func (b *builder) browseURL(params ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(params); i += 2 {
		if params[i+1] != "" {
			q.Set(params[i], params[i+1])
		}
	}
	return b.o.Base + "browse/?" + q.Encode()
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
	return repo.WriteFile(path, data)
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

func mainKey(sec string) string { return sec[:1] }

func girlified(v *repo.Version) bool {
	_, ok := v.Variants[repo.VariantGirl]
	return ok
}

// sourceChips lead to browsing everything, or a section, by source.
func (b *builder) sourceChips(section string) []chip {
	if len(b.m.Sources) == 0 {
		return nil
	}
	cs := []chip{{Label: "✨ latest of everything", URL: b.url(), On: true}}
	if section != "" {
		cs[0].URL = b.url("section", section)
	}
	for _, src := range b.m.Sources {
		if section != "" && src.Sections[section] == 0 {
			continue
		}
		label := src.Distro
		if src.Release != "" {
			label = "· " + src.Release
		}
		cs = append(cs, chip{Label: label, URL: b.browseURL("src", src.Key(), "section", section)})
	}
	return cs
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
			c = &card{Key: key, Name: name, Emoji: emoji, URL: b.url("section", key)}
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
			cs := []chip{{Label: "all", URL: b.url("section", key), On: on == ""}}
			for _, s := range secs {
				cs = append(cs, chip{Label: fmt.Sprintf("%s (%d)", s, subs[key][s]), URL: b.url("section", key, s), On: on == s})
			}
			return cs
		}
		list := func(sec string, ps []repo.PageRef) error {
			label, u := key, b.url("section", key)
			if sec != "" {
				label, u = sec, b.url("section", key, sec)
			}
			v := &view{
				Title:       "section " + label + " · " + strings.ToLower(name),
				Description: fmt.Sprintf("%s: every page in section %s of the manual, girlified.", name, label),
				Heading:     "section " + label + " · " + name + " " + emoji,
				Sub:         pluralPages(len(ps)),
				Chips:       chipsFor(sec),
				Sources:     b.sourceChips(key),
				SaveSection: label,
			}
			var items []item
			for _, p := range ps {
				cur := p.Current()
				items = append(items, item{Name: p.Pointer.Name, Section: p.Pointer.Section, Desc: cur.Description,
					URL: b.pageURL(p.Pointer.Name, p.Pointer.Section), Girlified: p.Girlified()})
			}
			sort.SliceStable(items, func(i, j int) bool {
				a, c := strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)
				if a != c {
					return a < c
				}
				return items[i].Section < items[j].Section
			})
			v.Groups, v.Letters = groupByLetter(items)
			v.JSONLD = b.ldCollection(v, u, items)
			return b.render(u, "list", v)
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
	return nil
}

func pluralPages(n int) string {
	if n == 1 {
		return "1 page"
	}
	return fmt.Sprintf("%d pages", n)
}

func groupByLetter(items []item) ([]group, []string) {
	var groups []group
	var letters []string
	for _, it := range items {
		l := "#"
		if r := []rune(it.Name); len(r) > 0 && unicode.IsLetter(r[0]) && r[0] < 0x80 {
			l = strings.ToUpper(string(r[0]))
		}
		if len(groups) == 0 || groups[len(groups)-1].Letter != l {
			groups = append(groups, group{Letter: l})
			letters = append(letters, l)
		}
		groups[len(groups)-1].Items = append(groups[len(groups)-1].Items, it)
	}
	return groups, letters
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
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(pages) {
					return
				}
				err := b.page(pages[i])
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
		SectionURL: b.sectionURL(sec), URL: u, BrowseURL: b.browseURL("page", name, "sec", sec),
		Versions: len(p.Listing.Versions), Version: ver.Upstream()}
	for _, pk := range ver.Packages {
		pv.Packages = append(pv.Packages, pk.String())
	}
	for _, src := range sourcesIn(ver) {
		pv.In = append(pv.In, chip{Label: src.Label(), URL: b.browseURL("page", name, "sec", sec, "src", src.Key())})
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
		body, toc := render.OriginalHTML(man.Header, man.Footer, originalSections(man), render.HTMLOptions{Link: b.link})
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
	seen := map[string]bool{}
	for _, p := range pages {
		for _, u := range []string{b.url("section", mainKey(p.Pointer.Section)), b.sectionURL(p.Pointer.Section)} {
			if !seen[u] {
				seen[u] = true
				loc(u, time.Time{})
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
	for _, dir := range []string{"man", "section", "src"} {
		root := filepath.Join(b.o.Out, dir)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() || b.written[path] || d.Name() != "index.html" {
				return nil
			}
			b.st.Removed++
			return os.Remove(path)
		})
		if err != nil {
			return err
		}
		removeEmptyDirs(root)
		_ = os.Remove(root) // if it's empty now
	}
	return nil
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
		_ = os.Remove(dirs[i])
	}
}
