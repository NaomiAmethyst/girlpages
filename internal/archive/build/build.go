// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package build writes a published site into an archive (see package
// archive): what changed since the archive's current index is
// compressed and appended, what didn't is pointed at where it already is.
package build

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/archive"
	"github.com/NaomiAmethyst/girlpages/internal/precompress"
)

// Options tunes Write.
type Options struct {
	// Jobs is how many files are read and compressed at once (default:
	// one per CPU).
	Jobs int
	// SegmentSize is how big segments get before another is started
	// (default 1 GiB).
	SegmentSize int64
	// Keep is how many of the newest indexes are kept (default 5), and
	// KeepFor how long any index or segment is kept after it's last used
	// (default an hour), for serving processes still on an older one.
	Keep    int
	KeepFor time.Duration
	// CompactBelow compacts the archive (copying what's in use into new
	// segments) when less than this share of its segments' bytes is in
	// use (default 0.5; negative never). Compact always does.
	CompactBelow float64
	Compact      bool
	// Progress, if set, is told how it's going.
	Progress func(stage string, done, total int)
	// Now is the clock (for tests).
	Now func() time.Time
}

// Stats says what Write did.
type Stats struct {
	// Files in the site; New of them weren't in the archive (by content),
	// whose encodings took NewBytes.
	Files, New int
	NewBytes   int64
	// Live is the bytes the new index uses, in Segments segments.
	Live      int64
	Segments  int
	Compacted bool
	// Removed counts old indexes and segments removed.
	Removed int
	Index   string
}

// Write adds the site in site to the archive in dir (creating it), and
// makes it the archive's current version.
func Write(ctx context.Context, site, dir string, o Options) (Stats, error) {
	absSite, _ := filepath.Abs(site)
	absDir, _ := filepath.Abs(dir)
	if absDir == absSite || strings.HasPrefix(absDir, absSite+string(filepath.Separator)) {
		return Stats{}, fmt.Errorf("the archive (%s) can't be inside the site (%s)", dir, site)
	}
	st, err := NewStage(dir, o)
	if err != nil {
		return Stats{}, err
	}
	defer st.Abort()
	paths, err := listSite(site)
	if err != nil {
		return Stats{}, err
	}
	if err := st.putFiles(ctx, site, paths); err != nil {
		return Stats{}, err
	}
	return st.Finish(ctx)
}

// putFiles puts files from a directory, from every worker at once.
func (st *Stage) putFiles(ctx context.Context, root string, paths []string) error {
	jobs := make(chan string)
	errs := make(chan error, st.o.Jobs)
	var wg sync.WaitGroup
	var done atomic.Int64
	for w := 0; w < st.o.Jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range jobs {
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
				if err == nil {
					err = st.Put(rel, b)
				}
				if err != nil {
					select {
					case errs <- fmt.Errorf("%s: %w", rel, err):
					default:
					}
					continue
				}
				if n := int(done.Add(1)); n%1000 == 0 || n == len(paths) {
					st.report("archiving", n, len(paths))
				}
			}
		}()
	}
	for _, rel := range paths {
		select {
		case jobs <- rel:
		case err := <-errs:
			close(jobs)
			wg.Wait()
			return err
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
	}
	return ctx.Err()
}

// A Stage is the next version of an archive being put together: files
// are put into it (from a directory, packs, a publish: anything), read
// back, and listed, and Finish makes it the archive's current version.
// What's put is written to the archive's segments as it goes, unless the
// archive (or the stage) has it already; it's safe to use from many
// goroutines at once.
type Stage struct {
	dir string
	o   Options

	mu      sync.Mutex
	unlock  func()
	segs    *segTable
	w       *segWriter
	readers map[int]*os.File
	// known is what the previous version has, by content; fresh what's
	// been added since.
	known, fresh map[archive.Hash][4]archive.Blob
	entries      map[string]staged
	// dirs is each directory's entries (true for a directory).
	dirs     map[string]map[string]bool
	new      int
	newBytes int64
	done     bool
}

type staged struct {
	hash  archive.Hash
	blobs [4]archive.Blob
}

