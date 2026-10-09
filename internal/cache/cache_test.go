// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	src := filepath.Join(t.TempDir(), "ls.1")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	user, sys := Dir(t.TempDir()), Dir(t.TempDir())
	set := Set{user, sys}

	if m, e, _ := set.Lookup(src); m != "" || e != nil {
		t.Fatal("empty cache returned something")
	}

	h := Hash("plain text")
	if err := sys.PutObject(h, "# girly"); err != nil {
		t.Fatal(err)
	}
	e := &Entry{Source: src, Name: "ls", Section: "1", Hash: h}
	if err := e.Stamp(); err != nil {
		t.Fatal(err)
	}
	if err := sys.PutEntry(e); err != nil {
		t.Fatal(err)
	}
	m, got, fresh := set.Lookup(src)
	if m != "# girly" || !fresh || got.Name != "ls" {
		t.Fatalf("lookup = %q %+v %v", m, got, fresh)
	}
	fi, err := os.Stat(sys.objectPath(h))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("object mode = %v, %v", fi.Mode(), err)
	}

	// Change the source: the entry goes stale but is still returned.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(src, future, future); err != nil {
		t.Fatal(err)
	}
	if m, _, fresh := set.Lookup(src); m != "# girly" || fresh {
		t.Errorf("stale lookup = %q %v", m, fresh)
	}

	// A fresh entry in a lower-priority cache beats a stale one above it.
	h2 := Hash("newer text")
	if err := user.PutObject(h2, "# old"); err != nil {
		t.Fatal(err)
	}
	if err := user.PutEntry(&Entry{Source: src, Hash: h2, Size: 1, ModTime: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.Stamp(); err != nil {
		t.Fatal(err)
	}
	if err := sys.PutEntry(e); err != nil {
		t.Fatal(err)
	}
	if m, _, fresh := set.Lookup(src); m != "# girly" || !fresh {
		t.Errorf("lookup = %q %v", m, fresh)
	}
}

func TestGC(t *testing.T) {
	d := Dir(t.TempDir())
	live, dead := Hash("a"), Hash("b")
	for _, h := range []string{live, dead} {
		if err := d.PutObject(h, h); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.PutEntry(&Entry{Source: "/x/ls.1", Hash: live}); err != nil {
		t.Fatal(err)
	}
	n, err := d.GC()
	if err != nil || n != 1 {
		t.Fatalf("GC = %d, %v", n, err)
	}
	if !d.HasObject(live) || d.HasObject(dead) {
		t.Error("GC removed the wrong thing")
	}
	if err := d.RemoveEntry("/x/ls.1"); err != nil {
		t.Fatal(err)
	}
	count := 0
	_ = d.Entries(func(*Entry) error { count++; return nil })
	if count != 0 {
		t.Errorf("%d entries left", count)
	}
}
