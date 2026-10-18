// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package render

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type style struct {
	bold, italic, code, kbd bool
	color                   string // palette name, "rainbow", or ""
}

type seg struct {
	text string
	st   style
}

// parseInline turns one paragraph of markup into styled segments: `code`,
// **bold**, *italic*, <colour>…</colour>, <bold>…</bold>,
// <italic>…</italic>, <kbd>…</kbd> and backslash escapes.
func parseInline(s string, base style) []seg {
	var out []seg
	var buf strings.Builder
	cur := base
	var colors []string
	bold, italic := false, false
	// How deep in each of the tags it is.
	tags := map[string]int{}

	apply := func() style {
		st := base
		st.bold = base.bold || bold || tags["bold"] > 0
		st.italic = base.italic || italic || tags["italic"] > 0
		st.kbd = base.kbd || tags["kbd"] > 0
		if len(colors) > 0 {
			st.color = colors[len(colors)-1]
		}
		return st
	}
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, seg{buf.String(), cur})
			buf.Reset()
		}
	}
	restyle := func() {
		flush()
		cur = apply()
	}

	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]):
			buf.WriteByte(s[i+1])
			i += 2
			continue

		case c == '`':
			n := runLen(s, i, '`')
			fence := strings.Repeat("`", n)
			if end := findRun(s, i+n, '`', n); end >= 0 {
				code := s[i+n : end]
				if len(code) >= 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.TrimSpace(code) != "" {
					code = code[1 : len(code)-1]
				}
				flush()
				st := apply()
				st.code = true
				out = append(out, seg{code, st})
				i = end + n
				continue
			}
			buf.WriteString(fence)
			i += n
			continue

		case c == '*' && strings.HasPrefix(s[i:], "**"):
			if bold && i > 0 && !isSpaceByte(s[i-1]) {
				bold = false
				restyle()
				i += 2
				continue
			}
			if !bold && i+2 < len(s) && !isSpaceByte(s[i+2]) && closes(s, i+2, "**") {
				bold = true
				restyle()
				i += 2
				continue
			}

		case c == '*':
			if italic && i > 0 && !isSpaceByte(s[i-1]) {
				italic = false
				restyle()
				i++
				continue
			}
			if !italic && i+1 < len(s) && !isSpaceByte(s[i+1]) && (i == 0 || !isWordByte(s[i-1])) && closesSingle(s, i+1) {
				italic = true
				restyle()
				i++
				continue
			}

		case c == '<':
			if name, closing, n := colorTag(s[i:]); n > 0 {
				if closing {
					for j := len(colors) - 1; j >= 0; j-- {
						if colors[j] == name {
							colors = colors[:j]
							break
						}
					}
				} else {
					colors = append(colors, name)
				}
				restyle()
				i += n
				continue
			}
			if name, closing, n := styleTag(s[i:]); n > 0 {
				// A tag that isn't closed (or closes none) is text: pages
				// can mention "<kbd>".
				if closing && tags[name] > 0 || !closing && strings.Contains(strings.ToLower(s[i+n:]), "</"+name+">") {
					if closing {
						tags[name]--
					} else {
						tags[name]++
					}
					restyle()
					i += n
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		buf.WriteRune(r)
		i += size
	}
	flush()
	return out
}

func colorTag(s string) (name string, closing bool, n int) {
	end := strings.IndexByte(s, '>')
	if end < 2 || end > 12 {
		return "", false, 0
	}
	inner := s[1:end]
	if strings.HasPrefix(inner, "/") {
		closing = true
		inner = inner[1:]
	}
	inner = strings.ToLower(inner)
	if _, ok := palette[inner]; ok || inner == "rainbow" {
		return inner, closing, end + 1
	}
	return "", false, 0
}

// styleTag reads a <bold>, <italic> or <kbd> tag (or its closing tag).
func styleTag(s string) (name string, closing bool, n int) {
	end := strings.IndexByte(s, '>')
	if end < 2 || end > 8 {
		return "", false, 0
	}
	inner := strings.ToLower(s[1:end])
	if strings.HasPrefix(inner, "/") {
		closing, inner = true, inner[1:]
	}
	switch inner {
	case "bold", "italic", "kbd":
		return inner, closing, end + 1
	}
	return "", false, 0
}

func runLen(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

// findRun finds a run of exactly n c's at or after i.
func findRun(s string, i int, c byte, n int) int {
	for i < len(s) {
		j := strings.IndexByte(s[i:], c)
		if j < 0 {
			return -1
		}
		j += i
		if l := runLen(s, j, c); l == n {
			return j
		} else {
			i = j + l
		}
	}
	return -1
}

// closes reports whether a closing delimiter (preceded by a non-space)
// appears after i, outside code spans.
func closes(s string, i int, delim string) bool {
	for i < len(s) {
		if s[i] == '`' {
			n := runLen(s, i, '`')
			if end := findRun(s, i+n, '`', n); end >= 0 {
				i = end + n
				continue
			}
			i += n
			continue
		}
		if strings.HasPrefix(s[i:], delim) && i > 0 && !isSpaceByte(s[i-1]) {
			return true
		}
		i++
	}
	return false
}

func closesSingle(s string, i int) bool {
	for i < len(s) {
		if s[i] == '`' {
			n := runLen(s, i, '`')
			if end := findRun(s, i+n, '`', n); end >= 0 {
				i = end + n
				continue
			}
			i += n
			continue
		}
		if s[i] == '*' {
			if i+1 < len(s) && s[i+1] == '*' {
				i += 2
				continue
			}
			if !isSpaceByte(s[i-1]) && (i+1 == len(s) || !isWordByte(s[i+1])) {
				return true
			}
		}
		i++
	}
	return false
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }

func isWordByte(c byte) bool {
	return c == '_' || c >= 0x80 || unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c))
}

func isASCIIPunct(c byte) bool {
	return c < 0x80 && unicode.IsPunct(rune(c)) || c == '`' || c == '<' || c == '>' || c == '*' || c == '|'
}

// plainInline strips markup, for places that need bare text.
func plainInline(s string) string {
	var b strings.Builder
	for _, sg := range parseInline(s, style{}) {
		b.WriteString(sg.text)
	}
	return b.String()
}
