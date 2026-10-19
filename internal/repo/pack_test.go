// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// packedStore is a store with n pages, each a version from debian with a
// girl variant.
func packedStore(t *testing.T, n int) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		addVersion(t, s, fmt.Sprintf("tool%03d", i), "1", "does tool things "+strings.Repeat("and more ", i%7),
			"slay "+strings.Repeat("bestie ", i%11), Package{Distro: "debian", Release: "trixie", Name: "tools", Version: "1"}, int64(i))
	}
	return s
}

func appendPacks(t *testing.T, root, dir string, limit int64) PackStats {
	t.Helper()
	st, err := AppendPacks(context.Background(), root, dir, PackOptions{Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// replayAll replays a log's packs into a fresh directory, returning it.
func replayAll(t *testing.T, root string) string {
	t.Helper()
	log, err := ReadPackLog(root)
	if err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	for _, e := range log.Packs {
		if _, _, err := applyPack(context.Background(), filepath.Join(root, e.Path), tree); err != nil {
			t.Fatal(err)
		}
	}
	return tree
}

func scan(t *testing.T, dir string) State {
	t.Helper()
	st, err := ScanState(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// checkChain checks a log's packs are numbered in order, each naming the
// one before.
func checkChain(t *testing.T, root string) *PackLog {
	t.Helper()
	log, err := ReadPackLog(root)
	if err != nil {
		t.Fatal(err)
	}
	prev := ""
	for i, e := range log.Packs {
		h, err := ReadPackHeader(filepath.Join(root, e.Path))
		if err != nil {
			t.Fatal(err)
		}
		if h.Seq != i+1 || e.Seq != i+1 || h.Prev != prev {
			t.Errorf("pack %d: seq %d/%d, prev %.8s, want %d after %.8s", i, h.Seq, e.Seq, h.Prev, i+1, prev)
		}
		if i < len(log.Packs)-1 && e.Tail && !log.Packs[i+1].Tail {
			t.Errorf("pack %d is a tail before a full pack", i)
		}
		prev = e.SHA256
	}
	return log
}

func TestPacks(t *testing.T) {
	s := packedStore(t, 60)
	// Small packs, so there are several.
	st := appendPacks(t, s.Dir, s.Dir, 4<<10)
	if len(st.Packs) < 2 || st.Deletes != 0 {
		t.Fatalf("first packs: %+v", st)
	}
	log := checkChain(t, s.Dir)
	if !log.Packs[len(log.Packs)-1].Tail || log.Packs[0].Tail {
		t.Errorf("first packs should be full, then a tail: %+v", log.Packs)
	}
	if !reflect.DeepEqual(scan(t, replayAll(t, s.Dir)), scan(t, s.Dir)) {
		t.Fatal("replaying the packs doesn't give the repository")
	}
	// Nothing new, no new pack: with the log's state cached, without it
	// (read from the packs), and with it damaged.
	if st := appendPacks(t, s.Dir, s.Dir, 4<<10); len(st.Packs) != 0 {
		t.Errorf("a pack of nothing: %+v", st)
	}
	caches, _ := filepath.Glob(filepath.Join(s.Dir, "packs", ".state-*"))
	if len(caches) != 1 {
		t.Fatalf("state caches: %v", caches)
	}
	os.Remove(caches[0])
	if st := appendPacks(t, s.Dir, s.Dir, 4<<10); len(st.Packs) != 0 {
		t.Errorf("a pack of nothing, without the cache: %+v", st)
	}
	os.WriteFile(caches[0], []byte("not zstd"), 0o644)
	if st := appendPacks(t, s.Dir, s.Dir, 4<<10); len(st.Packs) != 0 {
		t.Errorf("a pack of nothing, with the cache damaged: %+v", st)
	}
	// The manifest lists them.
	publish(t, s)
	m, err := s.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Packs, log.Packs) {
		t.Errorf("manifest packs %+v, want %+v", m.Packs, log.Packs)
	}

	// Changes: a new page, a changed one, and one gone (as pruning goes).
	addVersion(t, s, "newtool", "8", "new", "girly", Package{Distro: "debian", Release: "trixie"}, 99)
	addVersion(t, s, "tool001", "1", "does tool things again", "", Package{Distro: "ubuntu", Release: "noble"}, 100)
	if err := os.Remove(filepath.Join(s.Dir, NamePath("tool002", "1"))); err != nil {
		t.Fatal(err)
	}
	st = appendPacks(t, s.Dir, s.Dir, 1<<20)
	if len(st.Packs) != 1 || !st.Packs[0].Tail || st.Deletes != 1 || st.Puts == 0 {
		t.Fatalf("tail: %+v", st)
	}
	checkChain(t, s.Dir)
	if !reflect.DeepEqual(scan(t, replayAll(t, s.Dir)), scan(t, s.Dir)) {
		t.Fatal("replaying with the tail doesn't give the repository")
	}
}

func TestPackConsolidation(t *testing.T) {
	s := packedStore(t, 20)
	appendPacks(t, s.Dir, s.Dir, 6<<10)
	before := checkChain(t, s.Dir)
	var replaced []PackEntry
	for i := 0; i < 40 && len(replaced) == 0; i++ {
		addVersion(t, s, fmt.Sprintf("more%03d", i), "1", "more things "+strings.Repeat("yes ", i), "slay", Package{Distro: "arch"}, int64(200+i))
		st := appendPacks(t, s.Dir, s.Dir, 6<<10)
		replaced = st.Replaced
	}
	if len(replaced) == 0 {
		t.Fatal("tail packs were never replaced")
	}
	log := checkChain(t, s.Dir)
	// Full packs stay as they were.
	for _, e := range before.Packs {
		if !e.Tail && log.Packs[e.Seq-1].SHA256 != e.SHA256 {
			t.Errorf("full pack %d changed", e.Seq)
		}
	}
	if len(log.Retired) == 0 {
		t.Error("replaced packs aren't kept a while")
	}
	for _, r := range log.Retired {
		if _, err := os.Stat(filepath.Join(s.Dir, r.Path)); err != nil {
			t.Errorf("retired pack gone already: %v", err)
		}
	}
	// The first replacement takes the place of the first pack replaced.
	first, err := ReadPackHeader(filepath.Join(s.Dir, log.Packs[replaced[0].Seq-1].Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Replaces) != len(replaced) {
		t.Errorf("the replacement says it replaces %d packs, not %d", len(first.Replaces), len(replaced))
	}
	if !reflect.DeepEqual(scan(t, replayAll(t, s.Dir)), scan(t, s.Dir)) {
		t.Fatal("replaying after replacing doesn't give the repository")
	}
}

func TestSyncPacks(t *testing.T) {
	ctx := context.Background()
	a := packedStore(t, 30)
	appendPacks(t, a.Dir, a.Dir, 8<<10)
	publish(t, a)
	c := NewClient(a.Dir, 0)

	b, _ := Open(t.TempDir())
	b.SyncRoot = t.TempDir()
	sync := func() SyncStats {
		t.Helper()
		m, err := c.Manifest(ctx)
		if err != nil {
			t.Fatal(err)
		}
		st, err := b.SyncFrom(ctx, c, m, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.ImportFrom(ctx, filepath.Join(st.Dir, "tree"), st.Changed, ImportOptions{Jobs: 4}); err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := sync()
	if st.Changed != nil || len(st.Applied) == 0 {
		t.Fatalf("first sync: %+v", st)
	}
	if l := listing(t, b, "tool007", "1"); len(l.Versions) != 1 {
		t.Errorf("tool007 after syncing: %+v", l)
	}
	// The sync directory is a pack-only repository of its own.
	if bm, err := NewClient(st.Dir, 0).Manifest(ctx); err != nil || len(bm.Packs) != len(st.Applied) {
		t.Errorf("sync directory's manifest: %+v, %v", bm, err)
	}

	// Again: just what's new.
	addVersion(t, a, "fresh", "1", "fresh", "new", Package{Distro: "debian"}, 300)
	appendPacks(t, a.Dir, a.Dir, 8<<10)
	publish(t, a)
	st = sync()
	if len(st.Applied) != 1 || !st.Changed[NamePath("fresh", "1")] || len(st.Changed) != 1 {
		t.Errorf("second sync: applied %d, changed %v", len(st.Applied), st.Changed)
	}
	listing(t, b, "fresh", "1")
	if st := sync(); len(st.Applied) != 0 {
		t.Errorf("a sync with nothing new applied %d packs", len(st.Applied))
	}

	// The tail it applied is replaced: it goes on, from the replacement.
	for i := 0; i < 40; i++ {
		addVersion(t, a, fmt.Sprintf("late%03d", i), "1", "late "+strings.Repeat("ok ", i), "slay", Package{Distro: "arch"}, int64(400+i))
		if st := appendPacks(t, a.Dir, a.Dir, 8<<10); len(st.Replaced) > 0 {
			break
		}
	}
	publish(t, a)
	sync()
	listing(t, b, "late000", "1")
	if !reflect.DeepEqual(scan(t, filepath.Join(st.Dir, "tree")), scan(t, a.Dir)) {
		t.Error("the synced tree isn't the repository")
	}

	// An empty repository with the same sync directory (emptied since its
	// last sync, say) gets everything, though the packs are up to date.
	fresh, _ := Open(t.TempDir())
	fresh.SyncRoot = b.SyncRoot
	m, _ := c.Manifest(ctx)
	st, err := fresh.SyncFrom(ctx, c, m, nil)
	if err != nil || len(st.Applied) != 0 {
		t.Fatalf("resync: %+v, %v", st, err)
	}
	is, err := fresh.ImportFrom(ctx, filepath.Join(st.Dir, "tree"), st.Changed, ImportOptions{Jobs: 2})
	if err != nil || is.New == 0 {
		t.Errorf("an empty repository imported %+v, %v", is, err)
	}
}

func TestUnpackDir(t *testing.T) {
	ctx := context.Background()
	a := packedStore(t, 15)
	appendPacks(t, a.Dir, a.Dir, 6<<10)
	// The packs, copied somewhere as files.
	drop := t.TempDir()
	log, _ := ReadPackLog(a.Dir)
	for _, e := range log.Packs {
		b, err := os.ReadFile(filepath.Join(a.Dir, e.Path))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(drop, filepath.Base(e.Path)), b, 0o644)
	}
	c, _ := Open(t.TempDir())
	st, err := c.UnpackDir(ctx, drop, nil, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Applied) != len(log.Packs) {
		t.Errorf("unpacked %d packs of %d", len(st.Applied), len(log.Packs))
	}
	if _, err := c.ImportFrom(ctx, filepath.Join(st.Dir, "tree"), st.Changed, ImportOptions{Jobs: 2}); err != nil {
		t.Fatal(err)
	}
	listing(t, c, "tool010", "1")
	if left, _ := os.ReadDir(drop); len(left) != 0 {
		t.Errorf("unpacked packs were left behind: %d", len(left))
	}
}

func TestPackTampering(t *testing.T) {
	s := packedStore(t, 5)
	appendPacks(t, s.Dir, s.Dir, 1<<20)
	log, _ := ReadPackLog(s.Dir)
	file := filepath.Join(s.Dir, log.Packs[0].Path)
	b, _ := os.ReadFile(file)
	// A byte changed late in the pack: the body's compressed differently,
	// or a file in it isn't what the header says.
	b[len(b)-40] ^= 0xff
	bad := filepath.Join(t.TempDir(), "bad.tar.zst")
	os.WriteFile(bad, b, 0o644)
	if _, _, err := applyPack(context.Background(), bad, t.TempDir()); err == nil {
		t.Error("a damaged pack was applied")
	}
}
