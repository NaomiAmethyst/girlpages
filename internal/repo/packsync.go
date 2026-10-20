// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Syncing packs: a repository keeps each log it syncs from in a sync
// directory of its own, <SyncRoot>/<id>/, named after the log's first pack. That's a pack-only
// repository of its own (the packs synced, packs/index.json saying which,
// and a manifest.json listing them), so it can be served as a mirror; and
// tree/ in it holds what replaying them gives, to import from.

// SyncDir is where a repository keeps a log it syncs from: in SyncRoot,
// or if that's not set, .sync in the repository.
func (s *Store) SyncDir(first string) string {
	root := s.SyncRoot
	if root == "" {
		root = filepath.Join(s.Dir, ".sync")
	}
	return filepath.Join(root, first[:16])
}

// PackManifest is a pack-only repository's manifest.json.
type PackManifest struct {
	Format    int         `json:"format"`
	Generated time.Time   `json:"generated"`
	Packs     []PackEntry `json:"packs"`
}

// WritePackManifest writes a pack-only repository's manifest.json, in
// root, listing the packs in its log.
func WritePackManifest(root string, packs []PackEntry) error {
	if packs == nil {
		packs = []PackEntry{}
	}
	b, err := Encode(&PackManifest{Format: Format, Generated: now(), Packs: packs})
	if err != nil {
		return err
	}
	return WriteFile(filepath.Join(root, "manifest.json"), b)
}

// SyncStats say what a sync did.
type SyncStats struct {
	// Dir is the sync directory; Applied, the packs applied this time;
	// Bytes, their size.
	Dir     string
	Applied []PackEntry
	Bytes   int64
	// Changed are the pointer files (by path) the packs applied put, to
	// import; nil if it's all of them (the first sync, or starting
	// again).
	Changed map[string]bool
}

// fetchPack gets a pack (as a log lists it) into a file.
type fetchPack func(ctx context.Context, e PackEntry, dst string) error

// SyncPacks brings this repository's copy of a log up to date with the
// packs a repository lists (in its manifest.json), fetching those it
// hasn't applied with fetch, checking them, and applying them to the
// sync directory's tree. A copy that's diverged (the log started again,
// or it applied packs that are gone without being replaced) is started
// again.
func (s *Store) syncPacks(ctx context.Context, listed []PackEntry, fetch fetchPack, progress Progress) (SyncStats, error) {
	var st SyncStats
	if len(listed) == 0 || listed[0].Seq != 1 {
		return st, errors.New("its packs don't start at the start of the log")
	}
	for _, e := range listed {
		if !ValidHash(e.SHA256) || e.Path != PackPath(e.SHA256) {
			return st, fmt.Errorf("bad pack %q", e.Path)
		}
	}
	root := s.SyncDir(listed[0].SHA256)
	st.Dir = root
	tree := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(root, "packs"), 0o755); err != nil {
		return st, err
	}
	unlock, err := LockFile(filepath.Join(root, "packs", ".lock"))
	if err != nil {
		return st, err
	}
	defer unlock()
	log, err := ReadPackLog(root)
	if err != nil {
		return st, err
	}

	// What's applied must be what's listed, as far as it goes, but for
	// tail packs at the end, which a listed pack must replace.
	listedAt := map[string]int{}
	for i, e := range listed {
		listedAt[e.SHA256] = i
	}
	common := 0
	for common < len(log.Packs) && common < len(listed) && log.Packs[common].SHA256 == listed[common].SHA256 {
		common++
	}
	stale := map[string]bool{}
	for _, e := range log.Packs[common:] {
		if !e.Tail {
			return s.restartSync(ctx, root, listed, fetch, progress)
		}
		stale[e.SHA256] = true
	}
	// What's to merge: what wasn't, last time (if merging stopped part of
	// the way), and what the packs applied now change; everything, for
	// a copy that's new.
	all, pending, known := readPending(root)
	if len(log.Packs) == 0 || !known {
		// New, or synced by a girlrepo that didn't keep track (before
		// 0.0.4), which may have stopped merging part of the way.
		all = true
	}
	log.Packs = log.Packs[:common]
	for i := common; i < len(listed); i++ {
		e := listed[i]
		if err := ctx.Err(); err != nil {
			return st, err
		}
		file := filepath.Join(root, filepath.FromSlash(e.Path))
		if progress != nil {
			progress(fmt.Sprintf("fetching pack %d of %d", i+1, len(listed)), 0, 0)
		}
		if _, err := os.Stat(file); err != nil {
			if err := fetch(ctx, e, file); err != nil {
				return st, err
			}
		}
		// The pack's hash vouches for everything in it, however it got
		// here (fetched, moved in, or left by a sync that was stopped).
		if err := checkFile(file, e.SHA256, e.Bytes); err != nil {
			os.Remove(file)
			return st, err
		}
		h, err := ReadPackHeader(file)
		if err != nil {
			return st, err
		}
		prev := ""
		if i > 0 {
			prev = listed[i-1].SHA256
		}
		if h.Seq != e.Seq || h.Prev != prev {
			return st, fmt.Errorf("pack %d (%s) says it's %d, after %.12s, not %d after %.12s", i+1, e.SHA256[:12], h.Seq, h.Prev, e.Seq, prev)
		}
		for _, r := range h.Replaces {
			delete(stale, r)
		}
		stage := fmt.Sprintf("unpacking pack %d of %d into %s", i+1, len(listed), tree)
		if progress != nil {
			progress(stage, 0, h.Puts)
		}
		_, put, err := applyPack(ctx, file, tree, func(done, total int) {
			if progress != nil {
				progress(stage, done, total)
			}
		})
		if err != nil {
			return st, err
		}
		if !all {
			for _, p := range put {
				if strings.HasPrefix(p, "page/name-") {
					pending[p] = true
				}
			}
		}
		// Remember it now, what's to merge first: a sync that's
		// interrupted goes on from here.
		if err := writePending(root, all, pending); err != nil {
			return st, err
		}
		st.Applied = append(st.Applied, e)
		st.Bytes += e.Bytes
		log.Packs = append(log.Packs, e)
		if err := s.writeSyncLog(root, log); err != nil {
			return st, err
		}
	}
	if len(stale) > 0 {
		// Applied tail packs that nothing replaced: the log's not what
		// this copy of it says.
		return s.restartSync(ctx, root, listed, fetch, progress)
	}
	if all {
		st.Changed = nil
	} else {
		st.Changed = pending
	}
	// Replaced packs (and any others not in the log) go.
	ents, _ := os.ReadDir(filepath.Join(root, "packs"))
	for _, d := range ents {
		name := d.Name()
		if sha, ok := strings.CutSuffix(name, ".tar.zst"); ok {
			if _, listed := listedAt[sha]; !listed {
				os.Remove(filepath.Join(root, "packs", name))
			}
		}
	}
	return st, s.writeSyncLog(root, log)
}

