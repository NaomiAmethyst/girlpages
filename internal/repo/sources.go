// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"sort"
	"strings"
)

// A source is where pages come from, to browse by: a distribution
// ("debian"), a release of one ("debian", "trixie"), or "upstream" for
// pages from git repositories.

// Upstream is the source of pages from git repositories (packages with a
// URL but no distribution).
const Upstream = "upstream"

// SourceEntry is a source in the manifest.
type SourceEntry struct {
	Distro  string `json:"distro"`
	Release string `json:"release,omitempty"`
	// Pages is how many pages have a version from it.
	Pages int `json:"pages"`
}

// Label is how a source is written: "debian trixie".
func (e SourceEntry) Label() string { return strings.TrimSpace(e.Distro + " " + e.Release) }

// Source is the distribution a package belongs to, if any.
func (p Package) Source() string {
	if p.Distro != "" {
		return p.Distro
	}
	if p.URL != "" {
		return Upstream
	}
	return ""
}

// From reports whether a version came from distro (and release, unless
// that's empty).
func (v *Version) From(distro, release string) bool {
	for _, p := range v.Packages {
		if p.Source() == distro && (release == "" || p.Release == release) {
			return true
		}
	}
	return false
}

// SourceLabels lists where a version came from, like "debian trixie".
func (v *Version) SourceLabels() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range v.Packages {
		s := p.Source()
		if s == "" {
			continue
		}
		l := strings.TrimSpace(s + " " + p.Release)
		if s == Upstream {
			l = strings.TrimSpace(s + " " + p.Name + " " + p.Version)
		}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// VersionFrom picks the version of a page to show for a source: the
// current one if it came from there, otherwise the one from there that was
// added last. It returns nil if the source doesn't have the page.
func (l *Listing) VersionFrom(distro, release string) *Version {
	if cur := l.CurrentVersion(); cur != nil && cur.From(distro, release) {
		return cur
	}
	var best *Version
	for i := range l.Versions {
		v := &l.Versions[i]
		if v.From(distro, release) && (best == nil || v.Added.After(best.Added)) {
			best = v
		}
	}
	return best
}

// Sources lists every source the pages come from, distributions before
// their releases, with how many pages each has.
func Sources(pages []PageRef) []SourceEntry {
	count := map[[2]string]int{}
	for _, p := range pages {
		seen := map[[2]string]bool{}
		for _, v := range p.Listing.Versions {
			for _, pk := range v.Packages {
				s := pk.Source()
				if s == "" {
					continue
				}
				keys := [][2]string{{s, ""}}
				if pk.Release != "" && s != Upstream {
					keys = append(keys, [2]string{s, pk.Release})
				}
				for _, k := range keys {
					if !seen[k] {
						seen[k] = true
						count[k]++
					}
				}
			}
		}
	}
	var out []SourceEntry
	for k, n := range count {
		out = append(out, SourceEntry{Distro: k[0], Release: k[1], Pages: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Distro != out[j].Distro {
			return out[i].Distro < out[j].Distro
		}
		return out[i].Release < out[j].Release
	})
	return out
}
