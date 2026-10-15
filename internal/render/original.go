// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package render

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// OriginalSection is a section of man's plain-text rendering of a page,
// cut at its top-level headings.
type OriginalSection struct {
	Heading, Body string
}

// fillWidth is the last column man fills text to at 80 columns: a line
// of a paragraph ends there or where the next word wouldn't fit.
const fillWidth = 79

// OriginalHTML renders man's plain-text rendering of a page as HTML that
// reflows: filled text becomes paragraphs, tagged paragraphs (options)
// definition lists, bullets lists, and anything laid out by hand
// (synopses, examples, tables) code blocks, as in a girly page. Header and
// footer are man's first and last lines. It has to guess from the layout,
// so it errs towards code blocks, which lose nothing.
func OriginalHTML(header, footer string, sections []OriginalSection, o HTMLOptions) (string, []Heading) {
	h := &htmlWriter{o: o, ids: map[string]int{}}
	if f := headFields(header); len(f) > 0 {
		fmt.Fprintf(&h.b, "<p class=\"manhead\">%s</p>\n", html.EscapeString(strings.Join(f, " · ")))
	}
	for i, s := range sections {
		heading := strings.TrimSpace(StripControls(s.Heading))
		lines := origLines(StripControls(s.Body))
		if o.Indent > 0 && heading != "" {
			lines = dropUnderline(lines)
		}
		if heading != "" {
			id := h.id(heading)
			h.toc = append(h.toc, Heading{ID: id, Text: heading, Level: 1})
			fmt.Fprintf(&h.b, "<h2 id=\"%s\">%s</h2>\n", id, html.EscapeString(heading))
		}
		var nodes []onode
		if strings.EqualFold(heading, "SYNOPSIS") || (o.TitleBlock && heading == "" && i == 0) {
			// Synopses are laid out with care, even when they look filled.
			nodes = mergeCode(codeNodes(lines))
		} else {
			p := &oparser{tagInds: tagIndents(lines), width: fillWidth}
			if o.Width > 0 {
				p.width = o.Width
			}
			base := baseIndent(lines)
			if o.Indent > 0 {
				base, p.plain = o.Indent, true
			}
			nodes = p.parse(lines, base, true, false)
		}
		h.onodes(nodes)
	}
	if f := splitFields(footer); len(f) > 0 {
		fmt.Fprintf(&h.b, "<p class=\"manhead\">%s</p>\n", html.EscapeString(strings.Join(f, " · ")))
	}
	return h.b.String(), h.toc
}

var fieldSep = regexp.MustCompile(`\s{2,}`)

func splitFields(s string) []string {
	s = strings.TrimSpace(StripControls(s))
	if s == "" {
		return nil
	}
	return fieldSep.Split(s, -1)
}

// headFields is the header without the page's title at each end.
func headFields(s string) []string {
	f := splitFields(s)
	if len(f) >= 2 && f[0] == f[len(f)-1] {
		f = f[1 : len(f)-1]
	}
	return f
}

// An oline is a line of the original: its indentation, and the rest.
type oline struct {
	ind  int
	text string // "" for a blank line
}

func origLines(body string) []oline {
	body = strings.TrimSuffix(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if body == "" {
		return nil
	}
	var out []oline
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimRight(strings.ReplaceAll(l, "\t", "        "), " ")
		t := strings.TrimLeft(l, " ")
		out = append(out, oline{len(l) - len(t), t})
	}
	return out
}

// baseIndent is where a section's text starts: the least indentation of
// at least 4 (subsection headings stick out further left).
func baseIndent(lines []oline) int {
	base, least := -1, -1
	for _, l := range lines {
		if l.text == "" {
			continue
		}
		if least < 0 || l.ind < least {
			least = l.ind
		}
		if l.ind >= 4 && (base < 0 || l.ind < base) {
			base = l.ind
		}
	}
	if base < 0 {
		return max(least, 0)
	}
	return base
}

