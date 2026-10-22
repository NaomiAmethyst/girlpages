// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"regexp"
	"slices"
	"strings"
)

// Query is a search query taken apart: its words, the phrases in quotes,
// and the packages it keeps to (pkg:coreutils, package:debian/coreutils).
type Query struct {
	// Words is the rest of the query, searched as it always has been.
	Words string
	// Phrases are the quoted phrases, as their Tokens: pages must have
	// each, those words in that order.
	Phrases [][]string
	// Packages are package keys (see PackageKeys): pages must come from
	// one of them.
	Packages []string
}

// PackagePrefix starts a package's key in the words index: pkg:coreutils
// lists every page from a package called coreutils, pkg:debian/coreutils
// those from Debian's. Words never have a colon, so they can't clash.
const PackagePrefix = "pkg:"

var (
	packageTerm = regexp.MustCompile(`(?i)(?:^|\s)(?:pkg|package):(\S+)`)
	phraseTerm  = regexp.MustCompile(`"([^"]*)("|$)`)
)

// ParseQuery takes a query apart. A phrase is in double quotes (an
// unclosed one runs to the end); a package filter is pkg:name or
// package:name, the name as a package is called, or distro/name.
func ParseQuery(q string) Query {
	var out Query
	q = phraseTerm.ReplaceAllStringFunc(q, func(m string) string {
		if ts := Tokens(phraseTerm.FindStringSubmatch(m)[1]); len(ts) > 0 {
			out.Phrases = append(out.Phrases, ts)
		}
		return " "
	})
	q = packageTerm.ReplaceAllStringFunc(q, func(m string) string {
		if k := strings.ToLower(packageTerm.FindStringSubmatch(m)[1]); !slices.Contains(out.Packages, k) {
			out.Packages = append(out.Packages, k)
		}
		return " "
	})
	out.Words = strings.TrimSpace(q)
	return out
}

// PackageKeys are what a page's packages are found by: each package's
// name, and its distribution's name for it ("coreutils",
// "debian/coreutils"), in lower case.
func PackageKeys(l *Listing) []string {
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if k = strings.ToLower(k); k != "" && !strings.ContainsAny(k, " \t\n") && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, v := range l.Versions {
		for _, p := range v.Packages {
			if p.Name == "" {
				continue
			}
			add(p.Name)
			if p.Distro != "" {
				add(p.Distro + "/" + p.Name)
			}
		}
	}
	slices.Sort(out)
	return out
}

// HasPhrase reports whether the words of text, as Tokens splits them,
// have phrase in them in a row. Words man broke across lines with a
// hyphen ("speci-\n  fy") are found either way.
func HasPhrase(text string, phrase []string) bool {
	if len(phrase) == 0 {
		return true
	}
	if inRow(Tokens(text), phrase) {
		return true
	}
	joined := hyphenBreak.ReplaceAllString(text, "$1$2")
	return joined != text && inRow(Tokens(joined), phrase)
}

var hyphenBreak = regexp.MustCompile(`(\pL)-\n\s*(\pL)`)

func inRow(words, phrase []string) bool {
	for i := 0; i+len(phrase) <= len(words); i++ {
		if words[i] == phrase[0] && slices.Equal(words[i:i+len(phrase)], phrase) {
			return true
		}
	}
	return false
}
