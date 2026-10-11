// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const lsText = `LS(1)                     User Commands                    LS(1)

NAME
       ls - list directory contents

SYNOPSIS
       ls [OPTION]... [FILE]...

DESCRIPTION
       List information about the FILEs.

       -a, --all
              do not ignore entries starting with .

GNU coreutils 9.7               April 2025                     LS(1)
`

func TestManVariant(t *testing.T) {
	v := ManVariant("abc", lsText)
	if v.Title != "LS(1)" || !strings.HasPrefix(v.Header, "LS(1)") || !strings.HasPrefix(v.Footer, "GNU coreutils") {
		t.Errorf("header/footer: %q %q %q", v.Title, v.Header, v.Footer)
	}
	var heads []string
	for _, s := range v.Sections {
		heads = append(heads, s.Heading)
	}
	if want := []string{"", "NAME", "SYNOPSIS", "DESCRIPTION"}; !reflect.DeepEqual(heads, want) {
		t.Errorf("headings = %q, want %q", heads, want)
	}
	if got := v.Text(); got != lsText {
		t.Errorf("Text() doesn't round-trip:\n%q\n%q", got, lsText)
	}
	if d := Description(v); d != "list directory contents" {
		t.Errorf("Description = %q", d)
	}
	// Text without man's header and footer still round-trips.
	for _, s := range []string{"just some text\n", "A\n  b\n\nC\n", "\n\nX\n"} {
		if got := ManVariant("x", s).Text(); got != s {
			t.Errorf("%q round-tripped to %q", s, got)
		}
	}
}

func TestGirlVariant(t *testing.T) {
	md := "intro bestie\n\n# ✨ NAME ✨\n\n`ls` - list stuff 💅\n\n# SYNOPSIS\n\n```\n# not a heading\nls -a\n```\n\n#nope\n## sub\n"
	g := &Generator{Model: "m"}
	v := GirlVariant("abc", "LS(1)", md, g)
	var heads []string
	for _, s := range v.Sections {
		heads = append(heads, s.Heading)
	}
	if want := []string{"", "✨ NAME ✨", "SYNOPSIS"}; !reflect.DeepEqual(heads, want) {
		t.Errorf("headings = %q, want %q", heads, want)
	}
	if got := v.Text(); got != md {
		t.Errorf("Text() doesn't round-trip:\n%q\n%q", got, md)
	}
}

