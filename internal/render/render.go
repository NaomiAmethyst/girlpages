// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package render draws girlpages markup (a small Markdown subset plus colour
// tags) in a terminal, laid out like a man page.
package render

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
)

// ColorMode is how much colour the terminal can take.
type ColorMode int

const (
	NoColor ColorMode = iota
	Color16
	Color256
	TrueColor
)

// DetectColor guesses the terminal's colour support from the environment.
func DetectColor() ColorMode {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return NoColor
	}
	term := os.Getenv("TERM")
	if term == "dumb" {
		return NoColor
	}
	switch strings.ToLower(os.Getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return TrueColor
	}
	if strings.Contains(term, "256color") || strings.Contains(term, "kitty") || strings.Contains(term, "alacritty") ||
		strings.Contains(term, "wezterm") || strings.Contains(term, "ghostty") || strings.Contains(term, "foot") {
		return Color256
	}
	return Color16
}

type color struct {
	r, g, b uint8
	c256    int
	c16     int
}

var palette = map[string]color{
	"pink":     {0xff, 0x7e, 0xc8, 212, 95},
	"hotpink":  {0xff, 0x2e, 0x9a, 198, 95},
	"purple":   {0xb3, 0x88, 0xff, 141, 35},
	"lavender": {0xd7, 0xaf, 0xff, 183, 95},
	"cyan":     {0x5f, 0xf5, 0xf0, 87, 96},
	"mint":     {0x8f, 0xf5, 0xc4, 121, 92},
	"yellow":   {0xff, 0xe0, 0x66, 221, 93},
	"peach":    {0xff, 0xb3, 0x8a, 216, 33},
	"red":      {0xff, 0x5f, 0x87, 204, 91},
	"blue":     {0x87, 0xaf, 0xff, 111, 94},
	"green":    {0x87, 0xff, 0x87, 120, 92},
}

var rainbow = []string{"red", "peach", "yellow", "green", "cyan", "blue", "purple", "pink"}

// Theme colours for structural elements.
const (
	h1Color     = "hotpink"
	h2Color     = "purple"
	h3Color     = "lavender"
	codeColor   = "mint"
	bulletColor = "pink"
	quoteColor  = "pink"
	ruleColor   = "lavender"
)

