// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package build

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/archive"
	"github.com/NaomiAmethyst/girlpages/internal/precompress"
)

func write(t *testing.T, root, rel, s string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, s *archive.Snapshot, path, encoding string) string {
	t.Helper()
	e, ok := s.Lookup(path)
	if !ok {
		t.Fatalf("%s isn't in the archive", path)
	}
	r, _, err := s.Open(e, encoding)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if encoding != "" {
		dr, err := precompress.NewReader(encoding, bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if b, err = io.ReadAll(dr); err != nil {
			t.Fatal(err)
		}
	}
	return string(b)
}

func TestWrite(t *testing.T) {
	ctx := context.Background()
	site, dir := t.TempDir(), filepath.Join(t.TempDir(), "archive")
	page := "<!doctype html><title>ls</title>" + strings.Repeat("<p>list directory contents</p>", 50)
	write(t, site, "index.html", page)
	write(t, site, "index.html.br", "a stale copy the archive makes its own")
	write(t, site, "man/1/ls/index.html", page) // the same as index.html: stored once
	write(t, site, "repo/manifest.json", `{"format":1}`)
	write(t, site, "repo/packs/"+strings.Repeat("a", 64)+".tar.zst", "zstd bytes, not a copy of anything")
	write(t, site, "repo/.lock", "")
	write(t, site, ".publish.lock", "")

	clock := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	st, err := Write(ctx, site, dir, Options{Jobs: 3, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 4 || st.New != 3 || st.Segments != 1 || st.Index == "" {
		t.Fatalf("first: %+v", st)
	}
	a, err := archive.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := a.Current()
	if s.Index.Entries != 4 || s.Index.Generated != clock {
		t.Fatalf("index: %+v", s.Index.Header)
	}
	for _, enc := range []string{"", "zstd", "br", "gzip"} {
		if got := read(t, s, "man/1/ls/index.html", enc); got != page {
			t.Errorf("%q: %.40q", enc, got)
		}
	}
	e1, _ := s.Lookup("index.html")
	e2, _ := s.Lookup("man/1/ls/index.html")
	if e1.Blobs != e2.Blobs {
		t.Error("the same content was stored twice")
	}
	if e, _ := s.Lookup("repo/manifest.json"); e.Has("br") {
		t.Error("a file too small to be worth it was compressed")
	}
	if e, _ := s.Lookup("repo/packs/" + strings.Repeat("a", 64) + ".tar.zst"); e.Has("zstd") || !e.Has("") {
		t.Errorf("the pack: %+v", e.Blobs)
	}
	for _, p := range []string{"index.html.br", "repo/.lock", ".publish.lock", "nope"} {
		if _, ok := s.Lookup(p); ok {
			t.Errorf("%s is in the archive", p)
		}
	}

	// One file changes: only it's added, and serving processes see it.
	clock = clock.Add(time.Minute)
	write(t, site, "repo/manifest.json", `{"format":1,"pages":2}`)
	st, err = Write(ctx, site, dir, Options{Jobs: 3, Now: now, CompactBelow: -1})
	if err != nil {
		t.Fatal(err)
	}
	if st.New != 1 || st.NewBytes != int64(len(`{"format":1,"pages":2}`)) || st.Segments != 2 {
		t.Fatalf("second: %+v", st)
	}
	if changed, err := a.Reload(); err != nil || !changed {
		t.Fatalf("reload: %v, %v", changed, err)
	}
	s = a.Current()
	if got := read(t, s, "repo/manifest.json", ""); got != `{"format":1,"pages":2}` {
		t.Errorf("after: %q", got)
	}
	if read(t, s, "index.html", "br") != page {
		t.Error("an unchanged file changed")
	}
	if changed, err := a.Reload(); err != nil || changed {
		t.Errorf("a reload with nothing new: %v, %v", changed, err)
	}

	// Compacting puts it all in one segment again; an hour on, what the
	// old indexes used goes.
	clock = clock.Add(time.Minute)
	st, err = Write(ctx, site, dir, Options{Now: now, Compact: true, Keep: 1})
	if err != nil || !st.Compacted || st.Segments != 1 {
		t.Fatalf("compact: %+v, %v", st, err)
	}
	// (Files' times are the real clock's: collecting goes by those.)
	clock = time.Now().Add(2 * time.Hour)
	st, err = Write(ctx, site, dir, Options{Now: now, Keep: 1})
	if err != nil {
		t.Fatal(err)
	}
	segs, _ := os.ReadDir(filepath.Join(dir, archive.SegmentDir))
	idxs, _ := filepath.Glob(filepath.Join(dir, "index-*.idx"))
	// Everything's "old" now: only the current index and its segment stay.
	if len(segs) != 1 || len(idxs) != 1 || st.Removed < 3 {
		t.Errorf("after collecting: %d segments, %d indexes, removed %d", len(segs), len(idxs), st.Removed)
	}
	if _, err := a.Reload(); err != nil {
		t.Fatal(err)
	}
	if read(t, a.Current(), "index.html", "gzip") != page {
		t.Error("after compacting")
	}
}

func TestWriteRefusesArchiveInsideSite(t *testing.T) {
	site := t.TempDir()
	if _, err := Write(context.Background(), site, filepath.Join(site, "archive"), Options{}); err == nil {
		t.Error("wrote an archive inside its site")
	}
}
