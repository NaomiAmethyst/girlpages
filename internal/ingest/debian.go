// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package ingest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DebianLicense reads the license of a Debian package unpacked in dir from
// its copyright file (usr/share/doc/NAME/copyright, or another package's it
// shares a doc directory with): its Files: * stanza's License, in SPDX's
// names where Debian's differ ("GPL-3+" is GPL-3.0-or-later). Only files in
// Debian's machine-readable format say; "" for others, and none.
func DebianLicense(dir, name string) string {
	paths := []string{filepath.Join(dir, "usr/share/doc", name, "copyright")}
	if more, _ := filepath.Glob(filepath.Join(dir, "usr/share/doc/*/copyright")); len(more) > 0 {
		paths = append(paths, more...)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if l := dep5License(string(b)); l != "" {
			return l
		}
	}
	return ""
}

// dep5License is the license of a machine-readable copyright file's Files:
// * stanza (its first, else the first stanza's that has one).
func dep5License(s string) string {
	if !strings.HasPrefix(strings.TrimSpace(s), "Format:") {
		return ""
	}
	first := ""
	for _, stanza := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n\n") {
		fields := map[string]string{}
		for _, l := range strings.Split(stanza, "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
				fields[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
		lic := fields["license"]
		if lic == "" || fields["files"] == "" {
			continue
		}
		if first == "" {
			first = lic
		}
		if strings.Fields(fields["files"])[0] == "*" {
			return DebianToSPDX(lic)
		}
	}
	return DebianToSPDX(first)
}

var (
	debianGNU     = regexp.MustCompile(`^(A?GPL|LGPL|GFDL(?:-NIV)?)-([0-9]+(?:\.[0-9]+)?)(\+)?$`)
	debianBSD     = regexp.MustCompile(`(?i)^BSD-([0-9])-clause$`)
	debianSimple  = map[string]string{"expat": "MIT", "mit": "MIT", "isc": "ISC", "zlib": "Zlib", "public-domain": "LicenseRef-public-domain", "artistic": "Artistic-1.0-Perl", "artistic-2.0": "Artistic-2.0", "mpl-2.0": "MPL-2.0", "apache-2.0": "Apache-2.0", "cc0-1.0": "CC0-1.0", "psf-2": "PSF-2.0", "unicode-dfs-2016": "Unicode-DFS-2016", "bsl-1.0": "BSL-1.0"}
	debianOperand = regexp.MustCompile(`\s+(or|and|with)\s+`)
)

// DebianToSPDX turns a Debian license expression ("GPL-2+ or Artistic") into
// SPDX's ("GPL-2.0-or-later OR Artistic-1.0-Perl"), leaving names it
// doesn't know as they are.
func DebianToSPDX(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return ""
	}
	var out []string
	ops := debianOperand.FindAllStringSubmatch(expr, -1)
	for i, term := range debianOperand.Split(expr, -1) {
		out = append(out, debianTerm(strings.Trim(term, "()")))
		if i < len(ops) {
			out = append(out, strings.ToUpper(ops[i][1]))
		}
	}
	return strings.Join(out, " ")
}

func debianTerm(t string) string {
	if m := debianGNU.FindStringSubmatch(t); m != nil {
		ver := m[2]
		if !strings.Contains(ver, ".") {
			ver += ".0"
		}
		name := m[1]
		if name == "GFDL-NIV" {
			return "GFDL-" + ver + map[bool]string{true: "-or-later", false: "-only"}[m[3] != ""] + "-no-invariants"
		}
		return name + "-" + ver + map[bool]string{true: "-or-later", false: "-only"}[m[3] != ""]
	}
	if m := debianBSD.FindStringSubmatch(t); m != nil {
		return "BSD-" + m[1] + "-Clause"
	}
	if s, ok := debianSimple[strings.ToLower(t)]; ok {
		return s
	}
	return t
}
