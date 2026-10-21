// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A listing whose files went missing (a stopped merge, a file system that
// lost what it was writing) doesn't stop publishing, and merging again
// fetches the files.
func TestMissingContents(t *testing.T) {
	ctx := context.Background()
	src := testStore(t)
	dst, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dst.ImportFrom(ctx, src.Dir, nil, ImportOptions{Jobs: 4}); err != nil {
		t.Fatal(err)
	}
	ls, _, _ := dst.Listing("ls", "1")
	grep, _, _ := dst.Listing("grep", "1")
	lsSrc, grepSrc := ls.CurrentVersion().Source, grep.CurrentVersion().Source
	// ls loses its variants.json; grep keeps it but loses its man file.
	if err := os.Remove(dst.abs(ContentsPath(lsSrc))); err != nil {
		t.Fatal(err)
	}
	grepMan := grep.CurrentVersion().Variants[VariantMan].SHA256
	if err := os.Remove(dst.abs(VariantPath(grepSrc, VariantMan, grepMan))); err != nil {
		t.Fatal(err)
	}

	var warned []string
	dst.Warn = func(msg string) { warned = append(warned, msg) }
	pages, _ := dst.AllPages()
	m, err := dst.Publish(pages)
	if err != nil {
		t.Fatalf("publish with files missing: %v", err)
	}
	if m.Pages != len(pages) || len(warned) != 1 || !strings.Contains(warned[0], "2 versions' files are missing") ||
		!strings.Contains(warned[0], "ls(1)") || !strings.Contains(warned[0], "grep(1)") {
		t.Fatalf("pages %d of %d, warned %q", m.Pages, len(pages), warned)
	}

	// Nothing's changed in the tree, but merging it all again finds the
	// listings' files aren't here, and fetches them.
	st, err := dst.ImportFrom(ctx, src.Dir, nil, ImportOptions{Jobs: 4})
	if err != nil || st.Failed != 0 || st.Same != len(pages)-2 {
		t.Fatalf("merging again: %+v, %v", st, err)
	}
	for _, c := range []struct{ name, source string }{{"ls", lsSrc}, {"grep", grepSrc}} {
		if _, err := dst.Variant(c.source, VariantMan); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	warned = nil
	if _, err := dst.Publish(pages); err != nil || len(warned) != 0 {
		t.Fatalf("publish after: %v, %q", err, warned)
	}
	// And now it's all here, it's all the same.
	if st, _ := dst.ImportFrom(ctx, src.Dir, nil, ImportOptions{Jobs: 4}); st.Same != len(pages) {
		t.Errorf("third merge: %+v", st)
	}
}
