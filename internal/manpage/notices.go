// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package manpage

import (
	"regexp"
	"strings"
)

// Notices are what a page's source says, in comments man doesn't show,
// about whose it is and under what terms: its SPDX license identifier, if
// it has one, and its copyright lines.
type Notices struct {
	License   string
	Copyright []string
}

// maxCopyright is how many copyright lines are kept: a page's own, not
// every contributor's in a long history.
const maxCopyright = 12

var (
	roffComment = regexp.MustCompile(`^(?:[.']\s*)?\\["#](.*)$`)
	spdxLine    = regexp.MustCompile(`SPDX-License-Identifier:\s*(.+?)\s*$`)
	// A copyright notice: the word (or its sign), then a year, which
	// licenses' talk of "the above copyright notice" hasn't.
	copyright = regexp.MustCompile(`(?i)(\bcopyright\b\s*(?:\(c\)|©)?|\(c\)|©)\s*[0-9]{4}`)
	yearsOnly = regexp.MustCompile(`(?i)^(copyright|\(c\)|©|[\s,0-9-]|by)*$`)
)

// FindNotices reads a page's roff source for its notices.
func FindNotices(src []byte) Notices {
	var n Notices
	seen := map[string]bool{}
	lines := strings.Split(string(src), "\n")
	comment := func(i int) (string, bool) {
		if i >= len(lines) {
			return "", false
		}
		m := roffComment.FindStringSubmatch(strings.TrimRight(lines[i], "\r"))
		if m == nil {
			return "", false
		}
		return strings.Join(strings.Fields(strings.Trim(m[1], " \t*")), " "), true
	}
	for i := range lines {
		c, ok := comment(i)
		if !ok {
			continue
		}
		if m := spdxLine.FindStringSubmatch(c); m != nil {
			if n.License == "" {
				n.License = m[1]
			}
			continue
		}
		loc := copyright.FindStringIndex(c)
		if loc == nil || len(n.Copyright) >= maxCopyright {
			continue
		}
		// From the notice on ("This manpage is Copyright (C) 1992 …").
		t := strings.TrimRight(c[loc[0]:], " ;,.")
		// "Copyright (c) 1990, 1993" with whose on the next line.
		if yearsOnly.MatchString(t) {
			if next, ok := comment(i + 1); ok && next != "" && !copyright.MatchString(next) {
				t += " " + strings.TrimRight(next, " ;,.")
			}
		}
		if !seen[t] {
			seen[t] = true
			n.Copyright = append(n.Copyright, t)
		}
	}
	return n
}
