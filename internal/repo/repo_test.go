// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"bytes"
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
	"unicode/utf8"

	"github.com/NaomiAmethyst/girlpages/internal/precompress"
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
	// npm's pages have the whole manual in NAME, under subsections: only
	// its first paragraph is the description.
	npm := "NPM-JSON(5)  File Formats  NPM-JSON(5)\n\nNAME\n       package.json - Specifics of npm's\n       package.json handling\n\n   Description\n       This document is all you need to know.\n\nnpm  2026  NPM-JSON(5)\n"
	if d := Description(ManVariant("n", npm)); d != "Specifics of npm's package.json handling" {
		t.Errorf("npm Description = %q", d)
	}
	if d := ShortDescription(strings.Repeat("word ", 100)); len(d) > MaxDescription+len("…") || !strings.HasSuffix(d, "word…") {
		t.Errorf("ShortDescription = %q", d)
	}
	if d := ShortDescription(strings.Repeat("é", 150)); !utf8.ValidString(d) || !strings.HasSuffix(d, "é…") {
		t.Errorf("ShortDescription without spaces = %q", d)
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
	// A program with its version in its name, and pages that mention it.
	add("icecast2", "1", "an MP3/Ogg Vorbis broadcast streaming media server", "Streams audio to listeners.", false)
	add("ezstream", "1", "source client for Icecast with external de-/encoder support", "Sends audio to an Icecast server.", false)
	add("icecast_selinux", "8", "Security Enhanced Linux Policy for the icecast processes", "Confines the icecast server.", false)
	add("python3", "1", "an interpreted, interactive, object-oriented programming language", "Runs scripts.", false)
	add("python", "1", "an interpreted, interactive, object-oriented programming language", "Runs scripts.", false)
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
	if m.Pages != 42 || m.Variants["girl"] != 3 || len(m.Sections) != 3 || len(m.Chapters) != 3 || m.Sections[1].Variants["girl"] != 1 {
		t.Fatalf("manifest: %+v", m)
	}
	var p Pointer
	if err := s.Read(SectionPointerPath("1"), &p); err != nil || p.SHA256 != m.Chapters[0].SHA256 {
		t.Fatalf("chapter pointer %+v, %v", p, err)
	}
	var si ChapterIndex
	if err := s.Read(SectionIndexPath("1", p.SHA256), &si); err != nil {
		t.Fatal(err)
	}
	if len(si.Pages) != 10 || si.Pages[0].Name != "[" || fmt.Sprint(si.Pages[4].Variants) != "[girl man]" {
		t.Errorf("chapter 1: %+v", si.Pages)
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
	if err != nil || len(pages2) != 42 {
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
		// A versioned name is the program, before pages mentioning it; a
		// page called just that is better still.
		{"icecast", "icecast2(1)"},
		{"python", "python(1)"},
		{"python3", "python3(1)"},
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

func TestVariantIndex(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// A variant made with some other prompt, on one version.
	l, _, _ := s.Listing("grep", "1")
	pirate, err := s.PutVariant(&Variant{Format: Format, Variant: "pirate", Source: l.Current, Syntax: SyntaxMarkup,
		Sections: []Section{{Heading: "NAME", Body: "arr, grep be findin' lines\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	s.UpdateListing("grep", "1", func(l *Listing) error { l.CurrentVersion().Variants["pirate"] = pirate; return nil })
	pages, _ := s.AllPages()
	m, err := s.Publish(pages)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(m.Variants) != "map[girl:3 man:42 pirate:1]" || len(m.Index.Variants) != 3 {
		t.Fatalf("variants: %v %v", m.Variants, m.Index.Variants)
	}
	c := NewClient(s.Dir, time.Second)
	idx := map[string]*VariantIndex{}
	names := []string{"girl", "man", "pirate"}
	for _, v := range names {
		idx[v] = c.VariantIndex(v)
	}
	counts := map[string]int{}
	for _, p := range pages {
		for _, ver := range p.Listing.Versions {
			for _, variant := range names {
				has, err := idx[variant].Has(ctx, ver.Source)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := ver.Variants[variant]; ok != has {
					t.Errorf("%s %s: index says %v", p.Pointer.Name, variant, has)
				}
				if has {
					counts[variant]++
				}
			}
		}
	}
	if counts["girl"] != 3 || counts["man"] != 42 || counts["pirate"] != 1 {
		t.Errorf("counts: %v", counts)
	}
	if has, err := idx["girl"].Has(ctx, strings.Repeat("f", 64)); has || err != nil {
		t.Errorf("unknown source: %v, %v", has, err)
	}
	if _, err := c.VariantIndex("nobody").Has(ctx, l.Current); !errors.Is(err, ErrNoIndex) {
		t.Errorf("a variant nobody has: %v", err)
	}
	if _, err := c.VariantIndex("../etc").Has(ctx, l.Current); err == nil {
		t.Error("bad variant names should be refused")
	}
	// GC keeps every variant's index.
	if _, err := s.GC(false); err != nil {
		t.Fatal(err)
	}
	if has, err := NewClient(s.Dir, time.Second).VariantIndex("pirate").Has(ctx, l.Current); !has || err != nil {
		t.Errorf("after GC: %v, %v", has, err)
	}
}

func TestVariantIndexShards(t *testing.T) {
	defer func(n int) { sourceShardSize = n }(sourceShardSize)
	sourceShardSize = 1000
	s, _ := Open(t.TempDir())
	var pages []PageRef
	want := map[string]bool{}
	for i := 0; i < 2*sourceShardSize+10; i++ {
		src := Hash([]byte{byte(i), byte(i >> 8), 1})
		girl := i%2 == 0
		vs := map[string]VariantInfo{VariantMan: {}}
		if girl {
			vs[VariantGirl] = VariantInfo{}
		}
		want[src] = girl
		pages = append(pages, PageRef{Pointer: Pointer{Name: fmt.Sprint(i), Section: "1"},
			Listing: &Listing{Versions: []Version{{Source: src, Variants: vs}}}})
	}
	shas, err := s.writeVariantIndexes(pages)
	if err != nil {
		t.Fatal(err)
	}
	var sm ShardManifest
	s.Read(IndexManifestPath(VariantIndexKind("girl"), shas["girl"]), &sm)
	if len(sm.Shards) != 2 {
		t.Fatalf("%d girl shards", len(sm.Shards))
	}
	s.Read(IndexManifestPath(VariantIndexKind("man"), shas["man"]), &sm)
	if len(sm.Shards) != 3 {
		t.Fatalf("%d man shards", len(sm.Shards))
	}
	s.Write(ManifestPath(), &Manifest{Format: Format, Index: IndexEntry{Variants: shas}})
	g := NewClient(s.Dir, time.Second).VariantIndex("girl")
	for src, girl := range want {
		if has, err := g.Has(context.Background(), src); err != nil || has != girl {
			t.Fatalf("%s: %v, %v (want %v)", src[:8], has, err, girl)
		}
	}
}

func TestRefreshPromptVersions(t *testing.T) {
	s := testStore(t)
	// Forget the prompt versions, as listings from before they were
	// recorded did.
	pages, _ := s.AllPages()
	for _, p := range pages {
		s.UpdateListing(p.Pointer.Name, p.Pointer.Section, func(l *Listing) error {
			for i := range l.Versions {
				for k, info := range l.Versions[i].Variants {
					info.PromptVersion = 0
					l.Versions[i].Variants[k] = info
				}
			}
			return nil
		})
	}
	// The test store's girl variants record no prompt version; give one.
	l, _, _ := s.Listing("ls", "1")
	g, _ := s.Variant(l.Current, VariantGirl)
	g.Generator.PromptVersion = 7
	info, _ := s.PutVariant(g)
	info.PromptVersion = 0
	s.UpdateListing("ls", "1", func(l *Listing) error { l.CurrentVersion().Variants[VariantGirl] = info; return nil })

	pages, _ = s.AllPages()
	n, err := s.Refresh(pages)
	if err != nil || n != 1 {
		t.Fatalf("refresh changed %d listings, %v", n, err)
	}
	l, _, _ = s.Listing("ls", "1")
	if pv := l.CurrentVersion().Variants[VariantGirl].PromptVersion; pv != 7 {
		t.Errorf("ls girl prompt %d", pv)
	}
	pages, _ = s.AllPages()
	if n, _ := s.Refresh(pages); n != 0 {
		t.Errorf("second refresh changed %d", n)
	}
}

func TestClientEncodings(t *testing.T) {
	s := testStore(t)
	raw, _ := os.ReadFile(filepath.Join(s.Dir, ManifestPath()))
	for _, e := range precompress.Encodings {
		if e.Name == "zstd" {
			continue // for browsers (see AcceptEncoding)
		}
		t.Run(e.Name, func(t *testing.T) {
			data, _ := e.Compress(raw)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.Header.Get("Accept-Encoding"), e.Name) {
					t.Errorf("client didn't offer %s: %q", e.Name, r.Header.Get("Accept-Encoding"))
				}
				w.Header().Set("Content-Encoding", e.Name)
				w.Write(data)
			}))
			defer srv.Close()
			if m, err := NewClient(srv.URL, time.Second).Manifest(context.Background()); err != nil || m.Pages != 42 {
				t.Errorf("%+v, %v", m, err)
			}
		})
	}
	// A compression bomb is refused rather than read into memory.
	br, _ := precompress.Lookup("br")
	bomb, _ := br.Compress(bytes.Repeat([]byte{' '}, maxDocument+10))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		w.Write(bomb)
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL, 5*time.Second).Manifest(context.Background()); err == nil || !strings.Contains(err.Error(), "bigger than") {
		t.Errorf("bomb: %v", err)
	}
	// An encoding it didn't ask for is an error, not garbage.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "zstd")
		w.Write(raw)
	}))
	defer srv2.Close()
	if _, err := NewClient(srv2.URL, time.Second).Manifest(context.Background()); err == nil {
		t.Error("unknown encoding should fail")
	}
	// GC keeps a live file's compressed copies.
	if _, err := precompress.Sweep(s.Dir, precompress.Encodings); err != nil {
		t.Fatal(err)
	}
	if st, err := s.GC(false); err != nil || st.Files != 0 {
		t.Errorf("GC removed %d files, %v", st.Files, err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, ManifestPath()+".br")); err != nil {
		t.Error(err)
	}
}

