// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package render

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"
)

// Heading is an entry in a page's table of contents.
type Heading struct {
	ID    string
	Text  string
	Level int // 1 for sections, 2 for subsections
}

// HTMLOptions controls HTML rendering.
type HTMLOptions struct {
	// Link returns the URL for a reference such as ls(1), or "" to leave it
	// as text. Nil means no man page links.
	Link func(name, section string) string
}

// HTML renders markup as an HTML fragment, returning it with the page's
// table of contents. Colour tags become c-<colour> classes; the stylesheet
// picks shades that suit the theme.
func HTML(markup string, o HTMLOptions) (string, []Heading) {
	h := &htmlWriter{o: o, ids: map[string]int{}}
	for _, bl := range parseBlocks(markup) {
		h.block(bl)
	}
	h.closeLists(-1)
	return h.b.String(), h.toc
}

type listFrame struct {
	level   int
	ordered bool
}

type htmlWriter struct {
	o     HTMLOptions
	b     strings.Builder
	lists []listFrame
	toc   []Heading
	ids   map[string]int
}

func (h *htmlWriter) block(bl block) {
	if bl.kind == bBlank {
		return // lists stay open across blank lines
	}
	if bl.kind != bList {
		h.closeLists(-1)
	}
	switch bl.kind {
	case bHeading:
		// The page title is the h1, so markup headings sit one level down.
		tag := "h4"
		switch bl.level {
		case 1:
			tag = "h2"
		case 2:
			tag = "h3"
		}
		text := plainInline(bl.text)
		id := h.id(text)
		if bl.level <= 2 {
			h.toc = append(h.toc, Heading{ID: id, Text: strings.TrimSpace(text), Level: bl.level})
		}
		fmt.Fprintf(&h.b, "<%s id=\"%s\">%s</%s>\n", tag, id, h.inline(bl.text), tag)
	case bPara:
		fmt.Fprintf(&h.b, "<p>%s</p>\n", h.inline(bl.text))
	case bQuote:
		fmt.Fprintf(&h.b, "<blockquote>%s</blockquote>\n", h.inline(bl.text))
	case bRule:
		h.b.WriteString("<hr class=\"sparkle\">\n")
	case bCode:
		h.b.WriteString("<pre><code>")
		h.b.WriteString(h.linkify(strings.Join(bl.lines, "\n")))
		h.b.WriteString("</code></pre>\n")
	case bTable:
		h.b.WriteString("<div class=\"table\"><table>\n")
		for i, row := range bl.lines {
			cell := "td"
			if i == 0 && len(bl.lines) > 1 {
				cell = "th"
			}
			h.b.WriteString("<tr>")
			for _, c := range splitRow(row) {
				fmt.Fprintf(&h.b, "<%s>%s</%s>", cell, h.inline(c), cell)
			}
			h.b.WriteString("</tr>\n")
		}
		h.b.WriteString("</table></div>\n")
	case bList:
		h.listItem(bl)
	}
}

func (h *htmlWriter) listItem(bl block) {
	ordered := bl.marker != ""
	h.closeLists(bl.level)
	top := len(h.lists) - 1
	switch {
	case top >= 0 && h.lists[top].level == bl.level && h.lists[top].ordered == ordered:
		h.b.WriteString("</li>\n")
	default:
		if top >= 0 && h.lists[top].level == bl.level {
			// Same depth, different kind of list.
			h.closeLists(bl.level - 1)
		}
		if ordered {
			h.b.WriteString("<ol>\n")
		} else {
			h.b.WriteString("<ul>\n")
		}
		h.lists = append(h.lists, listFrame{bl.level, ordered})
	}
	if ordered {
		n := strings.TrimRight(bl.marker, ".)")
		fmt.Fprintf(&h.b, "<li value=\"%s\">%s", html.EscapeString(n), h.inline(bl.text))
	} else {
		fmt.Fprintf(&h.b, "<li>%s", h.inline(bl.text))
	}
}

// closeLists closes open lists deeper than level (all of them for -1).
func (h *htmlWriter) closeLists(level int) {
	for len(h.lists) > 0 && h.lists[len(h.lists)-1].level > level {
		f := h.lists[len(h.lists)-1]
		h.lists = h.lists[:len(h.lists)-1]
		if f.ordered {
			h.b.WriteString("</li>\n</ol>\n")
		} else {
			h.b.WriteString("</li>\n</ul>\n")
		}
	}
}

func (h *htmlWriter) id(text string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(text) {
		if r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	id := b.String()
	if id == "" {
		id = "section"
	}
	h.ids[id]++
	if n := h.ids[id]; n > 1 {
		id = fmt.Sprintf("%s-%d", id, n)
	}
	return id
}

func (h *htmlWriter) inline(s string) string {
	var b strings.Builder
	for _, sg := range parseInline(s, style{}) {
		var open, close []string
		add := func(o, c string) {
			open = append(open, o)
			close = append([]string{c}, close...)
		}
		switch {
		case sg.st.color == "rainbow":
			add(`<span class="rainbow">`, "</span>")
		case sg.st.color != "":
			add(`<span class="c-`+sg.st.color+`">`, "</span>")
		}
		if sg.st.bold {
			add("<strong>", "</strong>")
		}
		if sg.st.italic {
			add("<em>", "</em>")
		}
		if sg.st.code {
			add("<code>", "</code>")
		}
		b.WriteString(strings.Join(open, ""))
		b.WriteString(h.linkify(sg.text))
		b.WriteString(strings.Join(close, ""))
	}
	return b.String()
}

var linkRE = regexp.MustCompile("(https?://[^\\s<>()\"'`]*[^\\s<>()\"'`.,;:!?])" +
	`|([A-Za-z_][A-Za-z0-9_.:+-]*)\(([0-9][a-z0-9]*|n|l)\)`)

// linkify escapes text, turning URLs and man page references into links.
func (h *htmlWriter) linkify(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range linkRE.FindAllStringSubmatchIndex(s, -1) {
		var href string
		if m[2] >= 0 {
			href = s[m[2]:m[3]]
		} else if h.o.Link != nil {
			href = h.o.Link(s[m[4]:m[5]], s[m[6]:m[7]])
		}
		if href == "" {
			continue
		}
		b.WriteString(html.EscapeString(s[last:m[0]]))
		fmt.Fprintf(&b, `<a href="%s">%s</a>`, html.EscapeString(href), html.EscapeString(s[m[0]:m[1]]))
		last = m[1]
	}
	b.WriteString(html.EscapeString(s[last:]))
	return b.String()
}

func splitRow(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	cells := strings.Split(row, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}
