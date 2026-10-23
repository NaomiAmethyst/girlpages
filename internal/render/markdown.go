// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package render

import (
	"strings"
)

// Markdown turns girlpages markup into plain Markdown: it's Markdown
// already, but for its tags, so colour tags (<pink>…</pink>, <rainbow>) go,
// <bold> and <italic> become ** and *, and <kbd> stays (it's HTML, which
// Markdown takes). Code, in spans and fenced blocks, is left as it is, as
// are tags that Render shows as text (a <bold> never closed).
func Markdown(markup string) string {
	lines := strings.SplitAfter(markup, "\n")
	var b strings.Builder
	fence := ""              // the fence of the code block we're in, if we are
	open := map[string]int{} // style tags open, which may close on a later line
	at := 0                  // where the line starts in markup
	for _, l := range lines {
		at += len(l)
		if m := fenceRE.FindStringSubmatch(l); m != nil {
			switch {
			case fence == "":
				fence = m[1]
			case strings.HasPrefix(m[1], fence) && strings.TrimSpace(l) == m[1]:
				fence = ""
			}
			b.WriteString(l)
			continue
		}
		if fence != "" {
			b.WriteString(l)
			continue
		}
		b.WriteString(markdownLine(l, markup[at:], open))
	}
	return b.String()
}

// markdownLine is Markdown for a line of markup outside code blocks; rest
// is what follows it, where a tag opened on it may close, and open the
// style tags open so far.
func markdownLine(s, rest string, open map[string]int) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]):
			b.WriteString(s[i : i+2])
			i += 2
			continue
		case c == '`':
			n := runLen(s, i, '`')
			if end := findRun(s, i+n, '`', n); end >= 0 {
				b.WriteString(s[i : end+n])
				i = end + n
				continue
			}
			b.WriteString(s[i : i+n])
			i += n
			continue
		case c == '<':
			if _, _, n := colorTag(s[i:]); n > 0 {
				i += n
				continue
			}
			if name, closing, n := styleTag(s[i:]); n > 0 && name != "kbd" {
				after := strings.ToLower(s[i+n:] + rest)
				if closing && open[name] > 0 || !closing && strings.Contains(after, "</"+name+">") {
					if closing {
						open[name]--
					} else {
						open[name]++
					}
					if name == "bold" {
						b.WriteString("**")
					} else {
						b.WriteString("*")
					}
					i += n
					continue
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}
