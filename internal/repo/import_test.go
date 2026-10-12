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

// A long section's index is split into shards, which are read back whole,
// kept by GC, and followed by import.
func TestSectionShards(t *testing.T) {
	defer func(n int) { sectionShardBytes = n }(sectionShardBytes)
	sectionShardBytes = 1500
	s, _ := Open(t.TempDir())
	pkg := Package{Distro: "debian", Release: "trixie", Name: "p", Version: "1"}
	for i := 0; i < 40; i++ {
		addVersion(t, s, fmt.Sprintf("page%02d", i), "1", "a page", "girly", pkg, int64(i))
	}
	addVersion(t, s, "lonely", "5", "a short section", "", pkg, 1)
	publish(t, s)
	m, _ := s.Manifest()
	var si SectionIndex
	if err := s.Read(SectionIndexPath("1", m.Sections[0].SHA256), &si); err != nil {
		t.Fatal(err)
	}
	if len(si.Shards) < 3 || len(si.Pages) != 0 || si.Count != 40 {
		t.Fatalf("section 1's index: %d shards, %d pages inline, count %d", len(si.Shards), len(si.Pages), si.Count)
	}
	n := 0
	for i, r := range si.Shards {
		n += r.Count
		if r.Bytes == 0 || (i > 0 && si.Shards[i-1].Last >= r.First) {
			t.Errorf("shard %d: %+v", i, r)
		}
	}
	pages, err := s.SectionPages(m.Sections[0])
	if err != nil || len(pages) != 40 || n != 40 || pages[0].Name != "page00" || pages[39].Name != "page39" {
		t.Fatalf("pages: %d (shards say %d), %v", len(pages), n, err)
	}
	if short, _ := s.SectionPages(m.Sections[1]); len(short) != 1 {
		t.Errorf("section 5 should stay whole: %v", short)
	}
	if _, err := s.GC(false); err != nil {
		t.Fatal(err)
	}
	if again, err := s.SectionPages(m.Sections[0]); err != nil || len(again) != 40 {
		t.Fatalf("after GC: %d pages, %v", len(again), err)
	}
	dst, _ := Open(t.TempDir())
	st, err := dst.Import(context.Background(), NewClient(s.Dir, 5*time.Second), ImportOptions{Jobs: 2})
	if err != nil || st.Pages != 41 {
		t.Fatalf("import: %+v, %v", st, err)
	}
}