func TestEscapeName(t *testing.T) {
	cases := map[string]string{
		"ls": "ls", "git-commit": "git-commit", "File::Spec": "File::Spec", "c++": "c++",
		"[": "%5B", ".hidden": "%2Ehidden", "a.b": "a.b", "50%": "50%25", "a b": "a%20b", "é": "%C3%A9",
	}
	for in, want := range cases {
		if got := EscapeName(in); got != want {
			t.Errorf("EscapeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTokensAndGrams(t *testing.T) {
	if got := Tokens("List the FILEs, --all & a x86_64 Größe"); !reflect.DeepEqual(got,
		[]string{"list", "the", "files", "all", "x86_64", "größe"}) {
		t.Errorf("Tokens = %q", got)
	}
	if got := Grams("grep"); !reflect.DeepEqual(got, []string{"^gr", "gre", "rep", "ep$"}) {
		t.Errorf("Grams = %q", got)
	}
	for _, c := range []struct {
		a, b string
		d    int
	}{{"grep", "grpe", 1}, {"list", "lsit", 1}, {"kitten", "sitting", 3}, {"", "abc", 3}, {"same", "same", 0}} {
		if d := EditDistance(c.a, c.b); d != c.d {
			t.Errorf("EditDistance(%q, %q) = %d, want %d", c.a, c.b, d, c.d)
		}
	}
}

// testStore builds a published store from a few pages.
func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	add := func(name, sec, desc, body string, girl bool) {
		text := strings.ToUpper(name) + "(" + sec + ")    Manual    " + strings.ToUpper(name) + "(" + sec + ")\n\nNAME\n       " +
			name + " - " + desc + "\n\nDESCRIPTION\n       " + body + "\n\nfooter    " + strings.ToUpper(name) + "(" + sec + ")\n"
		src := Hash([]byte(name + sec + body))
		man := ManVariant(src, text)
		mi, err := s.PutVariant(man)
		if err != nil {
			t.Fatal(err)
		}
		variants := map[string]VariantInfo{VariantMan: mi}
		if girl {
			gi, err := s.PutVariant(GirlVariant(src, man.Title, "# ✨ NAME ✨\n\n"+name+" slays\n", &Generator{Model: "fake"}))
			if err != nil {
				t.Fatal(err)
			}
			variants[VariantGirl] = gi
		}
		err = s.UpdateListing(name, sec, func(l *Listing) error {
			l.Versions = append(l.Versions, Version{Source: src, Title: man.Title, Description: Description(man),
				Added: time.Unix(1700000000, 0).UTC(), Variants: variants, Packages: []Package{{Distro: "test", Name: name}}})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("ls", "1", "list directory contents", "List information about the files in a directory.", true)
	add("grep", "1", "print lines that match patterns", "Search for patterns in each file.", true)
	add("git-commit", "1", "record changes to the repository", "Create a new commit.", false)
	add("printf", "1", "format and print data", "Print arguments according to a format.", false)
	add("printf", "3", "formatted output conversion", "The printf family produces output according to a format.", true)
	add("[", "1", "check file types and compare values", "Exit with the status determined by the expression.", false)
	add("dir", "1", "list directory contents", "List information about the files in a directory.", false)
	for i := 0; i < 30; i++ {
		add(fmt.Sprintf("filler%02d", i), "3", "a filler page", "nothing to see here", false)
	}
	pages, err := s.AllPages()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(pages); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPublishAndGC(t *testing.T) {
	s := testStore(t)
	m, err := s.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if m.Pages != 37 || m.Girlified != 3 || len(m.Sections) != 2 {
		t.Fatalf("manifest: %+v", m)
	}
	var p Pointer
	if err := s.Read(SectionPointerPath("1"), &p); err != nil || p.SHA256 != m.Sections[0].SHA256 {
		t.Fatalf("section pointer %+v, %v", p, err)
	}
	var si SectionIndex
	if err := s.Read(SectionIndexPath("1", p.SHA256), &si); err != nil {
		t.Fatal(err)
	}
	if len(si.Pages) != 6 || si.Pages[0].Name != "[" || !si.Pages[3].Girlified {
		t.Errorf("section 1: %+v", si.Pages)
	}
	// Content-addressed files really are named after their contents.
	b, _ := os.ReadFile(filepath.Join(s.Dir, SectionIndexPath("1", p.SHA256)))
	if Hash(b) != p.SHA256 {
		t.Error("section index hash mismatch")
	}
	if _, err := os.Stat(filepath.Join(s.Dir, NamePath("[", "1"))); err != nil {
		t.Errorf("escaped name pointer: %v", err)
	}

	// Change a page: its old listing becomes garbage, but nothing that's
	// still referenced may go.
	if err := s.UpdateListing("ls", "1", func(l *Listing) error { l.Versions[0].Description = "changed"; return nil }); err != nil {
		t.Fatal(err)
	}
	pages, _ := s.AllPages()
	if _, err := s.Publish(pages); err != nil {
		t.Fatal(err)
	}
	st, err := s.GC(false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Files < 2 { // the old listing and the old section 1 index, at least
		t.Errorf("GC removed %d files", st.Files)
	}
	pages2, err := s.AllPages()
	if err != nil || len(pages2) != 37 {
		t.Fatalf("after GC: %d pages, %v", len(pages2), err)
	}
	for _, p := range pages2 {
		for v := range p.Current().Variants {
			if !s.HasVariant(p.Current().Source, v) {
				t.Errorf("GC removed %s/%s", p.Pointer.Name, v)
			}
		}
	}
	if st, _ := s.GC(false); st.Files != 0 {
		t.Errorf("second GC removed %d files", st.Files)
	}
	sr := &Searcher{Client: NewClient(s.Dir, time.Second)}
	if hits, err := sr.Search(context.Background(), "ls", 1); err != nil || len(hits) != 1 || hits[0].Description != "changed" {
		t.Errorf("search after GC: %+v, %v", hits, err)
	}
}

func TestSearch(t *testing.T) {
	s := testStore(t)
	// Over HTTP, as browsers and girl see it.
	srv := httptest.NewServer(http.FileServer(http.Dir(s.Dir)))
	defer srv.Close()
	sr := &Searcher{Client: NewClient(srv.URL, 5*time.Second)}
	ctx := context.Background()
	names := func(q string) []string {
		t.Helper()
		hits, err := sr.Search(ctx, q, 3)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		var out []string
		for _, h := range hits {
			out = append(out, h.Name+"("+h.Section+")")
		}
		return out
	}
	cases := []struct {
		q     string
		first string
	}{
		{"ls", "ls(1)"},
		{"list directory", "dir(1)"}, // same score as ls(1); alphabetical
		{"git-commit", "git-commit(1)"},
		{"commit", "git-commit(1)"},
		{"printf", "printf(1)"},
		{"formatted output", "printf(3)"},
		{"grpe", "grep(1)"}, // a typo
		{"patt", "grep(1)"}, // a prefix
		{"[", "[(1)"},       // a name that's no word at all
		{"repository", "git-commit(1)"},
	}
	for _, c := range cases {
		got := names(c.q)
		if len(got) == 0 || got[0] != c.first {
			t.Errorf("search %q = %v, want %s first", c.q, got, c.first)
		}
	}
	if got := names("zzzzzz"); len(got) != 0 {
		t.Errorf("nonsense found %v", got)
	}
	// Every word should match when some page has them all.
	if got := names("list files directory"); len(got) != 2 {
		t.Errorf("AND search = %v", got)
	}
}

func TestClient(t *testing.T) {
	s := testStore(t)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		http.FileServer(http.Dir(s.Dir)).ServeHTTP(w, r)
	}))
	defer srv.Close()
	ctx := context.Background()
	for _, base := range []string{srv.URL, srv.URL + "/", s.Dir, "file://" + s.Dir} {
		c := NewClient(base, 5*time.Second)
		var p Pointer
		if err := c.Get(ctx, NamePath("[", "1"), &p); err != nil || p.Name != "[" {
			t.Fatalf("%s: pointer %+v, %v", base, p, err)
		}
		l, _, _ := s.Listing("ls", "1")
		v, err := c.Variant(ctx, l.Current, VariantGirl)
		if err != nil || !strings.Contains(v.Text(), "ls slays") {
			t.Fatalf("%s: variant %+v, %v", base, v, err)
		}
		if _, err := c.Variant(ctx, strings.Repeat("0", 64), VariantGirl); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: missing variant: %v", base, err)
		}
	}
	if !strings.Contains(strings.Join(paths, " "), "/page/name-1/%255B.json") {
		t.Errorf("escaped names should be percent-encoded again in URLs: %v", paths)
	}
	if _, err := NewClient(srv.URL, time.Second).Variant(ctx, "../../etc/passwd", VariantGirl); err == nil {
		t.Error("bad hashes should be refused")
	}
}