// tagIndents are the columns that tagged paragraphs' descriptions start
// at in a section, to recognise tags on the same line as theirs.
func tagIndents(lines []oline) map[int]bool {
	out := map[int]bool{}
	for i := 0; i+1 < len(lines); i++ {
		if lines[i].text != "" && lines[i+1].text != "" && lines[i+1].ind > lines[i].ind {
			out[lines[i+1].ind] = true
		}
	}
	return out
}

type onodeKind int

const (
	oPara onodeKind = iota
	oCode
	oSub
	oDL
	oUL
	oOL
)

type onode struct {
	kind onodeKind
	// text is a paragraph's, with newlines where man's lines were broken
	// by hand.
	text  string
	gap   bool    // a blank line came before it
	lines []oline // for code
	items []oitem // for lists
}

type oitem struct {
	tag  string
	body []onode
}

type oparser struct {
	tagInds map[int]bool
	width   int // the last column text is filled to
	// plain is set for documents that are plain text (RFCs), not man's
	// rendering of a page, where a line left of the text is only a
	// heading if it looks like one (see subHeading): in man's, it's
	// always one (.SS), whatever it says.
	plain bool
}

// parse turns lines indented at least ind into nodes. At the top of a
// section, lines left of ind are subsection headings; indented is for
// blocks indented without a tag, where a lone line is more likely an
// example than a sentence.
func (p *oparser) parse(lines []oline, ind int, top, indented bool) []onode {
	var out []onode
	add := func(i int, n onode) {
		n.gap = i > 0 && lines[i-1].text == ""
		out = append(out, n)
	}
	for i := 0; i < len(lines); {
		l := lines[i]
		if l.text == "" {
			i++
			continue
		}
		heading := func(k int) bool { return top && lines[k].ind < ind && (!p.plain || subHeading(lines, k)) }
		if p.plain && !heading(i) {
			// A drawing (a diagram, a table) is code, whatever its lines
			// look like on their own: in plain text, it's drawn between
			// blank lines (or headings).
			j := i + 1
			for j < len(lines) && lines[j].text != "" && !heading(j) {
				j++
			}
			if drawn(lines[i:j]) || top && l.ind < ind {
				// Anything left of the text that isn't a heading is laid
				// out by hand too: a diagram's edge, a grammar at the
				// margin.
				add(i, onode{kind: oCode, lines: lines[i:j]})
				i = j
				continue
			}
		}
		if heading(i) {
			add(i, onode{kind: oSub, text: l.text})
			i++
			if p.plain && i < len(lines) && underline.MatchString(lines[i].text) {
				i++
			}
			continue
		}
		if tag, desc, col, ok := p.tagAt(lines, i); ok {
			end := regionEnd(lines, i+1, col)
			sub := lines[i+1 : end]
			if desc != "" {
				sub = append([]oline{{col, desc}}, sub...)
			}
			kind := oDL
			switch {
			case isBullet(tag):
				kind = oUL
			case orderedTag.MatchString(tag):
				kind = oOL
			}
			item := oitem{tag: tag, body: p.parse(sub, col, false, false)}
			if n := len(out); n > 0 && out[n-1].kind == kind {
				out[n-1].items = append(out[n-1].items, item)
			} else {
				add(i, onode{kind: kind, items: []oitem{item}})
			}
			i = end
			continue
		}
		if l.ind > ind {
			end := regionEnd(lines, i, l.ind)
			sub := p.parse(lines[i:end], l.ind, false, true)
			if len(sub) > 0 {
				sub[0].gap = i > 0 && lines[i-1].text == ""
			}
			out = append(out, sub...)
			i = end
			continue
		}
		j := i + 1
		for j < len(lines) && lines[j].text != "" && lines[j].ind == ind {
			if _, _, _, ok := p.tagAt(lines, j); ok {
				break
			}
			j++
		}
		switch run := lines[i:j]; {
		case indented && len(run) == 1 && !strings.HasSuffix(run[0].text, "."):
			add(i, onode{kind: oCode, lines: run})
		case isProse(run, p.width):
			add(i, onode{kind: oPara, text: joinProse(run)})
		case isText(run):
			var ts []string
			for _, l := range run {
				ts = append(ts, l.text)
			}
			add(i, onode{kind: oPara, text: strings.Join(ts, "\n")})
		default:
			add(i, onode{kind: oCode, lines: run})
		}
		i = j
	}
	return mergeCode(out)
}

