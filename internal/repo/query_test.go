// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestParseQuery(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Query
	}{
		{"ls -l", Query{Words: "ls -l"}},
		{`you can "specify Multiple URLs." curl`, Query{Words: "you can   curl", Phrases: [][]string{{"specify", "multiple", "urls"}}}},
		{`"unclosed phrase`, Query{Phrases: [][]string{{"unclosed", "phrase"}}}},
		{`"" "a"`, Query{}},
		{"pkg:CoreUtils sort package:debian/coreutils pkg:coreutils", Query{Words: "sort", Packages: []string{"coreutils", "debian/coreutils"}}},
		{"apkg:x", Query{Words: "apkg:x"}},
	} {
		if got := ParseQuery(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseQuery(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestHasPhrase(t *testing.T) {
	text := "You can specify\n       multiple URLs, or speci-\n       fy one."
	for _, c := range []struct {
		phrase []string
		want   bool
	}{
		{[]string{"specify", "multiple", "urls"}, true},
		{[]string{"multiple", "specify"}, false},
		{[]string{"specify", "one"}, true}, // across man's hyphenation
		{[]string{"urls", "or"}, true},
		{nil, true},
	} {
		if got := HasPhrase(text, c.phrase); got != c.want {
			t.Errorf("HasPhrase(%q) = %v", c.phrase, got)
		}
	}
}

func TestPackageKeys(t *testing.T) {
	l := &Listing{Versions: []Version{
		{Packages: []Package{{Distro: "debian", Release: "trixie", Name: "CoreUtils"}, {Distro: "arch", Name: "coreutils"}}},
		{Packages: []Package{{URL: "https://example.org"}, {Name: "has space"}}},
	}}
	if got, want := PackageKeys(l), []string{"arch/coreutils", "coreutils", "debian/coreutils"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PackageKeys = %q, want %q", got, want)
	}
}

func TestPhraseCandidates(t *testing.T) {
	full := func(ids ...int) [][2]int {
		var out [][2]int
		for _, id := range ids {
			out = append(out, [2]int{id, 10})
		}
		return out
	}
	many := func(from int) [][2]int {
		var ids []int
		for i := 0; i < maxPostings; i++ {
			ids = append(ids, from+i)
		}
		return full(ids...)
	}
	// A word listing every page with it decides, whatever the rest list.
	got, partial, _ := phraseCandidates([][][2]int{full(1, 2, 3), many(2)})
	if partial || !reflect.DeepEqual(got, map[int]float64{1: 10, 2: 20, 3: 20}) {
		t.Errorf("with a complete list: %v %v", got, partial)
	}
	// Words that are each in too many pages: those having both, partially.
	got, partial, _ = phraseCandidates([][][2]int{many(0), many(399)})
	if !partial || !reflect.DeepEqual(got, map[int]float64{399: 20}) {
		t.Errorf("both capped: %v %v", got, partial)
	}
	if got, _, _ := phraseCandidates(nil); got != nil {
		t.Errorf("no words: %v, want any page", got)
	}
}

func TestSearchPhrasesAndPackages(t *testing.T) {
	s := testStore(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(s.Dir)))
	defer srv.Close()
	sr := &Searcher{Client: NewClient(srv.URL, 5*time.Second)}
	ctx := context.Background()
	find := func(q string) ([]string, *Results) {
		t.Helper()
		r, err := sr.Find(ctx, q, "", 0)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		var out []string
		for _, h := range r.Hits {
			out = append(out, h.Name+"("+h.Section+")")
		}
		return out, r
	}
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"pkg:grep", []string{"grep(1)"}},
		{"pkg:test/printf", []string{"printf(1)", "printf(3)"}},
		{"pkg:printf output", []string{"printf(3)"}},
		{"pkg:grep pkg:ls", []string{"grep(1)", "ls(1)"}},
		{"pkg:nonesuch", nil},
		{`"information about the files"`, []string{"dir(1)", "ls(1)"}},
		{`"information about the files" ls`, []string{"ls(1)"}},
		{`"about information"`, nil},
		{`"according to a format"`, []string{"printf(1)", "printf(3)"}},
		{`"according to a format" pkg:test/printf`, []string{"printf(1)", "printf(3)"}},
		// Words in nearly every page's text (here, the fillers') can't
		// find a page, but other words can, and the phrase is checked.
		{`"nothing to see" filler07`, []string{"filler07(3)"}},
		{`"see nothing" filler07`, nil},
	} {
		got, _ := find(c.q)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q, want %q", c.q, got, c.want)
		}
	}
	if got, r := find(`"nothing to see here"`); got != nil || !r.TooCommon {
		t.Errorf("a phrase of common words: %q, too common %v", got, r.TooCommon)
	}
	// Package keys don't turn up as words a query word starts.
	if got, _ := find("pk"); got != nil {
		t.Errorf("pk: %q", got)
	}
}