// StripControls removes control characters other than newlines and tabs,
// so markup from a model or a repository can't drive the terminal with
// escape sequences of its own.
func StripControls(s string) string {
	clean := true
	for _, r := range s {
		if isControl(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, s)
}

func isControl(r rune) bool {
	return r < 0x20 && r != '\n' && r != '\t' || r >= 0x7f && r < 0xa0
}

// Options controls rendering.
type Options struct {
	Width int
	Color ColorMode
	// Title, such as "LS(1)", turns on the man-style header and footer.
	Title string
	// Footer is shown at the bottom left (e.g. "girlpages 1.0").
	Footer string
	// Stale adds a note that the source page has changed since conversion.
	Stale bool
}

const (
	bodyIndent = 7
	h2Indent   = 3
	h3Indent   = 5
	codeIndent = 9
)

type renderer struct {
	o       Options
	b       strings.Builder
	rainbow int
}

// Render lays out markup for the terminal.
func Render(markup string, o Options) string {
	o.Title, o.Footer = StripControls(o.Title), StripControls(o.Footer)
	if o.Width < 20 {
		o.Width = 20
	}
	r := &renderer{o: o}
	if o.Title != "" {
		r.header()
	}
	if o.Stale {
		r.line(bodyIndent, []seg{{"(this page changed since it was girlified ~ run girl --refresh to update 🔄)", style{italic: true, color: "lavender"}}})
		r.b.WriteString("\n")
	}
	blocks := parseBlocks(StripControls(markup))
	// Collapse leading/trailing blanks.
	for len(blocks) > 0 && blocks[0].kind == bBlank {
		blocks = blocks[1:]
	}
	for len(blocks) > 0 && blocks[len(blocks)-1].kind == bBlank {
		blocks = blocks[:len(blocks)-1]
	}
	prev := bBlank
	for i, bl := range blocks {
		if bl.kind == bHeading && bl.level <= 2 && i > 0 && prev != bBlank {
			r.b.WriteString("\n")
		}
		r.block(bl)
		prev = bl.kind
	}
	if o.Title != "" {
		r.footer()
	}
	return r.b.String()
}

func (r *renderer) block(bl block) {
	w := r.o.Width
	switch bl.kind {
	case bBlank:
		r.b.WriteString("\n")
	case bHeading:
		var indent int
		st := style{bold: true}
		switch bl.level {
		case 1:
			indent, st.color = 0, h1Color
		case 2:
			indent, st.color = h2Indent, h2Color
		default:
			indent, st.color = h3Indent, h3Color
		}
		r.wrapped(parseInline(bl.text, st), indent, indent, w)
	case bPara:
		r.wrapped(parseInline(bl.text, style{}), bodyIndent, bodyIndent, w)
	case bQuote:
		bar := []seg{{"┃ ", style{color: quoteColor, bold: true}}}
		lines := wrap(parseInline(bl.text, style{italic: true}), w-bodyIndent-2, w-bodyIndent-2)
		for _, l := range lines {
			r.line(bodyIndent, append(append([]seg{}, bar...), l...))
		}
	case bList:
		indent := bodyIndent + 2*bl.level
		marker := bl.marker
		mst := style{color: bulletColor, bold: true}
		if marker == "" {
			marker = []string{"✦", "✧", "•", "◦"}[min(bl.level, 3)]
			if bl.level == 1 {
				mst.color = "lavender"
			}
		}
		mw := runewidth.StringWidth(marker) + 1
		lines := wrap(parseInline(bl.text, style{}), w-indent-mw, w-indent-mw)
		for j, l := range lines {
			if j == 0 {
				r.line(indent, append([]seg{{marker, mst}, {" ", style{}}}, l...))
			} else {
				r.line(indent+mw, l)
			}
		}
	case bCode:
		for _, l := range bl.lines {
			r.line(codeIndent, []seg{{l, style{code: true}}})
		}
	case bTable:
		for _, l := range bl.lines {
			r.line(bodyIndent, parseInline(l, style{}))
		}
	case bRule:
		deco := "· ✧ · ✦ · ✧ ·"
		pad := (w - runewidth.StringWidth(deco)) / 2
		r.line(max(pad, 0), []seg{{deco, style{color: ruleColor}}})
	}
}

func (r *renderer) wrapped(segs []seg, first, rest, width int) {
	lines := wrap(segs, width-first, width-rest)
	for j, l := range lines {
		if j == 0 {
			r.line(first, l)
		} else {
			r.line(rest, l)
		}
	}
}

func (r *renderer) line(indent int, segs []seg) {
	r.b.WriteString(strings.Repeat(" ", indent))
	for _, s := range coalesce(segs) {
		r.emit(s)
	}
	r.b.WriteString("\n")
}

// coalesce merges neighbouring segments that share a style, including the
// plain spaces wrap puts between words, so a styled phrase is one run.
func coalesce(segs []seg) []seg {
	var out []seg
	for i := 0; i < len(segs); i++ {
		s := segs[i]
		if s.text == " " && s.st == (style{}) && len(out) > 0 && i+1 < len(segs) && segs[i+1].st == out[len(out)-1].st {
			s.st = out[len(out)-1].st
		}
		if len(out) > 0 && out[len(out)-1].st == s.st {
			out[len(out)-1].text += s.text
			continue
		}
		out = append(out, s)
	}
	return out
}

func (r *renderer) emit(s seg) {
	if r.o.Color == NoColor || s.text == "" {
		r.b.WriteString(s.text)
		return
	}
	st := s.st
	if st.code {
		st.color = codeColor
	}
	if st.color == "rainbow" {
		for _, ch := range s.text {
			if ch == ' ' {
				r.b.WriteRune(ch)
				continue
			}
			c := st
			c.color = rainbow[r.rainbow%len(rainbow)]
			r.rainbow++
			r.b.WriteString(r.sgr(c))
			r.b.WriteRune(ch)
			r.b.WriteString("\x1b[0m")
		}
		return
	}
	seq := r.sgr(st)
	if seq == "" {
		r.b.WriteString(s.text)
		return
	}
	r.b.WriteString(seq)
	r.b.WriteString(s.text)
	r.b.WriteString("\x1b[0m")
}

func (r *renderer) sgr(st style) string {
	var codes []string
	if st.bold {
		codes = append(codes, "1")
	}
	if st.italic {
		codes = append(codes, "3")
	}
	if st.kbd {
		// A key: bold, in reverse, like a key cap.
		codes = append(codes, "1", "7")
	}
	if c, ok := palette[st.color]; ok {
		switch r.o.Color {
		case TrueColor:
			codes = append(codes, fmt.Sprintf("38;2;%d;%d;%d", c.r, c.g, c.b))
		case Color256:
			codes = append(codes, fmt.Sprintf("38;5;%d", c.c256))
		default:
			codes = append(codes, fmt.Sprint(c.c16))
		}
	}
	if len(codes) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

func (r *renderer) header() {
	r.spread(
		seg{r.o.Title, style{bold: true, color: h1Color}},
		seg{"✨ girl manual ✨", style{color: "lavender", italic: true}},
		seg{r.o.Title, style{bold: true, color: h1Color}},
	)
	r.b.WriteString("\n")
}

func (r *renderer) footer() {
	r.b.WriteString("\n")
	r.spread(
		seg{r.o.Footer, style{color: "lavender"}},
		seg{"💖", style{}},
		seg{r.o.Title, style{bold: true, color: h1Color}},
	)
}

// spread lays out left, centre and right pieces across the width, like a
// man page header, dropping the centre if it doesn't fit.
func (r *renderer) spread(left, centre, right seg) {
	w := r.o.Width
	lw, cw, rw := runewidth.StringWidth(left.text), runewidth.StringWidth(centre.text), runewidth.StringWidth(right.text)
	if lw+cw+rw+4 > w {
		r.line(0, []seg{left})
		return
	}
	cpos := (w - cw) / 2
	r.emit(left)
	r.b.WriteString(strings.Repeat(" ", max(cpos-lw, 1)))
	r.emit(centre)
	r.b.WriteString(strings.Repeat(" ", max(w-cpos-cw-rw, 1)))
	r.emit(right)
	r.b.WriteString("\n")
}

// wrap fills styled words into lines of at most firstWidth (then width)
// display columns. Words longer than a line are left to overflow.
func wrap(segs []seg, firstWidth, width int) [][]seg {
	type word struct {
		segs []seg
		w    int
	}
	var words []word
	var cur []seg
	var curText strings.Builder
	endWord := func() {
		if len(cur) > 0 {
			words = append(words, word{cur, runewidth.StringWidth(curText.String())})
			cur = nil
			curText.Reset()
		}
	}
	for _, s := range segs {
		start := 0
		text := s.text
		for i, ch := range text {
			if ch == ' ' || ch == '\n' || ch == '\t' {
				if i > start {
					cur = append(cur, seg{text[start:i], s.st})
					curText.WriteString(text[start:i])
				}
				endWord()
				start = i + 1
			}
		}
		if start < len(text) {
			cur = append(cur, seg{text[start:], s.st})
			curText.WriteString(text[start:])
		}
	}
	endWord()

	var lines [][]seg
	var line []seg
	lw := 0
	limit := firstWidth
	for _, wd := range words {
		if lw > 0 && lw+1+wd.w > limit {
			lines = append(lines, line)
			line, lw, limit = nil, 0, width
		}
		if lw > 0 {
			line = append(line, seg{" ", style{}})
			lw++
		}
		line = append(line, wd.segs...)
		lw += wd.w
	}
	if len(line) > 0 || len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
}

type blockKind int

const (
	bBlank blockKind = iota
	bPara
	bHeading
	bList
	bCode
	bQuote
	bRule
	bTable
)

type block struct {
	kind   blockKind
	level  int
	marker string
	text   string
	lines  []string
}

var (
	headingRE = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	ruleRE    = regexp.MustCompile(`^\s{0,3}([-*_])(\s*[-*_]){2,}\s*$`)
	listRE    = regexp.MustCompile(`^(\s*)([-*+•]|\d{1,3}[.)])\s+(.*)$`)
	fenceRE   = regexp.MustCompile("^\\s*(```+|~~~+)")
	tableSep  = regexp.MustCompile(`^\s*\|?[\s:|-]+\|?\s*$`)
)

func startsBlock(l string) bool {
	t := strings.TrimSpace(l)
	return t == "" || headingRE.MatchString(l) || ruleRE.MatchString(l) || listRE.MatchString(l) ||
		fenceRE.MatchString(l) || strings.HasPrefix(t, ">") || strings.HasPrefix(t, "|")
}

func indentWidth(s string) int {
	n := 0
	for _, c := range s {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

func parseBlocks(markup string) []block {
	markup = strings.ReplaceAll(markup, "\r\n", "\n")
	lines := strings.Split(markup, "\n")
	var out []block
	for i := 0; i < len(lines); {
		l := lines[i]
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			if len(out) == 0 || out[len(out)-1].kind != bBlank {
				out = append(out, block{kind: bBlank})
			}
			i++

		case fenceRE.MatchString(l):
			m := fenceRE.FindStringSubmatch(l)
			fence := m[1]
			ind := indentWidth(l)
			i++
			var code []string
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), fence[:3]) {
				cl := strings.ReplaceAll(lines[i], "\t", "    ")
				// Drop the fence's own indentation from each line.
				if k := min(ind, indentWidth(cl)); k > 0 {
					cl = cl[k:]
				}
				code = append(code, strings.TrimRight(cl, " "))
				i++
			}
			i++ // closing fence
			out = append(out, block{kind: bCode, lines: code})

		case headingRE.MatchString(l):
			m := headingRE.FindStringSubmatch(l)
			out = append(out, block{kind: bHeading, level: len(m[1]), text: m[2]})
			i++

		case ruleRE.MatchString(l):
			out = append(out, block{kind: bRule})
			i++

		case strings.HasPrefix(t, ">"):
			var parts []string
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">") {
				q := strings.TrimLeft(strings.TrimSpace(lines[i]), ">")
				parts = append(parts, strings.TrimSpace(q))
				i++
			}
			out = append(out, block{kind: bQuote, text: strings.Join(parts, " ")})

		case strings.HasPrefix(t, "|"):
			var rows []string
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|") {
				if !tableSep.MatchString(lines[i]) {
					rows = append(rows, strings.TrimSpace(lines[i]))
				}
				i++
			}
			out = append(out, block{kind: bTable, lines: rows})

		case listRE.MatchString(l):
			m := listRE.FindStringSubmatch(l)
			ind := indentWidth(m[1])
			marker := ""
			if m[2][0] >= '0' && m[2][0] <= '9' {
				marker = m[2]
			}
			text := []string{m[3]}
			i++
			for i < len(lines) && !startsBlock(lines[i]) {
				text = append(text, strings.TrimSpace(lines[i]))
				i++
			}
			out = append(out, block{kind: bList, level: min(ind/2, 6), marker: marker, text: strings.Join(text, " ")})

		default:
			text := []string{t}
			i++
			for i < len(lines) && !startsBlock(lines[i]) {
				text = append(text, strings.TrimSpace(lines[i]))
				i++
			}
			out = append(out, block{kind: bPara, text: strings.Join(text, " ")})
		}
	}
	return out
}