// checkFile checks a file is bytes long and hashes to sha.
func checkFile(file, sha string, bytes int64) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != bytes || hex.EncodeToString(h.Sum(nil)) != sha {
		return fmt.Errorf("%s isn't the pack it should be (%d bytes, not %d, or a different hash)", filepath.Base(file), n, bytes)
	}
	return nil
}

// restartSync throws a copy of a log away and syncs it again.
func (s *Store) restartSync(ctx context.Context, root string, listed []PackEntry, fetch fetchPack, progress Progress) (SyncStats, error) {
	if err := os.RemoveAll(filepath.Join(root, "tree")); err != nil {
		return SyncStats{}, err
	}
	for _, f := range []string{filepath.FromSlash(PackLogPath()), "pending"} {
		if err := os.Remove(filepath.Join(root, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return SyncStats{}, err
		}
	}
	return s.syncPacks(ctx, listed, fetch, progress)
}

// What a copy of a log has applied but not yet merged into the
// repository is kept in its pending file: "all", or the pointer files the
// packs put, a line each. Merging empties it (Merged), so merging that
// stops part of the way is done again next time. A copy with no pending
// file at all isn't known to be merged.

func readPending(root string) (all bool, pending map[string]bool, known bool) {
	pending = map[string]bool{}
	b, err := os.ReadFile(filepath.Join(root, "pending"))
	if err != nil {
		return false, pending, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case line == "all":
			return true, pending, true
		case line != "":
			pending[line] = true
		}
	}
	return false, pending, true
}

func writePending(root string, all bool, pending map[string]bool) error {
	var b strings.Builder
	if all {
		b.WriteString("all\n")
	} else {
		lines := make([]string, 0, len(pending))
		for p := range pending {
			lines = append(lines, p)
		}
		sort.Strings(lines)
		for _, p := range lines {
			b.WriteString(p + "\n")
		}
	}
	return WriteFile(filepath.Join(root, "pending"), []byte(b.String()))
}

// Merged records that what a sync applied has been merged into the
// repository, so the next sync merges just what it changes.
func (s *Store) Merged(st SyncStats) error {
	return writePending(st.Dir, false, nil)
}

// writeSyncLog records what's applied, and lists it in the copy's
// manifest.json, so it's a pack-only repository of its own.
func (s *Store) writeSyncLog(root string, log *PackLog) error {
	log.Format = Format
	if err := writePackLog(root, log); err != nil {
		return err
	}
	return WritePackManifest(root, log.Packs)
}

