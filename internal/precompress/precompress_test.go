// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package precompress

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSweep(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat(`{"girl":"slay","bestie":"periodt"},`, 200)
	files := map[string]string{
		"index.html":         "<p>" + big + "</p>",
		"repo/a.json":        big,
		"repo/tiny.json":     "{}",
		"static/icon.png":    big, // not worth it: already compressed formats
		"static/app.js":      "const x = " + big,
		"repo/.tmp-abc.json": big,
	}
	for name, data := range files {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(data), 0o644)
	}
	st, err := Sweep(root, Encodings)
	if err != nil {
		t.Fatal(err)
	}
	if st.Compressed != 4 || st.Saved <= 0 { // tiny.json is read but too small
		t.Errorf("first sweep: %+v", st)
	}
	for _, name := range []string{"index.html", "repo/a.json", "static/app.js"} {
		for _, e := range Encodings {
			b, err := os.ReadFile(filepath.Join(root, name+e.Ext))
			if err != nil {
				t.Fatalf("%s%s: %v", name, e.Ext, err)
			}
			r, err := e.Reader(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(r)
			if string(got) != files[name] {
				t.Errorf("%s%s doesn't decompress to the original", name, e.Ext)
			}
		}
	}
	for _, name := range []string{"repo/tiny.json.br", "static/icon.png.br", "repo/.tmp-abc.json.br"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Errorf("%s shouldn't exist", name)
		}
	}

	// Nothing changed: nothing to do (tiny.json is looked at again, as it
	// never gets copies, but that's cheap).
	st, _ = Sweep(root, Encodings)
	if st.Compressed != 1 || st.Removed != 0 {
		t.Errorf("second sweep: %+v", st)
	}

	// A changed file is compressed again; a removed one's copies go; and
	// copies in a format no longer wanted go too.
	later := time.Now().Add(2 * time.Second)
	os.WriteFile(filepath.Join(root, "repo/a.json"), []byte(big+big), 0o644)
	os.Chtimes(filepath.Join(root, "repo/a.json"), later, later)
	os.Remove(filepath.Join(root, "static/app.js"))
	st, err = Sweep(root, Encodings[:2]) // no gzip
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "static/app.js.br")); err == nil {
		t.Error("copies of a removed file should go")
	}
	if _, err := os.Stat(filepath.Join(root, "index.html.gz")); err == nil {
		t.Error("gzip copies should go when gzip isn't wanted")
	}
	b, _ := os.ReadFile(filepath.Join(root, "repo/a.json.br"))
	r, _ := Encodings[1].Reader(bytes.NewReader(b))
	if got, _ := io.ReadAll(r); string(got) != big+big {
		t.Error("a changed file's copies weren't refreshed")
	}
	if st.Removed != 5 { // app.js ×3, index.html.gz, a.json.gz
		t.Errorf("third sweep removed %d", st.Removed)
	}
}

func TestParse(t *testing.T) {
	es, err := Parse("zstd, brotli,gz")
	if err != nil || len(es) != 3 || es[1].Name != "br" {
		t.Errorf("%v, %v", es, err)
	}
	if es, err := Parse("none"); err != nil || len(es) != 0 {
		t.Errorf("none: %v, %v", es, err)
	}
	if _, err := Parse("lzma"); err == nil {
		t.Error("unknown encodings should be refused")
	}
	if base, ok := IsCopy("repo/x.json.zst"); !ok || base != "repo/x.json" {
		t.Error("IsCopy")
	}
	if _, ok := IsCopy("repo/x.tar.gz"); ok {
		t.Error("a .tar.gz isn't a copy of anything here")
	}
}