var orderedTag = regexp.MustCompile(`^\(?[0-9]{1,3}[.)]$`)

// tocLine matches a line of a table of contents, with dot leaders to its
// page number ("6. REPLIES ........ 43").
var tocLine = regexp.MustCompile(`\.(\s?\.){3,}\s*[0-9]+\s*$`)

// subHeading reports whether line i of a plain-text document, left of its
// section's text, is a subsection heading: after a blank line (or, if
// it's numbered, before one: where a page break was taken out, it can
// follow anything), with nothing more at its indent right after it, and a
// title (after its number, if it has one: "2.2   Operations",
// "4.6.2.10 ID_DER_ASN1_DN"; else text, see textLike), not code (a
// diagram, a grammar) or a line of a table of contents.
func subHeading(lines []oline, i int) bool {
	l := lines[i]
	// Right under it can be its underline, or a heading of its own (4.3.
	// above 4.3.1.).
	if n := i + 1; n < len(lines) && lines[n].text != "" && lines[n].ind <= l.ind &&
		!underline.MatchString(lines[n].text) && !numberedSub.MatchString(lines[n].text) {
		return false
	}
	t := l.text
	m := numberedSub.FindStringSubmatch(t)
	if m != nil {
		t = m[1]
	}
	after := i == 0 || lines[i-1].text == ""
	before := i+1 == len(lines) || lines[i+1].text == "" || underline.MatchString(lines[i+1].text) ||
		numberedSub.MatchString(lines[i+1].text)
	if !after && !(m != nil && before) {
		return false
	}
	if utf8.RuneCountInString(t) >= 72 || tocLine.MatchString(t) {
		return false
	}
	// Columns are for tables and diagrams, but a numbered heading can have
	// a note at the end ("3.2  Alternatives   Rule1 / Rule2"), as long as
	// it isn't a page number (a table of contents').
	if title, rest, ok := strings.Cut(t, "   "); ok {
		if m == nil || pageNumber.MatchString(strings.TrimSpace(rest)) || strings.TrimSpace(title) == "" {
			return false
		}
	}
	return m != nil || textLike(t)
}

// pageNumber matches a page number, in a table of contents.
var pageNumber = regexp.MustCompile(`^([0-9]+|[ivxlc]+)$`)

// dropUnderline leaves out the underline of a plain-text document's
// heading, at the start of its section.
func dropUnderline(lines []oline) []oline {
	for i, l := range lines {
		if l.text == "" {
			continue
		}
		if underline.MatchString(l.text) {
			return lines[i+1:]
		}
		break
	}
	return lines
}

// drawn reports whether a block of lines (more than one) is a drawing or a
// table: half of them or more are laid out in columns, or made of symbols.
func drawn(block []oline) bool {
	if len(block) < 2 {
		return false
	}
	n := 0
	for _, l := range block {
		if strings.Contains(l.text, "   ") || codeLike(l.text) ||
			!strings.ContainsFunc(l.text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			n++
		}
	}
	return n*2 >= len(block)
}

// numberedSub matches a numbered subsection's heading: its number, then
// its title (m[1]).
var numberedSub = regexp.MustCompile(`^(?:[0-9]+|[A-Z])(?:\.[0-9]+)+\.?\s+(\S.*)$`)

// underline matches a line underlining a heading.
var underline = regexp.MustCompile(`^(-{3,}|={3,}|~{3,})$`)

func isBullet(tag string) bool {
	switch tag {
	case "•", "·", "◦", "‣", "∙", "*", "-", "+", "o":
		return true
	}
	return false
}