// SyncFrom syncs the packs a repository lists (see SyncPacks), fetching
// them from it.
func (s *Store) SyncFrom(ctx context.Context, c *Client, m *Manifest, progress Progress) (SyncStats, error) {
	return s.syncPacks(ctx, m.Packs, func(ctx context.Context, e PackEntry, dst string) error {
		return c.Download(ctx, e.Path, dst, e.SHA256, e.Bytes)
	}, progress)
}

// ErrNoPacks means there were no packs to unpack.
var ErrNoPacks = errors.New("no packs to unpack")

// UnpackDir syncs from pack files in dir (as a log's packs/ has them, or
// just the files): those that make a log from its first pack on, in
// order, but for own (a repository's own log, say), and tail packs others
// there replace (a log's packs/ keeps them a while). Those it uses go into
// the sync directory: moved, if move is set (so they're not unpacked
// again), or else linked or copied (leaving a mirror as it was).
func (s *Store) UnpackDir(ctx context.Context, dir string, own map[string]bool, move bool, progress Progress) (SyncStats, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return SyncStats{}, fmt.Errorf("%s: %w", dir, ErrNoPacks)
	}
	if err != nil {
		return SyncStats{}, err
	}
	type found struct {
		entry    PackEntry
		prev     string
		file     string
		replaces []string
	}
	var all []found
	for _, d := range ents {
		sha, ok := strings.CutSuffix(d.Name(), ".tar.zst")
		if !ok || !ValidHash(sha) || own[sha] {
			continue
		}
		file := filepath.Join(dir, d.Name())
		h, err := ReadPackHeader(file)
		if err != nil {
			return SyncStats{}, err
		}
		fi, err := d.Info()
		if err != nil {
			return SyncStats{}, err
		}
		all = append(all, found{PackEntry{Path: PackPath(sha), SHA256: sha, Bytes: fi.Size(), Seq: h.Seq, Tail: h.Tail}, h.Prev, file, h.Replaces})
	}
	if len(all) == 0 {
		return SyncStats{}, fmt.Errorf("%s: %w", dir, ErrNoPacks)
	}
	replaced := map[string]bool{}
	for _, f := range all {
		for _, r := range f.replaces {
			replaced[r] = true
		}
	}
	bySeq := map[int][]found{}
	for _, f := range all {
		if !replaced[f.entry.SHA256] {
			bySeq[f.entry.Seq] = append(bySeq[f.entry.Seq], f)
		}
	}
	// Follow the log from its first pack: each pack's successor is the
	// one that names it.
	var chain []found
	prev := ""
	for seq := 1; ; seq++ {
		var next []found
		for _, f := range bySeq[seq] {
			if f.prev == prev {
				next = append(next, f)
			}
		}
		if len(next) == 0 {
			break
		}
		if len(next) > 1 {
			sort.Slice(next, func(i, j int) bool { return next[i].entry.SHA256 < next[j].entry.SHA256 })
			return SyncStats{}, fmt.Errorf("packs %.12s and %.12s both follow pack %d", next[0].entry.SHA256, next[1].entry.SHA256, seq-1)
		}
		chain = append(chain, next[0])
		prev = next[0].entry.SHA256
	}
	if len(chain) == 0 {
		return SyncStats{}, fmt.Errorf("the packs in %s don't include the first of their log", dir)
	}
	listed := make([]PackEntry, len(chain))
	files := map[string]string{}
	for i, f := range chain {
		listed[i] = f.entry
		files[f.entry.SHA256] = f.file
	}
	if move {
		// Those that were applied already go too.
		defer func() {
			for _, f := range files {
				if _, err := os.Stat(f); err == nil {
					sha := strings.TrimSuffix(filepath.Base(f), ".tar.zst")
					if _, err := os.Stat(filepath.Join(s.SyncDir(listed[0].SHA256), filepath.FromSlash(PackPath(sha)))); err == nil {
						os.Remove(f)
					}
				}
			}
		}()
	}
	return s.syncPacks(ctx, listed, func(_ context.Context, e PackEntry, dst string) error {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if move {
			if err := os.Rename(files[e.SHA256], dst); err == nil {
				return nil
			}
		} else if err := os.Link(files[e.SHA256], dst); err == nil {
			return nil
		}
		// Elsewhere (another file system): copy it, as a download.
		if err := NewClient(dir, 0).Download(context.Background(), filepath.Base(files[e.SHA256]), dst, e.SHA256, e.Bytes); err != nil {
			return err
		}
		if move {
			return os.Remove(files[e.SHA256])
		}
		return nil
	}, progress)
}
