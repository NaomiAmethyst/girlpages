// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package build

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NaomiAmethyst/girlpages/internal/archive"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// addPage adds a page with one version to a repository.
func addPage(t *testing.T, s *repo.Store, name string) {
	t.Helper()
	src := repo.Hash([]byte(name))
	man := repo.ManVariant(src, strings.ToUpper(name)+"(1)\n\nNAME\n       "+name+" - does "+name+" things\n")
	info, err := s.PutVariant(man)
	if err != nil {
		t.Fatal(err)
	}
	err = s.UpdateListing(name, "1", func(l *repo.Listing) error {
		l.Versions = append(l.Versions, repo.Version{Source: src, Title: man.Title,
			Variants: map[string]repo.VariantInfo{repo.VariantMan: info}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Replaying packs into a stage gives what unpacking them would: every
// file of the repository they came from, but those they delete, and the
// packs and their log besides.
func TestPutPacks(t *testing.T) {
	ctx := context.Background()
	src, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ls", "cp", "mv"} {
		addPage(t, src, n)
	}
	packs := t.TempDir()
	if _, err := repo.AppendPacks(ctx, packs, src.Dir, repo.PackOptions{}); err != nil {
		t.Fatal(err)
	}
	// mv goes (its pointer file: a delete in the next pack), and ls
	// gets a second version (a put replacing the first pack's).
	if err := os.Remove(filepath.Join(src.Dir, filepath.FromSlash(repo.NamePath("mv", "1")))); err != nil {
		t.Fatal(err)
	}
	addPage(t, src, "rm")
	if err := src.UpdateListing("ls", "1", func(x *repo.Listing) error {
		x.Versions[0].Title = "LS(1) again"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AppendPacks(ctx, packs, src.Dir, repo.PackOptions{}); err != nil {
		t.Fatal(err)
	}
	log, _ := repo.ReadPackLog(packs)
	if len(log.Packs) < 2 {
		t.Fatalf("%d packs", len(log.Packs))
	}

	dir := filepath.Join(t.TempDir(), "archive")
	st, err := NewStage(dir, Options{Jobs: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Abort()
	got, err := st.PutPacks(ctx, filepath.Join(packs, "packs"), "repo/")
	if err != nil || len(got) != len(log.Packs) {
		t.Fatalf("put %d packs, %v", len(got), err)
	}
	// Every repository file is there, as it is.
	want := map[string]bool{}
	filepath.WalkDir(src.Dir, func(p string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(src.Dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() || !(strings.HasPrefix(rel, "page/") || strings.HasPrefix(rel, "contents/")) || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		want["repo/"+rel] = true
		b, _ := os.ReadFile(p)
		if g, err := st.Get("repo/" + rel); err != nil || !bytes.Equal(g, b) {
			t.Errorf("%s: %v", rel, err)
		}
		return nil
	})
	for _, p := range st.Paths() {
		if strings.HasPrefix(p, "repo/page/") || strings.HasPrefix(p, "repo/contents/") {
			if !want[p] {
				t.Errorf("%s is staged, but not in the repository", p)
			}
		}
	}
	if st.Has("repo/" + repo.NamePath("mv", "1")) {
		t.Error("mv's pointer wasn't deleted")
	}
	for _, e := range log.Packs {
		if !st.Has("repo/" + e.Path) {
			t.Errorf("pack %s isn't staged", e.Path)
		}
	}
	if !st.Has("repo/packs/index.json") {
		t.Error("the pack log isn't staged")
	}

	// A store over the stage reads it as a repository.
	s := &repo.Store{Dir: "/nowhere", Files: st.Files("repo/")}
	pages, err := s.AllPages()
	if err != nil || len(pages) != 3 {
		t.Fatalf("%d pages, %v", len(pages), err)
	}
	if l, _, err := s.Listing("ls", "1"); err != nil || l.CurrentVersion().Title != "LS(1) again" {
		t.Fatalf("ls: %+v, %v", l, err)
	}
	m, err := s.Publish(pages)
	if err != nil || m.Pages != 3 || len(m.Packs) != len(log.Packs) {
		t.Fatalf("publish: %+v, %v", m, err)
	}
	if _, err := st.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := archive.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, ok := a.Current().Lookup("repo/manifest.json"); !ok {
		t.Error("the archive has no manifest")
	}
}