// tagAt reports whether line i starts a tagged paragraph: a tag, then a
// description indented further, starting on the next line or later on the
// tag's own (desc, starting at column col).
func (p *oparser) tagAt(lines []oline, i int) (tag, desc string, col int, ok bool) {
	l := lines[i]
	if tocLine.MatchString(l.text) {
		return "", "", 0, false
	}
	rs := []rune(l.text)
	if i+1 < len(lines) && lines[i+1].text != "" && lines[i+1].ind > l.ind {
		col = lines[i+1].ind
		c := col - l.ind
		if c >= 2 && len(rs) > c && rs[c] != ' ' && rs[c-1] == ' ' && strings.TrimSpace(string(rs[:c])) != "" {
			return strings.TrimSpace(string(rs[:c])), string(rs[c:]), col, true
		}
		// A long line is more likely a paragraph followed by something
		// indented than a tag.
		if len(rs) <= 60 && tagLike(l.text) {
			return l.text, "", col, true
		}
		return "", "", 0, false
	}
	// A tag that fits before its description shares its line.
	if i+1 < len(lines) && lines[i+1].text != "" && lines[i+1].ind == l.ind {
		return "", "", 0, false
	}
	if len(rs) >= 3 && isBullet(string(rs[0])) && rs[1] == ' ' {
		c := 1
		for c < len(rs) && rs[c] == ' ' {
			c++
		}
		if c < len(rs) {
			return string(rs[0]), string(rs[c:]), l.ind + c, true
		}
	}
	// The tag ends at the first space; an option's tag may be followed
	// by just one, if it only just fits.
	for c := 1; c+1 < len(rs) && c <= 24; c++ {
		if rs[c] != ' ' {
			continue
		}
		e := c
		for e < len(rs) && rs[e] == ' ' {
			e++
		}
		if e < len(rs) && (e == 7 || p.tagInds[l.ind+e]) && (e-c >= 2 || rs[0] == '-') && tagLike(string(rs[:c])) {
			return string(rs[:c]), string(rs[e:]), l.ind + e, true
		}
		break
	}
	return "", "", 0, false
}

// tagLike reports whether text could be a tag: it has a letter or digit,
// and doesn't end like a line of code opening a block or going on.
func tagLike(t string) bool {
	if !strings.ContainsFunc(t, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return false
	}
	return !strings.ContainsAny(t[len(t)-1:], "{[(,;\\=")
}

// isWord reports whether s is all lower-case letters.
func isWord(s string) bool {
	for _, r := range s {
		if !unicode.IsLower(r) {
			return false
		}
	}
	return s != ""
}

// regionEnd is where the lines from start indented at least ind end,
// leaving out blank lines at the end.
func regionEnd(lines []oline, start, ind int) int {
	end := start
	for k := start; k < len(lines); k++ {
		if lines[k].text == "" {
			continue
		}
		if lines[k].ind < ind {
			break
		}
		end = k + 1
	}
	return end
}

// isProse reports whether a run of lines is text man filled: no columns
// lined up with runs of spaces, and every line but the last ending where
// the next word wouldn't have fit.
func isProse(run []oline, width int) bool {
	for k, l := range run {
		if strings.Contains(l.text, "   ") {
			return false
		}
		if k+1 < len(run) {
			// Lines of code can be long too.
			if strings.ContainsAny(l.text[len(l.text)-1:], "{};\\") {
				return false
			}
			next, _, _ := strings.Cut(run[k+1].text, " ")
			if l.ind+utf8.RuneCountInString(l.text)+1+utf8.RuneCountInString(next) <= width {
				return false
			}
		}
	}
	if len(run) == 1 {
		return textLike(run[0].text)
	}
	return true
}

// isText reports whether lines man didn't fill are still text, broken by
// hand: each is words, not code.
func isText(run []oline) bool {
	for k, l := range run {
		// (The last line may be short: a paragraph's often ends so.)
		if strings.Contains(l.text, "   ") || !textLike(l.text) || (k < len(run)-1 && len(strings.Fields(l.text)) < 3) {
			return false
		}
	}
	return true
}

// textLike reports whether a line reads as text: it starts with a letter
// (or a quote or bracket, or an option followed by words), and is mostly
// letters, spaces and ordinary punctuation.
func textLike(t string) bool {
	r, _ := utf8.DecodeRuneInString(t)
	if !unicode.IsLetter(r) && !strings.ContainsRune("\"'([", r) {
		f := strings.Fields(t)
		if r != '-' || len(f) < 3 || !isWord(f[1]) {
			return false
		}
	}
	return !codeLike(t)
}

