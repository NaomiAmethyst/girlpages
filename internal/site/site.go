// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package site builds a static website from a girlpages repository: an
// HTML page for every man page (so search engines can read them), section
// listings, and a search page that queries the repository's index from
// the browser. It only reads the repository, so anyone can build a site
// from a published one.
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

//go:embed static/search.js
var searchJS string

//go:embed static/girlpages.js
var girlpagesJS string

//go:embed static/sw.js
var serviceWorkerJS string

// SearchJS is the browser search module.
func SearchJS() string { return searchJS }

var templates = map[string]*template.Template{}

func init() {
	funcs := template.FuncMap{"pages": func(n int) string {
		if n == 1 {
			return "1 page"
		}
		return fmt.Sprintf("%d pages", n)
	}}
	for _, name := range []string{"home", "list", "page", "original", "search", "notfound", "offline"} {
		templates[name] = template.Must(template.New(name).Funcs(funcs).ParseFS(templateFS,
			"templates/layout.html", "templates/"+name+".html"))
	}
}

// Options controls a build.
type Options struct {
	// Out is the site's root directory. The repository is expected at
	// Out/repo, so the search page can find it.
	Out string
	// Base is the URL path the site is served from, such as "/" or
	// "/girlpages/".
	Base string
	// URL is the site's absolute URL, for canonical links and the
	// sitemap. Optional.
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
	b := &builder{o: o, s: s, written: map[string]bool{}, byName: map[string][]string{}}
	for _, p := range pages {
		b.byName[p.Pointer.Name] = append(b.byName[p.Pointer.Name], p.Pointer.Section)
	}
	steps := []func() error{
		func() error { return b.static() },
		func() error { return b.home(pages, m) },
		func() error { return b.sections(pages) },
		func() error { return b.pages(pages) },
		func() error { return b.search() },
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
	// Search pages load the search script, which brings the rest.
	Search bool

	// home
	Sections         []card
	Total, Girlified int
	Generated        string

	// list
	Heading, Sub string
	Crumb        *crumb
	Chips        []chip
	Letters      []string
	Groups       []group

	// page, original
	Page *pageView
}

type card struct {
	Key, Name, Emoji, URL string
	Count, Girlified      int
}

type crumb struct{ Label, URL string }

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
	SectionURL, URL, OriginalURL      string
	Body                              template.HTML
	TOC                               []render.Heading
	Girlified                         bool
	Model, Generated                  string
	Header, Footer                    string
	Original                          []textSection
	Packages                          []string
	Versions                          []versionLink
	Old                               bool // an older version, not the current one
	CurrentURL                        string
}

type textSection struct{ Heading, Body string }