// NewStage starts the next version of the archive in dir (making it, if
// need be). It holds the archive's lock until Finish or Abort.
func NewStage(dir string, o Options) (*Stage, error) {
	if o.Jobs <= 0 {
		o.Jobs = runtime.NumCPU()
	}
	if o.SegmentSize <= 0 {
		o.SegmentSize = 1 << 30
	}
	if o.Keep <= 0 {
		o.Keep = 5
	}
	if o.KeepFor <= 0 {
		o.KeepFor = time.Hour
	}
	if o.CompactBelow == 0 {
		o.CompactBelow = 0.5
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Join(dir, archive.SegmentDir), 0o755); err != nil {
		return nil, err
	}
	unlock, err := lockFile(filepath.Join(dir, ".lock"))
	if err != nil {
		return nil, err
	}
	st := &Stage{dir: dir, o: o, unlock: unlock, segs: &segTable{}, readers: map[int]*os.File{},
		known: map[archive.Hash][4]archive.Blob{}, fresh: map[archive.Hash][4]archive.Blob{},
		entries: map[string]staged{}, dirs: map[string]map[string]bool{}}
	st.w = &segWriter{dir: dir, limit: o.SegmentSize, table: st.segs, readers: st.readers}
	prev, err := archive.ReadCurrent(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		unlock()
		return nil, err
	}
	if prev != nil {
		remap := make([]int, len(prev.Segments))
		for i, s := range prev.Segments {
			remap[i] = st.segs.add(s)
		}
		prev.Each(func(e archive.Entry) bool {
			if _, ok := st.known[e.Hash]; !ok {
				b := e.Blobs
				for i := range b {
					if b[i].Len >= 0 {
						b[i].Seg = remap[b[i].Seg]
					}
				}
				st.known[e.Hash] = b
			}
			return true
		})
	}
	return st, nil
}

func (st *Stage) report(stage string, done, total int) {
	if st.o.Progress != nil {
		st.o.Progress(stage, done, total)
	}
}

