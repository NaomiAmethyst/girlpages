// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestKey(t *testing.T) {
	h := strings.Repeat("0123456789abcdef", 4)
	for _, p := range []string{
		"", "index.html", "repo/contents/ab/" + h + "/man-" + h + ".json",
		h, h + h, "x" + strings.Repeat("a", 63), "x" + strings.Repeat("a", 65), strings.ToUpper(h),
	} {
		k := key(nil, p)
		if got := string(unkey(nil, k)); got != p {
			t.Errorf("%q: back as %q", p, got)
		}
		if strings.Count(p, h) == 1 && len(k) != len(p)-64+33 {
			t.Errorf("%q: key is %d bytes", p, len(k))
		}
	}
	if k := key(nil, h+h); len(k) != 128 {
		t.Errorf("a 128-digit run was packed: %d bytes", len(k))
	}
}

// A table finds what its index does, and nothing it doesn't.
func TestTable(t *testing.T) {
	h := func(i int) string { return fmt.Sprintf("%064x", i*7919) }
	var paths []string
	for i := 0; i < 500; i++ {
		paths = append(paths, fmt.Sprintf("repo/contents/%s/%s/man-%s.json", h(i)[:2], h(i), h(i+1)))
		paths = append(paths, fmt.Sprintf("man/%d/page%03d/index.html", i%9, i))
	}
	paths = append(paths, "index.html", "404.html", "repo/manifest.json", "repo/contents/zz", "repo/contents/00")
	sort.Strings(paths)
	es := make([]Entry, len(paths))
	for i, p := range paths {
		es[i] = Entry{Path: p, Blobs: emptyBlobs()}
		es[i].Blobs[0] = Blob{Seg: 0, Off: int64(8 + 30*i), Len: 10}
		if i%2 == 0 {
			es[i].Blobs[2] = Blob{Seg: 0, Off: int64(18 + 30*i), Len: 5} // follows it
			es[i].Blobs[3] = Blob{Seg: 1, Off: 8, Len: int64(i)}         // elsewhere
		}
	}
	dir := t.TempDir()
	sha, err := WriteIndex(dir, Header{Segments: []Segment{{SHA256: "a", Bytes: int64(8 + 30*len(paths))}, {SHA256: "b", Bytes: 8 + int64(len(paths))}}}, es)
	if err != nil {
		t.Fatal(err)
	}
	compare(t, filepath.Join(dir, IndexName(sha)))
}

// compare checks a table of an index finds every entry the index has,
// with the same blobs, and not paths between them.
func compare(t *testing.T, file string) {
	t.Helper()
	ix, err := ReadIndex(file)
	if err != nil {
		t.Fatal(err)
	}
	_, tb, err := loadTable(file)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	ix.Each(func(e Entry) bool {
		n++
		b, ok := tb.lookup(e.Path)
		if !ok || b != e.Blobs {
			t.Fatalf("%s: %v %+v, want %+v", e.Path, ok, b, e.Blobs)
		}
		for _, miss := range []string{e.Path + "x", e.Path[:len(e.Path)-1], e.Path + "/"} {
			if _, inIndex := ix.Lookup(miss); !inIndex {
				if _, ok := tb.lookup(miss); ok {
					t.Fatalf("found %s, which isn't there", miss)
				}
			}
		}
		return true
	})
	if n != ix.Entries {
		t.Fatalf("%d entries, not %d", n, ix.Entries)
	}
	if _, ok := tb.lookup(""); ok {
		t.Error("found the empty path")
	}
	t.Logf("%d entries, table %d bytes (%.1f a file)", n, len(tb.data), float64(len(tb.data))/float64(n))
}

// (On a real archive, if there's one: ARCHIVE=dir.)
func TestTableOnArchive(t *testing.T) {
	dir := os.Getenv("ARCHIVE")
	if dir == "" {
		t.Skip("ARCHIVE isn't set")
	}
	c, err := ReadCurrentPointer(dir)
	if err != nil {
		t.Fatal(err)
	}
	compare(t, filepath.Join(dir, c.Index))
}
