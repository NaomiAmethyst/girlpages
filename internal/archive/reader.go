// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package archive

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxIndexBytes bounds what's read as an index file.
const maxIndexBytes = 4 << 30

// ReadIndex reads an index file, checking it's named after its hash (if
// it's named as an index is).
func ReadIndex(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := readAllLimit(f, maxIndexBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ix, err := decodeIndex(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if name := filepath.Base(path); strings.HasPrefix(name, "index-") && name != IndexName(ix.SHA256) {
		return nil, fmt.Errorf("%s doesn't match its hash", path)
	}
	return ix, nil
}

// ReadCurrentPointer reads an archive's pointer to its current index.
func ReadCurrentPointer(dir string) (Current, error) {
	var c Current
	b, err := os.ReadFile(filepath.Join(dir, CurrentName))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", filepath.Join(dir, CurrentName), err)
	}
	if c.Format != Format {
		return c, fmt.Errorf("archive format %d, not %d", c.Format, Format)
	}
	if !strings.HasPrefix(c.Index, "index-") || strings.ContainsAny(c.Index, `/\`) {
		return c, fmt.Errorf("%s names %q", filepath.Join(dir, CurrentName), c.Index)
	}
	return c, nil
}

// ReadCurrent reads an archive's current index; fs.ErrNotExist if there
// isn't one yet.
func ReadCurrent(dir string) (*Index, error) {
	c, err := ReadCurrentPointer(dir)
	if err != nil {
		return nil, err
	}
	return ReadIndex(filepath.Join(dir, c.Index))
}

// WriteIndex writes an index for entries (whose blobs are in h's
// segments), returning its hash.
func WriteIndex(dir string, h Header, entries []Entry) (string, error) {
	b, err := encodeIndex(h, entries)
	if err != nil {
		return "", err
	}
	sha := fmt.Sprintf("%x", sha256.Sum256(b))
	return sha, writeAtomic(dir, IndexName(sha), b)
}

// WriteCurrent points an archive at an index.
func WriteCurrent(dir string, c Current) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(dir, CurrentName, append(b, '\n'))
}

// writeAtomic writes a file in dir by writing a temporary one and
// renaming it, synced, so readers see the old file or the new one.
func writeAtomic(dir, name string, b []byte) error {
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// Archive serves files from an archive's current index, which Reload
// (or Watch) keeps up with.
type Archive struct {
	dir string
	cur atomic.Pointer[Snapshot]

	mu    sync.Mutex
	files map[string]*os.File // open segments, by hash
	// reloads and reloadErrors count Reload's outcomes.
	reloads, reloadErrors atomic.Int64
}

// Snapshot is one version of an archive: an index and its segments.
type Snapshot struct {
	Index  *Index
	Loaded time.Time
	segs   []*os.File
}

// New is an archive with nothing loaded yet (Current is nil until a
// Reload finds a current index): for serving one that may not have been
// written yet.
func New(dir string) *Archive { return &Archive{dir: dir, files: map[string]*os.File{}} }

// Open opens an archive, loading its current index.
func Open(dir string) (*Archive, error) {
	a := &Archive{dir: dir, files: map[string]*os.File{}}
	if _, err := a.reload(); err != nil { // (loading, not a reload)
		return nil, err
	}
	return a, nil
}

// Dir is the archive's directory.
func (a *Archive) Dir() string { return a.dir }

// Current is the snapshot being served.
func (a *Archive) Current() *Snapshot { return a.cur.Load() }

// Counts are how many reloads happened (that changed what's served),
// and how many failed.
func (a *Archive) Counts() (reloads, failures int64) {
	return a.reloads.Load(), a.reloadErrors.Load()
}

// segmentGrace is how long a segment the current index no longer uses
// stays open, for requests still reading it.
var segmentGrace = 10 * time.Minute

// Reload loads the archive's current index if it's not the one being
// served, reporting whether it changed. If it fails, what was being
// served still is.
func (a *Archive) Reload() (bool, error) {
	changed, err := a.reload()
	if err != nil {
		a.reloadErrors.Add(1)
	} else if changed {
		a.reloads.Add(1)
	}
	return changed, err
}

func (a *Archive) reload() (bool, error) {
	c, err := ReadCurrentPointer(a.dir)
	if err != nil {
		return false, err
	}
	if s := a.cur.Load(); s != nil && IndexName(s.Index.SHA256) == c.Index {
		return false, nil
	}
	ix, err := ReadIndex(filepath.Join(a.dir, c.Index))
	if err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	segs := make([]*os.File, len(ix.Segments))
	opened := map[string]*os.File{}
	for i, s := range ix.Segments {
		f := a.files[s.SHA256]
		if f == nil {
			f = opened[s.SHA256]
		}
		if f == nil {
			if f, err = os.Open(filepath.Join(a.dir, filepath.FromSlash(SegmentName(s.SHA256)))); err == nil {
				var fi os.FileInfo
				if fi, err = f.Stat(); err == nil && fi.Size() != s.Bytes {
					err = fmt.Errorf("segment %s is %d bytes, not %d", s.SHA256, fi.Size(), s.Bytes)
				}
				if err != nil {
					f.Close()
				}
			}
			if err != nil {
				for _, f := range opened {
					f.Close()
				}
				return false, err
			}
			opened[s.SHA256] = f
		}
		segs[i] = f
	}
	for sha, f := range opened {
		a.files[sha] = f
	}
	// Segments no longer used are closed once requests have had time to
	// finish with them.
	inUse := map[string]bool{}
	for _, s := range ix.Segments {
		inUse[s.SHA256] = true
	}
	for sha, f := range a.files {
		if !inUse[sha] {
			delete(a.files, sha)
			time.AfterFunc(segmentGrace, func() { f.Close() })
		}
	}
	a.cur.Store(&Snapshot{Index: ix, Loaded: time.Now(), segs: segs})
	return true, nil
}

// Watch reloads the archive every interval until ctx is done (and when
// asked to on reload), telling report of each reload that changed
// something or failed.
func (a *Archive) Watch(ctx context.Context, interval time.Duration, reload <-chan struct{}, report func(changed bool, err error)) {
	var tick <-chan time.Time
	if interval > 0 {
		t := time.NewTicker(interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-reload:
		}
		changed, err := a.Reload()
		if report != nil && (changed || err != nil) {
			report(changed, err)
		}
	}
}

// Close closes the archive's segments.
func (a *Archive) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for sha, f := range a.files {
		f.Close()
		delete(a.files, sha)
	}
	return nil
}

// ErrNotFound is what Open returns for a path the archive hasn't.
var ErrNotFound = errors.New("not in the archive")

// Lookup finds a file in the snapshot.
func (s *Snapshot) Lookup(path string) (Entry, bool) { return s.Index.Lookup(path) }

// Open reads one of an entry's encodings ("" for identity).
func (s *Snapshot) Open(e Entry, encoding string) (io.ReadSeeker, int64, error) {
	b, ok := e.Blob(encoding)
	if !ok {
		return nil, 0, fmt.Errorf("%s (%q): %w", e.Path, encoding, ErrNotFound)
	}
	return io.NewSectionReader(s.segs[b.Seg], b.Off, b.Len), b.Len, nil
}
