// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package crawl

import (
	"strconv"
	"strings"
	"unicode"
)

// Repositories (rpm-md ones especially) often keep older builds next to
// the newest; a crawl only wants the newest of each package.

// CompareDeb compares Debian versions ([epoch:]upstream[-revision]) the way
// dpkg does.
func CompareDeb(a, b string) int {
	ea, ua, ra := splitDeb(a)
	eb, ub, rb := splitDeb(b)
	if ea != eb {
		return cmpInt(ea, eb)
	}
	if c := debPart(ua, ub); c != 0 {
		return c
	}
	return debPart(ra, rb)
}

func splitDeb(v string) (int, string, string) {
	epoch := 0
	if e, rest, ok := strings.Cut(v, ":"); ok {
		epoch, _ = strconv.Atoi(e)
		v = rest
	}
	rev := ""
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		v, rev = v[:i], v[i+1:]
	}
	return epoch, v, rev
}

// debOrder ranks a character for dpkg: ~ before the end of the string,
// which is before letters, which are before everything else.
func debOrder(c byte) int {
	switch {
	case c == '~':
		return -1
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		return int(c)
	default:
		return int(c) + 256
	}
}

func debPart(a, b string) int {
	for a != "" || b != "" {
		// Non-digits, character by character.
		for (a != "" && !isDigit(a[0])) || (b != "" && !isDigit(b[0])) {
			ca, cb := 0, 0
			if a != "" && !isDigit(a[0]) {
				ca = debOrder(a[0])
			}
			if b != "" && !isDigit(b[0]) {
				cb = debOrder(b[0])
			}
			if ca != cb {
				return cmpInt(ca, cb)
			}
			if a != "" && !isDigit(a[0]) {
				a = a[1:]
			}
			if b != "" && !isDigit(b[0]) {
				b = b[1:]
			}
		}
		// Then a number.
		var na, nb string
		na, a = digitRun(a)
		nb, b = digitRun(b)
		if c := cmpNum(na, nb); c != 0 {
			return c
		}
	}
	return 0
}

// CompareRPM compares rpm versions ([epoch:]version-release) the way
// rpmvercmp does; pacman's vercmp is the same algorithm.
func CompareRPM(a, b string) int {
	ea, va := splitEpoch(a)
	eb, vb := splitEpoch(b)
	if ea != eb {
		return cmpInt(ea, eb)
	}
	// Version, then release, each compared on its own.
	va, ra := splitRelease(va)
	vb, rb := splitRelease(vb)
	if c := rpmvercmp(va, vb); c != 0 {
		return c
	}
	return rpmvercmp(ra, rb)
}

func splitRelease(v string) (string, string) {
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

func splitEpoch(v string) (int, string) {
	if e, rest, ok := strings.Cut(v, ":"); ok {
		if n, err := strconv.Atoi(e); err == nil {
			return n, rest
		}
	}
	return 0, v
}

func rpmvercmp(a, b string) int {
	if a == b {
		return 0
	}
	for {
		// Separators don't count, except ~ (before anything) and ^ (after
		// the end, before anything else).
		a = strings.TrimLeftFunc(a, func(r rune) bool { return !isAlnum(r) && r != '~' && r != '^' })
		b = strings.TrimLeftFunc(b, func(r rune) bool { return !isAlnum(r) && r != '~' && r != '^' })
		if strings.HasPrefix(a, "~") || strings.HasPrefix(b, "~") {
			if !strings.HasPrefix(a, "~") {
				return 1
			}
			if !strings.HasPrefix(b, "~") {
				return -1
			}
			a, b = a[1:], b[1:]
			continue
		}
		if strings.HasPrefix(a, "^") || strings.HasPrefix(b, "^") {
			switch {
			case a == "":
				return -1
			case b == "":
				return 1
			case !strings.HasPrefix(a, "^"):
				return 1
			case !strings.HasPrefix(b, "^"):
				return -1
			}
			a, b = a[1:], b[1:]
			continue
		}
		if a == "" || b == "" {
			break
		}
		var sa, sb string
		if isDigit(a[0]) {
			sa, a = digitRun(a)
			sb, b = digitRun(b)
			if sb == "" {
				return 1 // numbers beat letters
			}
			if c := cmpNum(sa, sb); c != 0 {
				return c
			}
			continue
		}
		sa, a = alphaRun(a)
		sb, b = alphaRun(b)
		if sb == "" {
			return -1
		}
		if c := strings.Compare(sa, sb); c != 0 {
			return c
		}
	}
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	}
	return 1
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlnum(r rune) bool { return r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)) }

func digitRun(s string) (string, string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

func alphaRun(s string) (string, string) {
	i := 0
	for i < len(s) && (s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z') {
		i++
	}
	return s[:i], s[i:]
}

// cmpNum compares strings of digits as numbers, however long.
func cmpNum(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return cmpInt(len(a), len(b))
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// newest keeps only the newest version of each package and architecture.
func newest(pkgs []Package, cmp func(a, b string) int) []Package {
	best := map[string]int{}
	var out []Package
	for _, p := range pkgs {
		k := p.Name + "\x00" + p.Arch
		if i, ok := best[k]; ok {
			if cmp(p.Version, out[i].Version) > 0 {
				out[i] = p
			}
			continue
		}
		best[k] = len(out)
		out = append(out, p)
	}
	return out
}