type versionLink struct {
	Label, URL string
	Current    bool
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

func (b *builder) canonical(u string) string {
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
	if v.Canonical == "" {
		v.Canonical = b.canonical(u)
	}
	var buf bytes.Buffer
	if err := templates[name].ExecuteTemplate(&buf, "layout", v); err != nil {
		return err
	}
	return b.write(b.file(u), buf.Bytes())
}

func (b *builder) static() error {
	icons := appIcons()
	files := map[string][]byte{
		"style.css":    []byte(web.StyleCSS()),
		"search.js":    []byte(searchJS),
		"girlpages.js": []byte(girlpagesJS),
	}
	for name, data := range icons {
		files[name] = data
	}
	// What the service worker keeps for offline use. Changing any of it
	// changes its version, so browsers fetch the new ones.
	shell := []string{"", "search/", "offline/", "manifest.webmanifest"}
	h := sha256.New()
	io.WriteString(h, b.o.Version)
	io.WriteString(h, serviceWorkerJS)
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
	sw := strings.NewReplacer(
		`/*BASE*/"/"`, js(b.o.Base),
		`/*VERSION*/"dev"`, js(hex.EncodeToString(h.Sum(nil))[:16]),
		`/*SHELL*/[]`, js(shell),
	).Replace(serviceWorkerJS)
	if err := b.write(filepath.Join(b.o.Out, "sw.js"), []byte(sw)); err != nil {
		return err
	}

	// GitHub Pages would otherwise hide files starting with _ or .
	if err := b.write(filepath.Join(b.o.Out, ".nojekyll"), nil); err != nil {
		return err
	}
	robots := "User-agent: *\nDisallow: " + b.o.Base + "repo/\n"
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

func (b *builder) home(pages []repo.PageRef, m *repo.Manifest) error {
	cards := map[string]*card{}
	v := &view{Title: "the manual, but make it girly",
		Description: "Every Unix man page, rewritten by an LLM in the voice of a Gen Z girl, with every detail kept. 💅",
		Generated:   m.Generated.Format(time.DateOnly)}
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

func (b *builder) pages(pages []repo.PageRef) error {
	type job struct {
		p   repo.PageRef
		ver repo.Version
	}
	var jobs []job
	for _, p := range pages {
		for _, v := range p.Listing.Versions {
			jobs = append(jobs, job{p, v})
		}
	}
	errs := make([]error, len(jobs))
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(jobs) {
					return
				}
				j := jobs[i]
				err := b.page(j.p, j.ver, j.ver.Source != j.p.Current().Source)
				if errors.Is(err, repo.ErrNotFound) {
					b.mu.Lock()
					fmt.Fprintf(b.o.Log, "💔 %s(%s): %v\n", j.p.Pointer.Name, j.p.Pointer.Section, err)
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

func (b *builder) page(p repo.PageRef, ver repo.Version, old bool) error {
	name, sec := p.Pointer.Name, p.Pointer.Section
	u := b.pageURL(name, sec)
	if old {
		u = b.url("man", sec, name, "v", ver.Source)
	}
	title := ver.Title
	if title == "" {
		title = strings.ToUpper(name) + "(" + sec + ")"
	}
	pv := &pageView{Name: name, Section: sec, Title: title, Description: ver.Description,
		SectionURL: b.sectionURL(sec), URL: u, OriginalURL: u + "original/", Old: old,
		CurrentURL: b.pageURL(name, sec)}
	for _, pk := range ver.Packages {
		pv.Packages = append(pv.Packages, pk.String())
	}
	if len(p.Listing.Versions) > 1 {
		cur := p.Current()
		for _, v := range p.Listing.Versions {
			label := v.Added.Format(time.DateOnly)
			if len(v.Packages) > 0 {
				label = v.Packages[len(v.Packages)-1].String()
			}
			vu := b.url("man", sec, name, "v", v.Source)
			if v.Source == cur.Source {
				vu = b.pageURL(name, sec)
			}
			pv.Versions = append(pv.Versions, versionLink{Label: label, URL: vu, Current: v.Source == ver.Source})
		}
	}

	man, err := b.s.Variant(ver.Source, repo.VariantMan)
	if err != nil {
		return err
	}
	pv.Header, pv.Footer = man.Header, man.Footer
	for _, s := range man.Sections {
		pv.Original = append(pv.Original, textSection{Heading: s.Heading, Body: s.Body})
	}
	desc := ver.Description
	if desc == "" {
		desc = "the " + title + " man page, girlified"
	}
	v := &view{Title: pageTitle(name, sec, ver.Description), Description: desc, Page: pv, NoIndex: old}
	if old {
		v.Canonical = b.canonical(b.pageURL(name, sec))
	}

	if _, ok := ver.Variants[repo.VariantGirl]; ok {
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
				pv.Generated = g.Generated.Format(time.DateOnly)
			}
		}
	}
	if err := b.render(u, "page", v); err != nil {
		return err
	}
	if !pv.Girlified {
		return nil // the page itself is the original
	}
	ov := &view{Title: pageTitle(name, sec, "the original"), Description: desc, Page: pv, NoIndex: true,
		Canonical: b.canonical(b.pageURL(name, sec))}
	return b.render(pv.OriginalURL, "original", ov)
}

func pageTitle(name, sec, desc string) string {
	t := name + "(" + sec + ")"
	if desc != "" {
		t += " — " + desc
	}
	return t
}

func (b *builder) search() error {
	return b.render(b.o.Base+"search/", "search", &view{Title: "search", NoIndex: true, Search: true})
}

func (b *builder) sitemap(pages []repo.PageRef) error {
	if b.o.URL == "" {
		// Without an absolute URL there's no sitemap; don't leave an old one.
		if err := os.Remove(filepath.Join(b.o.Out, "sitemap.xml")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	loc := func(u string) {
		buf.WriteString("<url><loc>")
		template.HTMLEscape(&buf, []byte(b.canonical(u)))
		buf.WriteString("</loc></url>\n")
	}
	loc(b.o.Base)
	seen := map[string]bool{}
	for _, p := range pages {
		if k := mainKey(p.Pointer.Section); !seen[k] {
			seen[k] = true
			loc(b.url("section", k))
		}
	}
	for _, p := range pages {
		loc(b.pageURL(p.Pointer.Name, p.Pointer.Section))
	}
	buf.WriteString("</urlset>\n")
	return b.write(filepath.Join(b.o.Out, "sitemap.xml"), buf.Bytes())
}

// prune removes pages from earlier builds that no longer exist.
func (b *builder) prune() error {
	for _, dir := range []string{"man", "section"} {
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
