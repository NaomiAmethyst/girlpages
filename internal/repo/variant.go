// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"strings"
	"unicode/utf8"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
)

// ManVariant splits man's plain-text output into a man Variant. Text()
// gives back exactly the same text.
func ManVariant(source, text string) *Variant {
	v := &Variant{Format: Format, Variant: VariantMan, Source: source, Syntax: SyntaxText,
		Title: manpage.HeaderTitle(text)}
	lines := splitLines(text)
	if v.Title != "" && len(lines) >= 2 {
		v.Header, v.Footer = lines[0], lines[len(lines)-1]
		lines = lines[1 : len(lines)-1]
	}
	v.Sections = splitSections(lines, func(l string) (string, bool) {
		if l != "" && l[0] != ' ' && l[0] != '\t' {
			return l, true
		}
		return "", false
	})
	return v
}

// GirlVariant splits girlpages markup into a girl Variant, one section per
// top-level heading. Text() gives back exactly the same markup.
func GirlVariant(source, title, markup string, g *Generator) *Variant {
	return MarkupVariant(VariantGirl, source, title, markup, g)
}

// MarkupVariant is GirlVariant for any variant in girlpages markup (boy,
// pirate, witch…).
func MarkupVariant(variant, source, title, markup string, g *Generator) *Variant {
	v := &Variant{Format: Format, Variant: variant, Source: source, Syntax: SyntaxMarkup,
		Title: title, Generator: g}
	fence := false
	v.Sections = splitSections(splitLines(markup), func(l string) (string, bool) {
		if t := strings.TrimLeft(l, " "); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
			return "", false
		}
		if !fence && strings.HasPrefix(l, "# ") {
			return l[2:], true
		}
		return "", false
	})
	return v
}

// Text reassembles the page.
func (v *Variant) Text() string {
	var b strings.Builder
	if v.Header != "" || v.Footer != "" {
		b.WriteString(v.Header)
		b.WriteByte('\n')
	}
	for _, s := range v.Sections {
		if s.Heading != "" {
			if v.Syntax == SyntaxMarkup {
				b.WriteString("# ")
			}
			b.WriteString(s.Heading)
			b.WriteByte('\n')
		}
		b.WriteString(s.Body)
	}
	if v.Header != "" || v.Footer != "" {
		b.WriteString(v.Footer)
		b.WriteByte('\n')
	}
	return b.String()
}

// splitLines splits text into lines, dropping the final newline.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func splitSections(lines []string, heading func(string) (string, bool)) []Section {
	var out []Section
	start := 0 // first body line of the last section
	flush := func(end int) {
		if end > start {
			if len(out) == 0 {
				out = append(out, Section{})
			}
			out[len(out)-1].Body = strings.Join(lines[start:end], "\n") + "\n"
		}
	}
	for i, l := range lines {
		if h, ok := heading(l); ok && h != "" {
			flush(i)
			out = append(out, Section{Heading: h})
			start = i + 1
		}
	}
	flush(len(lines))
	return out
}

// Description pulls the one-line summary out of a man page's NAME section
// ("ls - list directory contents" gives "list directory contents"). Only
// its first paragraph: some pages (npm's) have the whole manual in NAME,
// as subsections.
func Description(v *Variant) string {
	for _, s := range v.Sections {
		h := strings.ToUpper(strings.TrimSpace(s.Heading))
		if h != "NAME" && h != "NOM" && h != "NOMBRE" {
			continue
		}
		var para []string
		for _, l := range strings.Split(s.Body, "\n") {
			if strings.TrimSpace(l) == "" {
				if len(para) > 0 {
					break
				}
				continue
			}
			para = append(para, l)
		}
		body := strings.Join(strings.Fields(strings.Join(para, " ")), " ")
		for _, sep := range []string{" - ", " — ", " -- ", " – ", "\\- "} {
			if _, d, ok := strings.Cut(body, sep); ok {
				return ShortDescription(d)
			}
		}
		return ""
	}
	return ""
}

// MaxDescription is how long a description may be, in bytes: a NAME line
// is meant to fit on one.
const MaxDescription = 200

// ShortDescription trims a description, and cuts one too long (as
// repositories made before Description took only NAME's first paragraph
// have) at a word, with an ellipsis.
func ShortDescription(d string) string {
	d = strings.TrimSpace(d)
	if len(d) <= MaxDescription {
		return d
	}
	cut := strings.LastIndexByte(d[:MaxDescription], ' ')
	if cut <= 0 {
		cut = MaxDescription
		for cut > 0 && !utf8.RuneStart(d[cut]) {
			cut--
		}
	}
	return strings.TrimRight(d[:cut], " ,;:.") + "…"
}
