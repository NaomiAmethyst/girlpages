// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package web renders girlified pages as HTML, either as standalone
// documents (girl --html) or from a small web server (girl --serve).
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/render"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/style.css
var styleCSS string

var templates = map[string]*template.Template{}

var funcs = template.FuncMap{"pages": pages}

func pages(n int) string {
	if n == 1 {
		return "1 page"
	}
	return fmt.Sprintf("%d pages", n)
}

func init() {
	for _, name := range []string{"home", "list", "page", "waiting", "oops", "original"} {
		templates[name] = template.Must(template.New(name).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html"))
	}
}

// view is everything any template might want.
type view struct {
	Title      string
	Version    string
	Standalone bool
	CSS        template.CSS
	Query      string
	Refresh    int

	// home
	Sections         []sectionCard
	Total, Girlified int

	// list
	Heading, Sub string
	Chips        []chip
	Letters      []string
	Groups       []group

	// page, original
	Docs     []docView
	Original string

	Wait *waitView
	Oops *oopsView
}

type sectionCard struct {
	Key, Name, Emoji string
	Count, Girlified int
}

type chip struct {
	Label, URL string
	On         bool
}

type group struct {
	Letter string
	Items  []listItem
}

type listItem struct {
	Name, Section, Desc, URL string
	Girlified                bool
}

type docView struct {
	Title      string
	Name       string
	SectionKey string
	URL        string
	Body       template.HTML
	TOC        []render.Heading
	Model      string
	Generated  string
	Stale      bool
	Crumbs     bool
	Actions    bool
	CanConvert bool
}

type waitView struct {
	Title, Mood, Emoji, Elapsed, URL string
	Part, Parts, Percent             int
}

type oopsView struct {
	Emoji, Heading, Message, Detail string
	Retry, Original                 string
}

func execute(w io.Writer, name string, v *view) error {
	return templates[name].ExecuteTemplate(w, "layout", v)
}

// DocPage is one page for a standalone document.
type DocPage struct {
	Page   manpage.Page
	Title  string
	Markup string
	Entry  *cache.Entry
	Stale  bool
}

// WriteDocument writes pages as one self-contained HTML document, with the
// stylesheet inlined so it works anywhere.
func WriteDocument(w io.Writer, pages []DocPage, version string) error {
	v := &view{Version: version, Standalone: true, CSS: template.CSS(styleCSS)}
	for i, p := range pages {
		if i == 0 {
			v.Title = p.Title
		}
		v.Docs = append(v.Docs, newDocView(p.Page, p.Title, p.Markup, p.Entry, p.Stale, nil))
	}
	return execute(w, "page", v)
}

func newDocView(p manpage.Page, title, markup string, e *cache.Entry, stale bool, link func(name, sec string) string) docView {
	body, toc := render.HTML(markup, render.HTMLOptions{Link: link})
	d := docView{
		Title:      title,
		Name:       p.Name,
		SectionKey: p.Section,
		URL:        pageURL(p.Name, p.Section),
		Body:       template.HTML(body), // render.HTML escapes everything it emits
		TOC:        toc,
		Stale:      stale,
	}
	if e != nil {
		d.Model = e.Model
		if d.Model == "" {
			d.Model = e.Backend
		}
		if !e.Generated.IsZero() {
			d.Generated = e.Generated.Local().Format(time.DateOnly)
		}
	}
	return d
}

func pageURL(name, sec string) string {
	return "/man/" + url.PathEscape(sec) + "/" + url.PathEscape(name)
}