// Put puts a file (by its slash path from the site's root) into the
// stage, in place of any already there.
func (st *Stage) Put(rel string, b []byte) error {
	if !validPath(rel) {
		return fmt.Errorf("bad path %q", rel)
	}
	h := archive.HashOf(b)
	st.mu.Lock()
	if st.done {
		st.mu.Unlock()
		return errors.New("the stage is finished")
	}
	if e, ok := st.entries[rel]; ok && e.hash == h {
		st.mu.Unlock()
		return nil
	}
	if blobs, ok := st.have(h); ok {
		st.set(rel, staged{h, blobs})
		st.mu.Unlock()
		return nil
	}
	st.mu.Unlock()

	// New: compressed (outside the lock), then appended.
	var data [4][]byte
	data[0] = b
	if precompress.Worth(rel) && len(b) >= precompress.MinSize {
		for k, name := range archive.Encodings[1:] {
			e, _ := precompress.Lookup(name)
			c, err := e.Compress(b)
			if err != nil {
				return fmt.Errorf("%s: %s: %w", rel, name, err)
			}
			if len(c) < len(b) {
				data[k+1] = c
			}
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if blobs, ok := st.have(h); ok { // (put meanwhile)
		st.set(rel, staged{h, blobs})
		return nil
	}
	blobs := blank()
	for k, d := range data {
		if d == nil {
			continue
		}
		bl, err := st.w.append(d)
		if err != nil {
			return err
		}
		blobs[k] = bl
		st.newBytes += int64(len(d))
	}
	st.fresh[h] = blobs
	st.new++
	st.set(rel, staged{h, blobs})
	return nil
}

func (st *Stage) have(h archive.Hash) ([4]archive.Blob, bool) {
	if b, ok := st.known[h]; ok {
		return b, true
	}
	b, ok := st.fresh[h]
	return b, ok
}

// set records an entry (holding mu), and its directories.
func (st *Stage) set(rel string, e staged) {
	if _, ok := st.entries[rel]; !ok {
		child, isDir := rel, false
		for {
			parent := path.Dir(child)
			if parent == "." {
				parent = ""
			}
			m := st.dirs[parent]
			if m == nil {
				m = map[string]bool{}
				st.dirs[parent] = m
			}
			name := path.Base(child)
			if _, had := m[name]; had {
				break
			}
			m[name] = isDir
			if parent == "" {
				break
			}
			child, isDir = parent, true
		}
	}
	st.entries[rel] = e
}

// Delete takes a file out of the stage, if it's there.
func (st *Stage) Delete(rel string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.entries[rel]; !ok {
		return
	}
	delete(st.entries, rel)
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	delete(st.dirs[parent], path.Base(rel))
}

// Get reads a file from the stage: fs.ErrNotExist if it isn't there.
func (st *Stage) Get(rel string) ([]byte, error) {
	st.mu.Lock()
	e, ok := st.entries[rel]
	if !ok {
		st.mu.Unlock()
		return nil, fmt.Errorf("%s: %w", rel, fs.ErrNotExist)
	}
	f, err := st.reader(e.blobs[0].Seg)
	st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	b := make([]byte, e.blobs[0].Len)
	if _, err := f.ReadAt(b, e.blobs[0].Off); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	return b, nil
}

// Has reports whether a file is in the stage.
func (st *Stage) Has(rel string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.entries[rel]
	return ok
}

// ReadDir lists a directory of the stage ("" for the root): its files and
// directories, sorted by name.
func (st *Stage) ReadDir(rel string) ([]fs.DirEntry, error) {
	rel = strings.Trim(rel, "/")
	st.mu.Lock()
	m, ok := st.dirs[rel]
	out := make([]fs.DirEntry, 0, len(m))
	for name, isDir := range m {
		out = append(out, dirEntry{name, isDir})
	}
	st.mu.Unlock()
	if !ok && rel != "" {
		return nil, fmt.Errorf("%s: %w", rel, fs.ErrNotExist)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// reader is a segment's file, to read from (holding mu).
func (st *Stage) reader(seg int) (*os.File, error) {
	if f := st.readers[seg]; f != nil {
		return f, nil
	}
	f, err := os.Open(filepath.Join(st.dir, filepath.FromSlash(archive.SegmentName(st.segs.segs[seg].SHA256))))
	if err != nil {
		return nil, err
	}
	st.readers[seg] = f
	return f, nil
}

// Paths lists the stage's files, sorted.
func (st *Stage) Paths() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]string, 0, len(st.entries))
	for p := range st.entries {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Finish makes what's been put the archive's current version, collects
// garbage and lets go of the archive.
func (st *Stage) Finish(ctx context.Context) (Stats, error) {
	var stats Stats
	st.mu.Lock()
	if st.done {
		st.mu.Unlock()
		return stats, errors.New("the stage is finished")
	}
	st.done = true
	st.mu.Unlock()
	defer st.release()
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	if err := st.w.finish(); err != nil {
		return stats, err
	}
	entries := make([]archive.Entry, 0, len(st.entries))
	for p, e := range st.entries {
		entries = append(entries, archive.Entry{Path: p, Hash: e.hash, Blobs: e.blobs})
	}
	stats.Files, stats.New, stats.NewBytes = len(entries), st.new, st.newBytes

	live, total := usage(entries, st.segs)
	if st.o.Compact || (st.o.CompactBelow > 0 && total > 0 && float64(live) < st.o.CompactBelow*float64(total)) {
		st.report("compacting", 0, len(entries))
		if err := compact(ctx, st.dir, entries, st.segs, st.o.SegmentSize); err != nil {
			return stats, err
		}
		stats.Compacted = true
		live, _ = usage(entries, st.segs)
	}
	h, err := renumber(entries, st.segs)
	if err != nil {
		return stats, err
	}
	h.Generated = st.o.Now().UTC().Truncate(time.Second)
	h.Live = live
	stats.Live, stats.Segments = live, len(h.Segments)
	sha, err := archive.WriteIndex(st.dir, h, entries)
	if err != nil {
		return stats, err
	}
	stats.Index = sha
	if err := archive.WriteCurrent(st.dir, archive.Current{Format: archive.Format, Index: archive.IndexName(sha), Generated: h.Generated}); err != nil {
		return stats, err
	}
	n, err := collect(st.dir, sha, st.o)
	stats.Removed = n
	return stats, err
}

// Abort gives up on the stage (if it wasn't finished): what it wrote is
// left for garbage collection.
func (st *Stage) Abort() {
	st.mu.Lock()
	wasDone := st.done
	st.done = true
	st.mu.Unlock()
	if !wasDone {
		st.release()
	}
}

func (st *Stage) release() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.w.abort()
	for k, f := range st.readers {
		f.Close()
		delete(st.readers, k)
	}
	if st.unlock != nil {
		st.unlock()
		st.unlock = nil
	}
}

// validPath reports whether rel is a clean relative slash path.
func validPath(rel string) bool {
	return rel != "" && !strings.HasPrefix(rel, "/") && path.Clean(rel) == rel && !strings.HasPrefix(rel, "../") && rel != ".."
}

type dirEntry struct {
	name  string
	isDir bool
}

func (d dirEntry) Name() string { return d.name }
func (d dirEntry) IsDir() bool  { return d.isDir }
func (d dirEntry) Type() fs.FileMode {
	if d.isDir {
		return fs.ModeDir
	}
	return 0
}
func (d dirEntry) Info() (fs.FileInfo, error) { return nil, errors.ErrUnsupported }

// listSite lists a site's files (by slash paths), leaving out hidden
// files (locks, temporary files) and compressed copies of others, which
// the archive makes its own.
func listSite(site string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(site, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if p != site && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if base, ok := precompress.IsCopy(p); ok {
			if _, err := os.Stat(base); err == nil {
				return nil
			}
		}
		rel, err := filepath.Rel(site, p)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func blank() [4]archive.Blob {
	var b [4]archive.Blob
	for i := range b {
		b[i].Len = -1
	}
	return b
}

// segTable is every segment blobs may be in while writing: the previous
// index's, then new ones (whose names are known once they're finished).
type segTable struct {
	segs []archive.Segment
}

func (t *segTable) add(s archive.Segment) int {
	t.segs = append(t.segs, s)
	return len(t.segs) - 1
}

// segWriter appends blobs to new segments.
type segWriter struct {
	dir   string
	limit int64
	table *segTable
	// readers, if set, gets the open segment being written (to read from
	// as it's written), and loses it when it's finished.
	readers map[int]*os.File
	f       *os.File
	tmp     string
	h       hash.Hash
	n       int64
	cur     int
}

func (w *segWriter) append(b []byte) (archive.Blob, error) {
	if w.f != nil && w.n+int64(len(b)) > w.limit && w.n > int64(len("GPASEG1\n")) {
		if err := w.finish(); err != nil {
			return archive.Blob{}, err
		}
	}
	if w.f == nil {
		f, err := os.CreateTemp(filepath.Join(w.dir, archive.SegmentDir), ".tmp-*")
		if err != nil {
			return archive.Blob{}, err
		}
		w.f, w.tmp, w.h, w.n = f, f.Name(), sha256.New(), 0
		w.cur = w.table.add(archive.Segment{})
		if w.readers != nil {
			w.readers[w.cur] = f
		}
		if err := w.write([]byte("GPASEG1\n")); err != nil {
			return archive.Blob{}, err
		}
	}
	bl := archive.Blob{Seg: w.cur, Off: w.n, Len: int64(len(b))}
	return bl, w.write(b)
}

func (w *segWriter) write(b []byte) error {
	if _, err := w.f.Write(b); err != nil {
		return err
	}
	w.h.Write(b)
	w.n += int64(len(b))
	return nil
}

// finish names the segment being written after its hash.
func (w *segWriter) finish() error {
	if w.f == nil {
		return nil
	}
	f := w.f
	w.f = nil
	if w.readers != nil && w.readers[w.cur] == f {
		delete(w.readers, w.cur) // (opened by its name when it's next read)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	sha := fmt.Sprintf("%x", w.h.Sum(nil))
	if err := os.Rename(w.tmp, filepath.Join(w.dir, filepath.FromSlash(archive.SegmentName(sha)))); err != nil {
		return err
	}
	w.table.segs[w.cur] = archive.Segment{SHA256: sha, Bytes: w.n}
	return nil
}

// abort removes a segment that wasn't finished.
func (w *segWriter) abort() {
	if w.f != nil {
		if w.readers != nil && w.readers[w.cur] == w.f {
			delete(w.readers, w.cur)
		}
		w.f.Close()
		os.Remove(w.tmp)
		w.f = nil
	}
}

// usage is how many bytes entries' blobs take (each once), and how big
// the segments they're in are.
func usage(entries []archive.Entry, t *segTable) (live, total int64) {
	seen := map[archive.Blob]bool{}
	used := map[int]bool{}
	for _, e := range entries {
		for _, b := range e.Blobs {
			if b.Len >= 0 && !seen[b] {
				seen[b] = true
				live += b.Len
				used[b.Seg] = true
			}
		}
	}
	for s := range used {
		total += t.segs[s].Bytes
	}
	return live, total
}

// compact copies every blob in use into new segments.
func compact(ctx context.Context, dir string, entries []archive.Entry, t *segTable, limit int64) error {
	files := map[int]*os.File{}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	w := &segWriter{dir: dir, limit: limit, table: t}
	defer w.abort()
	moved := map[archive.Blob]archive.Blob{}
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		for k, b := range entries[i].Blobs {
			if b.Len < 0 {
				continue
			}
			if nb, ok := moved[b]; ok {
				entries[i].Blobs[k] = nb
				continue
			}
			f := files[b.Seg]
			if f == nil {
				var err error
				if f, err = os.Open(filepath.Join(dir, filepath.FromSlash(archive.SegmentName(t.segs[b.Seg].SHA256)))); err != nil {
					return err
				}
				files[b.Seg] = f
			}
			buf := make([]byte, b.Len)
			if _, err := f.ReadAt(buf, b.Off); err != nil {
				return err
			}
			nb, err := w.append(buf)
			if err != nil {
				return err
			}
			moved[b] = nb
			entries[i].Blobs[k] = nb
		}
	}
	return w.finish()
}

// renumber lists the segments entries use, in a header, and points their
// blobs at them.
func renumber(entries []archive.Entry, t *segTable) (archive.Header, error) {
	var h archive.Header
	index := map[int]int{}
	for i := range entries {
		for k, b := range entries[i].Blobs {
			if b.Len < 0 {
				continue
			}
			n, ok := index[b.Seg]
			if !ok {
				s := t.segs[b.Seg]
				if s.SHA256 == "" {
					return h, errors.New("a segment wasn't finished")
				}
				n = len(h.Segments)
				h.Segments = append(h.Segments, s)
				index[b.Seg] = n
			}
			entries[i].Blobs[k].Seg = n
		}
	}
	return h, nil
}

// collect removes indexes and segments nothing's likely to want any more:
// indexes other than the current one, the Keep newest and those written
// within KeepFor; and segments none of those use, last changed before
// KeepFor (as are temporary files).
func collect(dir, current string, o Options) (int, error) {
	cutoff := o.Now().Add(-o.KeepFor)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	type idx struct {
		name string
		mod  time.Time
	}
	var idxs []idx
	removed := 0
	for _, d := range ents {
		name := d.Name()
		fi, err := d.Info()
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(name, "index-") && strings.HasSuffix(name, ".idx"):
			idxs = append(idxs, idx{name, fi.ModTime()})
		case strings.HasPrefix(name, ".tmp-") && fi.ModTime().Before(cutoff):
			if os.Remove(filepath.Join(dir, name)) == nil {
				removed++
			}
		}
	}
	sort.Slice(idxs, func(i, j int) bool { return idxs[i].mod.After(idxs[j].mod) })
	used := map[string]bool{}
	for i, x := range idxs {
		keep := x.name == archive.IndexName(current) || i < o.Keep || x.mod.After(cutoff)
		if !keep {
			if os.Remove(filepath.Join(dir, x.name)) == nil {
				removed++
			}
			continue
		}
		ix, err := archive.ReadIndex(filepath.Join(dir, x.name))
		if err != nil {
			if x.name == archive.IndexName(current) {
				return removed, err
			}
			continue // a broken old index protects nothing
		}
		for _, s := range ix.Segments {
			used[s.SHA256] = true
		}
	}
	segDir := filepath.Join(dir, archive.SegmentDir)
	sents, err := os.ReadDir(segDir)
	if err != nil {
		return removed, err
	}
	for _, d := range sents {
		name := d.Name()
		fi, err := d.Info()
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		sha, isSeg := strings.CutSuffix(name, ".seg")
		if (isSeg && !used[sha]) || strings.HasPrefix(name, ".tmp-") {
			if os.Remove(filepath.Join(segDir, name)) == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// lockFile takes an exclusive lock on a file, waiting for it.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
