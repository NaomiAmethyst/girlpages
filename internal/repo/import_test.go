// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// addVersion adds a version of a page, with a girl variant if girl isn't
// empty, and returns its source hash.
func addVersion(t *testing.T, s *Store, name, sec, desc, girl string, pkg Package, added int64) string {
	t.Helper()
	text := strings.ToUpper(name) + "(" + sec + ")   M   " + strings.ToUpper(name) + "(" + sec + ")\n\nNAME\n       " +
		name + " - " + desc + "\n\nfooter   " + strings.ToUpper(name) + "(" + sec + ")\n"
	src := Hash([]byte(text))
	vs := map[string]VariantInfo{}
	info, err := s.PutVariant(ManVariant(src, text))
	if err != nil {
		t.Fatal(err)
	}
	vs[VariantMan] = info
	if girl != "" {
		if info, err = s.PutVariant(GirlVariant(src, "", "# NAME\n\n"+girl+"\n", &Generator{Model: "m"})); err != nil {
			t.Fatal(err)
		}
		vs[VariantGirl] = info
	}
	err = s.UpdateListing(name, sec, func(l *Listing) error {
		if v := l.Find(src); v != nil {
			v.Packages = append(v.Packages, pkg)
			for k, i := range vs {
				v.Variants[k] = i
			}
			return nil
		}
		l.Versions = append(l.Versions, Version{Source: src, Description: desc, Added: time.Unix(added, 0).UTC(),
			Variants: vs, Packages: []Package{pkg}})
		l.Current = src
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func publish(t *testing.T, s *Store) {
	t.Helper()
	pages, err := s.AllPages()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(pages); err != nil {
		t.Fatal(err)
	}
}

func listing(t *testing.T, s *Store, name, sec string) *Listing {
	t.Helper()
	l, _, err := s.Listing(name, sec)
	if err != nil || l == nil {
		t.Fatalf("%s(%s): %v, %v", name, sec, l, err)
	}
	return l
}

func TestImport(t *testing.T) {
	deb := Package{Distro: "debian", Name: "coreutils", Version: "9.7"}
	arch := Package{Distro: "arch", Name: "coreutils", Version: "9.7-1"}

	newLocal := func() (*Store, map[string]string) {
		a, _ := Open(t.TempDir())
		srcs := map[string]string{
			"ls":   addVersion(t, a, "ls", "1", "list directory contents", "", deb, 100),
			"grep": addVersion(t, a, "grep", "1", "print lines", "grep slays", deb, 100),
			"cat":  addVersion(t, a, "cat", "1", "concatenate files", "our cat", deb, 100),
		}
		// dog is an alias of cat: the same version, so the same files.
		cat := listing(t, a, "cat", "1")
		if err := a.UpdateListing("dog", "1", func(l *Listing) error {
			l.Versions, l.Current = cat.Versions, cat.Current
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return a, srcs
	}
	b, _ := Open(filepath.Join(t.TempDir(), "repo"))
	addVersion(t, b, "ls", "1", "list directory contents", "ls but cute", arch, 200) // same version, girlified there
	addVersion(t, b, "grep", "1", "print lines matching", "new grep", arch, 300)     // a newer version
	addVersion(t, b, "cat", "1", "concatenate files", "their cat", arch, 200)        // a different conversion
	addVersion(t, b, "tar", "1", "an archiving utility", "tar slays", arch, 200)     // a new page
	addVersion(t, b, "bad", "8", "tampered with", "fine", arch, 200)                 // see below
	// A variant made with another prompt comes along too.
	lt := listing(t, b, "tar", "1")
	pirate, _ := b.PutVariant(&Variant{Format: Format, Variant: "pirate", Source: lt.Current, Syntax: SyntaxMarkup,
		Sections: []Section{{Heading: "NAME", Body: "arr\n"}}})
	b.UpdateListing("tar", "1", func(l *Listing) error { l.CurrentVersion().Variants["pirate"] = pirate; return nil })
	publish(t, b)
	// Tamper with a file after publishing: its hash no longer matches.
	lb := listing(t, b, "bad", "8")
	evil := lb.CurrentVersion().Variants[VariantGirl].SHA256
	os.WriteFile(filepath.Join(b.Dir, VariantPath(lb.Current, VariantGirl, evil)), []byte(`{"evil":true}`), 0o644)

	srv := httptest.NewServer(http.FileServer(http.Dir(b.Dir)))
	defer srv.Close()
	ctx := context.Background()

	// A dry run changes nothing.
	a, srcs := newLocal()
	before, _ := a.AllPages()
	st, err := a.Import(ctx, NewClient(srv.URL, 5*time.Second), ImportOptions{Jobs: 4, DryRun: true})
	if err != nil || st.New != 2 || st.Versions != 3 {
		t.Errorf("dry run: %+v, %v", st, err)
	}
	after, _ := a.AllPages()
	if len(after) != len(before) {
		t.Error("the dry run changed things")
	}

	st, err = a.Import(ctx, NewClient(srv.URL, 5*time.Second), ImportOptions{Jobs: 4})
	if err != nil {
		t.Fatal(err)
	}
	if st.Pages != 5 || st.New != 2 || st.Conflicts != 1 || st.Replaced != 0 || st.Failed != 1 {
		t.Errorf("import: %+v", st)
	}
	// ls: the same version gains their girl variant and their package.
	l := listing(t, a, "ls", "1")
	v := l.Find(srcs["ls"])
	if len(l.Versions) != 1 || v.Variants[VariantGirl].SHA256 == "" || len(v.Packages) != 2 {
		t.Errorf("ls: %+v", l)
	}
	if g, err := a.Variant(srcs["ls"], VariantGirl); err != nil || !strings.Contains(g.Text(), "ls but cute") {
		t.Errorf("ls girl variant: %v", err)
	}
	// grep: their version is added, and it's current: both are 9.7, but
	// theirs was added later.
	l = listing(t, a, "grep", "1")
	if len(l.Versions) != 2 || l.Current == srcs["grep"] {
		t.Errorf("grep: %+v", l)
	}
	// cat: the conversions differ; ours stays.
	if g, _ := a.Variant(srcs["cat"], VariantGirl); !strings.Contains(g.Text(), "our cat") {
		t.Errorf("cat should keep ours: %q", g.Text())
	}
	// tar: new, with their current, and every variant.
	if l := listing(t, a, "tar", "1"); l.CurrentVersion().Description != "an archiving utility" ||
		l.CurrentVersion().Variants["pirate"].SHA256 == "" || !a.HasVariant(l.Current, "pirate") {
		t.Errorf("tar: %+v", l)
	}
	// bad: the tampered variant was refused, but the rest of the page came.
	if l := listing(t, a, "bad", "8"); l.CurrentVersion().Variants[VariantGirl].SHA256 != "" || a.HasVariant(l.Current, VariantGirl) {
		t.Errorf("tampered variant was imported: %+v", l)
	}
	// Everything listed is there and matches.
	pages, _ := a.AllPages()
	for _, p := range pages {
		for _, ver := range p.Listing.Versions {
			for variant, info := range ver.Variants {
				raw, err := a.ReadRaw(VariantPath(ver.Source, variant, info.SHA256))
				if err != nil || Hash(raw) != info.SHA256 {
					t.Errorf("%s(%s) %s: listed %s, stored %v", p.Pointer.Name, p.Pointer.Section, variant, info.SHA256[:8], err)
				}
			}
		}
	}
	// Again: nothing more to do.
	st, err = a.Import(ctx, NewClient(srv.URL, 5*time.Second), ImportOptions{Jobs: 4})
	if err != nil || st.New+st.Versions+st.Variants+st.Packages != 0 {
		t.Errorf("second import: %+v, %v", st, err)
	}

	// Preferring theirs, from a directory this time.
	a, srcs = newLocal()
	st, err = a.Import(ctx, NewClient(b.Dir, time.Second), ImportOptions{PreferRemote: true})
	if err != nil || st.Replaced != 1 {
		t.Fatalf("prefer remote: %+v, %v", st, err)
	}
	// Theirs is a second generation; ours is still there as the first.
	cs, _ := a.Contents(srcs["cat"])
	gens := cs.Variants[VariantGirl]
	if len(gens) != 2 || cs.Latest(VariantGirl).Generation != 2 {
		t.Fatalf("cat's generations: %+v", gens)
	}
	for _, alias := range []string{"cat", "dog"} {
		l := listing(t, a, alias, "1")
		g, _ := a.Variant(srcs["cat"], VariantGirl)
		if !strings.Contains(g.Text(), "their cat") || l.Find(srcs["cat"]).Variants[VariantGirl] != *cs.Latest(VariantGirl) {
			t.Errorf("%s should point at their conversion", alias)
		}
	}
	if err := a.Read(VariantPath(srcs["cat"], VariantGirl, gens[0].SHA256), new(Variant)); err != nil {
		t.Errorf("generation 1 should still be readable: %v", err)
	}
}

// Shards end with letters when that's near enough.
func TestLetterShards(t *testing.T) {
	defer func(f, m int) { shardFirst, shardMore = f, m }(shardFirst, shardMore)
	shardFirst, shardMore = 10, 20
	run := func(spec ...any) ([]int, []string) {
		var sizes []int
		var letters []string
		for i := 0; i < len(spec); i += 2 {
			for j := 0; j < spec[i+1].(int); j++ {
				sizes = append(sizes, 1)
				letters = append(letters, spec[i].(string))
			}
		}
		return sizes, letters
	}
	for _, c := range []struct {
		spec []any
		want string
	}{
		// Back to B's start (5 of 10 is half), then on to its end (12 of
		// 10 is 20% over), then the rest.
		{[]any{"A", 5, "B", 12, "C", 3}, "[0 5 17]"},
		// A letter too long for either: cut at the target, then the bigger.
		{[]any{"A", 50}, "[0 10 30]"},
		// Small letters share a shard; one that would make it too big
		// starts the next.
		{[]any{"A", 3, "B", 3, "C", 3, "D", 8}, "[0 9]"},
		{[]any{"A", 4}, "[0]"},
		{nil, "[]"},
	} {
		sizes, letters := run(c.spec...)
		if got := fmt.Sprint(letterShards(sizes, letters)); got != c.want && !(c.want == "[]" && got == "[]") {
			t.Errorf("%v: %s, want %s", c.spec, got, c.want)
		}
	}
}

// A chapter's index has all its sections' pages, in order, in shards
// starting with letters, read back whole, kept by GC, and followed by
// import.
func TestChapterIndex(t *testing.T) {
	defer func(f, m int) { shardFirst, shardMore = f, m }(shardFirst, shardMore)
	shardFirst, shardMore = 600, 1200
	s, _ := Open(t.TempDir())
	pkg := Package{Distro: "debian", Release: "trixie", Name: "p", Version: "1"}
	for i := 0; i < 40; i++ {
		addVersion(t, s, fmt.Sprintf("page%02d", i), "1", "a page", "girly", pkg, int64(i))
	}
	addVersion(t, s, "apple", "1", "a fruit", "", pkg, 1)
	addVersion(t, s, "pear", "1ssl", "another section", "", Package{Distro: "arch", Release: "rolling", Name: "q"}, 1)
	addVersion(t, s, "lonely", "5", "a short chapter", "", pkg, 1)
	publish(t, s)
	m, _ := s.Manifest()
	if len(m.Chapters) != 2 || m.Chapters[0].Chapter != "1" || m.Chapters[0].Pages != 42 || len(m.Sections) != 3 {
		t.Fatalf("manifest: %+v, %+v", m.Chapters, m.Sections)
	}
	var ci ChapterIndex
	if err := s.Read(SectionIndexPath("1", m.Chapters[0].SHA256), &ci); err != nil {
		t.Fatal(err)
	}
	// A is too short to end the first shard (under half of it), so P
	// starts in it too.
	if ci.Count != 42 || len(ci.Shards) < 2 || fmt.Sprint(ci.Letters) != "[{A 1} {P 1}]" {
		t.Fatalf("chapter 1's index: count %d, %d inline, %d shards, letters %v", ci.Count, len(ci.Pages), len(ci.Shards), ci.Letters)
	}
	if ci.Pages[0].Name != "apple" || ci.Shards[0].Prev != "P" {
		t.Errorf("first shard %+v, then %+v", ci.Pages, ci.Shards[0])
	}
	n := len(ci.Pages)
	for i, r := range ci.Shards {
		n += r.Count
		if r.Bytes == 0 || (i > 0 && ci.Shards[i-1].Last >= r.First) {
			t.Errorf("shard %d: %+v", i, r)
		}
	}
	pages, err := s.ChapterPages(m.Chapters[0])
	if err != nil || len(pages) != 42 || n != 42 {
		t.Fatalf("pages: %d (shards say %d), %v", len(pages), n, err)
	}
	var names []string
	for _, p := range pages {
		names = append(names, p.Name+"("+p.Section+")")
	}
	if names[0] != "apple(1)" || names[40] != "page39(1)" || names[41] != "pear(1ssl)" {
		t.Errorf("order: %v", names)
	}
	p0 := pages[1]
	if p0.Name != "page00" || fmt.Sprint(p0.Sources) != "[debian debian/trixie]" || fmt.Sprint(p0.Girl) != "[debian debian/trixie]" ||
		fmt.Sprint(p0.Variants) != "[girl man]" || p0.Page == "" {
		t.Errorf("page00: %+v", p0)
	}
	if pear := pages[41]; fmt.Sprint(pear.Sources) != "[arch arch/rolling]" || pear.Girl != nil {
		t.Errorf("pear: %+v", pear)
	}
	if short, _ := s.ChapterPages(m.Chapters[1]); len(short) != 1 {
		t.Errorf("chapter 5 should be one page: %v", short)
	}
	if _, err := s.GC(false); err != nil {
		t.Fatal(err)
	}
	if again, err := s.ChapterPages(m.Chapters[0]); err != nil || len(again) != 42 {
		t.Fatalf("after GC: %d pages, %v", len(again), err)
	}
	dst, _ := Open(t.TempDir())
	st, err := dst.Import(context.Background(), NewClient(s.Dir, 5*time.Second), ImportOptions{Jobs: 2})
	if err != nil || st.Pages != 43 {
		t.Fatalf("import: %+v, %v", st, err)
	}
	// Just one section of a chapter.
	dst2, _ := Open(t.TempDir())
	st, err = dst2.Import(context.Background(), NewClient(s.Dir, 5*time.Second), ImportOptions{Jobs: 2, Sections: []string{"1ssl"}})
	if err != nil || st.Pages != 1 {
		t.Fatalf("import of 1ssl: %+v, %v", st, err)
	}
}

func TestBase91(t *testing.T) {
	for _, s := range []string{"", "a", "girlpages", "\x00\x00\x00", "\xff\xfe\xfd\xfc\xfb"} {
		e := Encode91([]byte(s))
		if strings.ContainsAny(e, "\"\\") {
			t.Errorf("%q encodes with characters JSON escapes: %q", s, e)
		}
		if d, err := Decode91(e); err != nil || string(d) != s {
			t.Errorf("%q → %q → %q, %v", s, e, d, err)
		}
	}
	h := Hash([]byte("x"))
	e := hash91(h)
	if back, err := unhash91(e); err != nil || back != h || len(e) > 40 {
		t.Errorf("hash %s → %s (%d) → %s, %v", h, e, len(e), back, err)
	}
}

// Pages keep their ids across publishes, new ones going on the end, so
// the page table's shards that don't change keep their hashes.
func TestStableIDs(t *testing.T) {
	s, _ := Open(t.TempDir())
	pkg := Package{Distro: "debian", Release: "trixie", Name: "p", Version: "1"}
	for _, n := range []string{"b", "d", "f"} {
		addVersion(t, s, n, "1", "a page", "girly", pkg, 1)
	}
	publish(t, s)
	ids := func() map[string]int {
		m, _ := s.Manifest()
		var nm TableManifest
		s.Read(IndexManifestPath("names", m.Index.Names), &nm)
		out := map[string]int{}
		for _, r := range nm.Shards {
			var sh NameShard
			s.Read(ShardPath("names", r.SHA256), &sh)
			for i, n := range sh.Names {
				if n.Name != "" {
					out[n.Name] = sh.First + i
				}
			}
		}
		return out
	}
	before := ids()
	addVersion(t, s, "a", "1", "a new page", "girly", pkg, 2)
	publish(t, s)
	after := ids()
	for n, id := range before {
		if after[n] != id {
			t.Errorf("%s was %d, now %d", n, id, after[n])
		}
	}
	if after["a"] != 3 {
		t.Errorf("the new page should go on the end: %v", after)
	}
	// It's found, by its new id.
	sr := &Searcher{Client: NewClient(s.Dir, time.Second)}
	if hits, err := sr.Search(context.Background(), "new", 5); err != nil || len(hits) != 1 || hits[0].Name != "a" {
		t.Errorf("search: %+v, %v", hits, err)
	}
}
