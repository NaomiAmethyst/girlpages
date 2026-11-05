// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import "testing"

// listingOf is a page with a version for each list of packages, as
// "distro/name/version", named by its first.
func listingOf(versions ...[]Package) *Listing {
	l := &Listing{Name: "page", Section: "1"}
	for _, ps := range versions {
		l.Versions = append(l.Versions, Version{Source: ps[0].Distro + "-" + ps[0].Name + "-" + ps[0].Version, Packages: ps})
	}
	l.derive()
	return l
}

func pkg(distro, name, version string) Package {
	return Package{Distro: distro, Name: name, Version: version}
}

func TestLatest(t *testing.T) {
	gnu := []Package{pkg("arch", "coreutils", "9.12-1"), pkg("fedora", "coreutils", "9.12-2.fc44")}
	older := []Package{pkg("debian", "coreutils", "9.7-3")}
	for _, c := range []struct {
		what string
		l    *Listing
		want string
	}{
		{"FreeBSD's release isn't a newer version", listingOf(gnu, older, []Package{pkg("freebsd", "base", "15.1")}), "arch-coreutils-9.12-1"},
		{"nor is Nix's other program", listingOf(gnu, older, []Package{pkg("nixos", "9base", "070715")}), "arch-coreutils-9.12-1"},
		{"nor Nix's own, newer, copy", listingOf(older, []Package{pkg("nixos", "coreutils", "9.99")}), "debian-coreutils-9.7-3"},
		{"but they're the latest if nothing else has it", listingOf(
			[]Package{pkg("nixos", "tool", "2.0")}, []Package{pkg("freebsd", "base", "1.0")}), "nixos-tool-2.0"},
		{"an unrelated package's higher number doesn't win", listingOf(
			[]Package{pkg("arch", "grep", "3.12-1"), pkg("fedora", "grep", "3.12-1")},
			[]Package{pkg("debian", "grep", "3.11-4")},
			[]Package{pkg("arch", "9base", "6-8")}), "arch-grep-3.12-1"},
		{"systems sharing their packaging are one vote", listingOf(
			[]Package{pkg("debian", "dash", "0.5.12-2"), pkg("ubuntu", "dash", "0.5.12-6")},
			[]Package{pkg("arch", "other", "1.0-1"), pkg("alpine", "other", "1.0-r0")}), "arch-other-1.0-1"},
		{"a tie goes to the most systems, not the highest number", listingOf(
			[]Package{pkg("debian", "dash", "0.5.12-2"), pkg("ubuntu", "dash", "0.5.12-6")},
			[]Package{pkg("alpine", "loksh", "7.9-r0")}), "debian-dash-0.5.12-2"},
		{"then the highest number", listingOf(
			[]Package{pkg("arch", "tool", "1.0-1")}, []Package{pkg("alpine", "another", "2.0-r0")}), "alpine-another-2.0-r0"},
		{"-doc packages speak for their package", listingOf(
			[]Package{pkg("alpine", "tool-doc", "2.0-r0")},
			[]Package{pkg("arch", "tool", "1.0-1")},
			[]Package{pkg("fedora", "other", "9.0-1")}), "alpine-tool-doc-2.0-r0"},
		{"the same version goes to the most places", listingOf(
			[]Package{pkg("arch", "tool", "1.0-1")},
			[]Package{pkg("debian", "tool", "1.0-1"), pkg("ubuntu", "tool", "1.0-2")}), "debian-tool-1.0-1"},
	} {
		if got := c.l.Latest(); got == nil || got.Source != c.want {
			t.Errorf("%s: got %+v, want %s", c.what, got, c.want)
		}
		if c.l.Current != c.want {
			t.Errorf("%s: current is %s, want %s", c.what, c.l.Current, c.want)
		}
	}

	// With no names to go by, it's version numbers across the page, and a
	// version with no packages at all can still be the page's.
	l := &Listing{Versions: []Version{
		{Source: "old", Packages: []Package{{URL: "https://example.com/a", Version: "1.0"}}},
		{Source: "new", Packages: []Package{{URL: "https://example.com/a", Version: "2.0"}}},
	}}
	if v := l.Latest(); v == nil || v.Source != "new" {
		t.Errorf("no names: got %+v", v)
	}
	l = &Listing{Versions: []Version{{Source: "only"}}}
	if v := l.Latest(); v == nil || v.Source != "only" {
		t.Errorf("no packages: got %+v", v)
	}
	if v := (&Listing{}).Latest(); v != nil {
		t.Errorf("no versions: got %+v", v)
	}
}

func TestVersionFrom(t *testing.T) {
	l := listingOf(
		[]Package{pkg("arch", "grep", "3.12-1"), pkg("fedora", "grep", "3.12-1")},
		[]Package{{Distro: "debian", Release: "trixie", Name: "grep", Version: "3.11-4"}},
		[]Package{{Distro: "debian", Release: "bookworm", Name: "grep", Version: "3.8-5"}},
		[]Package{pkg("arch", "9base", "6-8")},
		[]Package{pkg("freebsd", "base", "15.1")},
		[]Package{pkg("nixos", "9base", "070715")},
	)
	for _, c := range []struct{ distro, release, want string }{
		// Whatever's filtered to, it's theirs...
		{"freebsd", "", "freebsd-base-15.1"},
		{"nixos", "", "nixos-9base-070715"},
		{"debian", "bookworm", "debian-grep-3.8-5"},
		{"debian", "", "debian-grep-3.11-4"},
		// ...and of the page's main package if they have that.
		{"arch", "", "arch-grep-3.12-1"},
	} {
		if v := l.VersionFrom(c.distro, c.release); v == nil || v.Source != c.want {
			t.Errorf("from %s %s: got %+v, want %s", c.distro, c.release, v, c.want)
		}
	}
	if v := l.VersionFrom("alpine", ""); v != nil {
		t.Errorf("from alpine: got %+v", v)
	}
}

func TestUpstreamPrefersCounted(t *testing.T) {
	v := Version{Packages: []Package{pkg("freebsd", "base", "15.1"), pkg("debian", "tool", "1.2-1")}}
	if got := v.Upstream(); got != "1.2" {
		t.Errorf("got %q", got)
	}
	v = Version{Packages: []Package{pkg("freebsd", "base", "15.1")}}
	if got := v.Upstream(); got != "15.1" {
		t.Errorf("got %q", got)
	}
	// Newer agrees: FreeBSD's copy isn't newer than a counted one.
	bsd, deb := Version{Packages: []Package{pkg("freebsd", "base", "15.1")}}, Version{Packages: []Package{pkg("debian", "tool", "1.2-1")}}
	if bsd.Newer(&deb) || !deb.Newer(&bsd) {
		t.Error("FreeBSD's version counts as newer")
	}
}