// A repository from before generations: one file per variant, changed in
// place. Refresh moves each into generation 1, and GC tidies up.
func TestMigrateVariants(t *testing.T) {
	s := testStore(t)
	pages, _ := s.AllPages()
	// Turn it back into the old layout.
	for _, p := range pages {
		for _, v := range p.Listing.Versions {
			cs, _ := s.Contents(v.Source)
			for variant := range v.Variants {
				g := cs.Latest(variant)
				b, _ := s.ReadRaw(VariantPath(v.Source, variant, g.SHA256))
				WriteFile(filepath.Join(s.Dir, legacyVariantPath(v.Source, variant)), b)
				os.Remove(filepath.Join(s.Dir, VariantPath(v.Source, variant, g.SHA256)))
			}
			os.Remove(filepath.Join(s.Dir, ContentsPath(v.Source)))
		}
		s.UpdateListing(p.Pointer.Name, p.Pointer.Section, func(l *Listing) error {
			for i := range l.Versions {
				for k, info := range l.Versions[i].Variants {
					info.Generation = 0
					l.Versions[i].Variants[k] = info
				}
			}
			return nil
		})
	}
	pages, _ = s.AllPages()
	n, err := s.Refresh(pages)
	if err != nil || n != len(pages) {
		t.Fatalf("refreshed %d of %d: %v", n, len(pages), err)
	}
	pages, _ = s.AllPages()
	for _, p := range pages {
		v := p.Current()
		for variant, info := range v.Variants {
			got, err := s.Variant(v.Source, variant)
			if err != nil || info.Generation != 1 || got.Variant != variant {
				t.Fatalf("%s %s: %+v, %v", p.Pointer.Name, variant, info, err)
			}
		}
	}
	if _, err := s.Publish(pages); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GC(false); err != nil {
		t.Fatal(err)
	}
	l, _, _ := s.Listing("ls", "1")
	if _, err := os.Stat(filepath.Join(s.Dir, legacyVariantPath(l.Current, VariantGirl))); err == nil {
		t.Error("GC should remove the old files")
	}
	if n, _ := s.Refresh(pages); n != 0 {
		t.Errorf("a second refresh changed %d", n)
	}
	// A client reads it the new way.
	if v, err := NewClient(s.Dir, time.Second).Variant(context.Background(), l.Current, VariantGirl); err != nil || !strings.Contains(v.Text(), "ls slays") {
		t.Errorf("client: %v", err)
	}
}