// codeLike reports whether more than a tenth of a line is the symbols code
// is made of.
func codeLike(t string) bool {
	n, odd := 0, 0
	for _, r := range t {
		n++
		if strings.ContainsRune("{}[]<>|=$#;/\\_*&~^`@%", r) {
			odd++
		}
	}
	return odd*10 > n
}

// joinProse joins filled lines into one, without a space after a line
// broken at a hyphen inside a word.
func joinProse(run []oline) string {
	var b strings.Builder
	for k, l := range run {
		if k > 0 {
			prev := run[k-1].text
			pr, _ := utf8.DecodeLastRuneInString(strings.TrimSuffix(prev, "-"))
			nr, _ := utf8.DecodeRuneInString(l.text)
			if !(strings.HasSuffix(prev, "-") && len(prev) >= 2 && unicode.IsLetter(pr) && unicode.IsLetter(nr)) {
				b.WriteByte(' ')
			}
		}
		b.WriteString(l.text)
	}
	return b.String()
}

func codeNodes(lines []oline) []onode {
	var out []onode
	for i := 0; i < len(lines); {
		if lines[i].text == "" {
			i++
			continue
		}
		j := i
		for j < len(lines) && lines[j].text != "" {
			j++
		}
		out = append(out, onode{kind: oCode, lines: lines[i:j], gap: i > 0})
		i = j
	}
	return out
}

// mergeCode joins code blocks that follow each other, with a blank line
// between, so an example indented in steps stays one block.
func mergeCode(nodes []onode) []onode {
	var out []onode
	for _, n := range nodes {
		if k := len(out); k > 0 && n.kind == oCode && out[k-1].kind == oCode {
			ls := append([]oline(nil), out[k-1].lines...)
			if n.gap {
				ls = append(ls, oline{})
			}
			out[k-1].lines = append(ls, n.lines...)
			continue
		}
		out = append(out, n)
	}
	return out
}

func (h *htmlWriter) onodes(nodes []onode) {
	for _, n := range nodes {
		switch n.kind {
		case oPara:
			fmt.Fprintf(&h.b, "<p>%s</p>\n", strings.ReplaceAll(h.linkify(n.text), "\n", "<br>\n"))
		case oSub:
			id := h.id(n.text)
			h.toc = append(h.toc, Heading{ID: id, Text: n.text, Level: 2})
			fmt.Fprintf(&h.b, "<h3 id=\"%s\">%s</h3>\n", id, html.EscapeString(n.text))
		case oCode:
			least := -1
			for _, l := range n.lines {
				if l.text != "" && (least < 0 || l.ind < least) {
					least = l.ind
				}
			}
			var b strings.Builder
			for k, l := range n.lines {
				if k > 0 {
					b.WriteByte('\n')
				}
				if l.text != "" {
					b.WriteString(strings.Repeat(" ", l.ind-least))
					b.WriteString(l.text)
				}
			}
			fmt.Fprintf(&h.b, "<pre><code>%s</code></pre>\n", h.linkify(b.String()))
		case oDL:
			h.b.WriteString("<dl>\n")
			for _, it := range n.items {
				fmt.Fprintf(&h.b, "<dt>%s</dt>\n<dd>\n", h.linkify(it.tag))
				h.onodes(it.body)
				h.b.WriteString("</dd>\n")
			}
			h.b.WriteString("</dl>\n")
		case oUL, oOL:
			tag := "ul"
			if n.kind == oOL {
				tag = "ol"
			}
			fmt.Fprintf(&h.b, "<%s>\n", tag)
			for _, it := range n.items {
				if n.kind == oOL {
					fmt.Fprintf(&h.b, "<li value=\"%s\">\n", html.EscapeString(strings.Trim(it.tag, "().")))
				} else {
					h.b.WriteString("<li>\n")
				}
				h.onodes(it.body)
				h.b.WriteString("</li>\n")
			}
			fmt.Fprintf(&h.b, "</%s>\n", tag)
		}
	}
}