// Making a variant again adds a generation; the same again doesn't.
func TestGenerations(t *testing.T) {
	s := testStore(t)
	l, _, _ := s.Listing("ls", "1")
	g1, _ := s.Variant(l.Current, VariantGirl)
	again, err := s.PutVariant(g1)
	if err != nil || again.Generation != 1 {
		t.Fatalf("same again: %+v, %v", again, err)
	}
	g2 := *g1
	g2.Sections = []Section{{Heading: "NAME", Body: "ls, but newer\n"}}
	info, err := s.PutVariant(&g2)
	if err != nil || info.Generation != 2 {
		t.Fatalf("new generation: %+v, %v", info, err)
	}
	if v, _ := s.Variant(l.Current, VariantGirl); !strings.Contains(v.Text(), "newer") {
		t.Error("the latest generation should be read")
	}
	c := NewClient(s.Dir, time.Second)
	if v, err := c.VariantAt(context.Background(), l.Current, VariantGirl, l.CurrentVersion().Variants[VariantGirl].SHA256); err != nil || !strings.Contains(v.Text(), "ls slays") {
		t.Errorf("generation 1 by hash: %v", err)
	}
	if _, err := c.VariantAt(context.Background(), l.Current, VariantGirl, info.SHA256[:63]+"0"); err == nil {
		t.Error("a hash that isn't there should fail")
	}
}
