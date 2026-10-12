// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package precompress writes compressed copies of a static site's files
// next to them (index.html.br, manifest.json.zst, …) for web servers that
// serve those directly when a client accepts them: static-web-server with
// --compression-static, nginx with gzip_static, brotli_static and
// zstd_static. Files are compressed as hard as the formats go, once: only
// new or changed files are compressed again.
package precompress

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// An Encoding is a compression format a web server can serve.
type Encoding struct {
	// Name is the Content-Encoding (and Accept-Encoding) token.
	Name string
	// Ext is the file name suffix, with its dot.
	Ext string
	// Compress compresses src.
	Compress func(src []byte) ([]byte, error)
	// Reader decompresses a stream.
	Reader func(io.Reader) (io.ReadCloser, error)
}

// zstdEncoder is shared: EncodeAll may be called from many goroutines.
// Site files are small, so a 1 MiB window loses nothing and keeps memory
// down.
var zstdEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithEncoderConcurrency(workers()), zstd.WithWindowSize(1<<20))
})

// Workers is how many files are compressed at once: one per CPU, unless
// it's set. Each takes tens of megabytes at most (windows are fitted to
// files' sizes).
var Workers = 0

func workers() int {
	if Workers > 0 {
		return Workers
	}
	return runtime.NumCPU()
}

// brotliWindow is the smallest window (as a power of two, from 2^16 to
// 2^24) that holds n bytes: brotli's biggest would cost memory for nothing
// on small files.
func brotliWindow(n int) int {
	w := 16
	for w < 24 && 1<<w < n {
		w++
	}
	return w
}

// Encodings are the formats this package knows, in the order a client
// should prefer them.
var Encodings = []Encoding{
	{Name: "zstd", Ext: ".zst",
		Compress: func(src []byte) ([]byte, error) {
			enc, err := zstdEncoder()
			if err != nil {
				return nil, err
			}
			return enc.EncodeAll(src, nil), nil
		},
		Reader: func(r io.Reader) (io.ReadCloser, error) {
			d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
			if err != nil {
				return nil, err
			}
			return d.IOReadCloser(), nil
		}},
	{Name: "br", Ext: ".br",
		Compress: func(src []byte) ([]byte, error) {
			var b bytes.Buffer
			w := brotli.NewWriterOptions(&b, brotli.WriterOptions{Quality: brotli.BestCompression, LGWin: brotliWindow(len(src))})
			if _, err := w.Write(src); err != nil {
				return nil, err
			}
			err := w.Close()
			return b.Bytes(), err
		},
		Reader: func(r io.Reader) (io.ReadCloser, error) { return io.NopCloser(brotli.NewReader(r)), nil }},
	{Name: "gzip", Ext: ".gz",
		Compress: func(src []byte) ([]byte, error) {
			var b bytes.Buffer
			w, err := gzip.NewWriterLevel(&b, gzip.BestCompression)
			if err != nil {
				return nil, err
			}
			if _, err := w.Write(src); err != nil {
				return nil, err
			}
			err = w.Close()
			return b.Bytes(), err
		},
		Reader: func(r io.Reader) (io.ReadCloser, error) { return gzip.NewReader(r) }},
}

// Lookup finds an encoding by its name.
func Lookup(name string) (Encoding, bool) {
	for _, e := range Encodings {
		if e.Name == name {
			return e, true
		}
	}
	return Encoding{}, false
}

// Parse reads a comma-separated list of encoding names ("zstd,br,gzip";
// "none" or "" for none).
func Parse(list string) ([]Encoding, error) {
	var out []Encoding
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" || name == "none" {
			continue
		}
		switch name {
		case "br", "brotli":
			name = "br"
		case "zst":
			name = "zstd"
		case "gz":
			name = "gzip"
		}
		e, ok := Lookup(name)
		if !ok {
			return nil, fmt.Errorf("can't compress with %q; try zstd, br or gzip", name)
		}
		out = append(out, e)
	}
	return out, nil
}

// worth says which files are worth compressing: text that web servers
// send a lot of.
var worth = map[string]bool{".json": true, ".html": true, ".js": true, ".css": true, ".xml": true,
	".svg": true, ".txt": true, ".webmanifest": true}

// MinSize is the smallest file worth compressing: below it, the headers
// cost more than compression saves.
const MinSize = 256

// Stats counts what a Sweep did.
type Stats struct {
	Compressed, Unchanged, Removed int
	// Bytes is the size of the originals compressed, and Saved how much
	// smaller the smallest copies are.
	Bytes, Saved int64
}

// IsCopy reports whether a file name is a compressed copy of another (by
// any of Encodings), returning the original's name.
func IsCopy(name string) (string, bool) {
	for _, e := range Encodings {
		if base, ok := strings.CutSuffix(name, e.Ext); ok && worth[filepath.Ext(base)] {
			return base, true
		}
	}
	return "", false
}

// Sweep brings the compressed copies under root up to date: making them
// for files worth it that don't have current ones (a copy is current if
// it's no older than its original), and removing copies whose originals
// are gone, or that encodings no longer includes.
func Sweep(root string, encodings []Encoding) (Stats, error) {
	return SweepProgress(root, encodings, nil)
}

// SweepProgress is Sweep, telling progress (if not nil) how many files
// it's compressed of how many.
func SweepProgress(root string, encodings []Encoding, progress func(done, total int)) (Stats, error) {
	q := NewQueue(encodings)
	q.Progress = progress
	err := q.Stale(root)
	st, qerr := q.Wait()
	return st, errors.Join(err, qerr)
}

// Queue compresses files as they're handed to it, one per worker at a
// time, so that compressing goes on while files are still being written.
type Queue struct {
	enc  []Encoding
	want map[string]bool
	ch   chan string
	wg   sync.WaitGroup

	mu   sync.Mutex
	st   Stats
	errs []error
	// queued and done count files, for Progress.
	queued, done atomic.Int64
	// Progress, if set, is told how many files have been compressed of
	// how many queued so far.
	Progress func(done, queued int)
}

// NewQueue starts compressing (with Workers workers) whatever's added.
func NewQueue(encodings []Encoding) *Queue {
	q := &Queue{enc: encodings, want: map[string]bool{}, ch: make(chan string, 1<<16)}
	for _, e := range encodings {
		q.want[e.Ext] = true
	}
	for w := 0; w < workers(); w++ {
		q.wg.Add(1)
		go q.work()
	}
	return q
}

// Add queues a file that's been written, if it's the kind worth
// compressing.
func (q *Queue) Add(path string) {
	if len(q.enc) == 0 || !worth[filepath.Ext(path)] || strings.HasPrefix(filepath.Base(path), ".tmp-") {
		return
	}
	q.queued.Add(1)
	q.ch <- path
}

func (q *Queue) work() {
	defer q.wg.Done()
	for path := range q.ch {
		did, size, saved, err := compress(path, q.enc)
		q.mu.Lock()
		switch {
		case err != nil && !errors.Is(err, fs.ErrNotExist): // (gone since: nothing to do)
			q.errs = append(q.errs, err)
		case did:
			q.st.Compressed++
			q.st.Bytes += size
			q.st.Saved += saved
		case err == nil:
			q.st.Unchanged++
		}
		q.mu.Unlock()
		n := q.done.Add(1)
		if p := q.Progress; p != nil {
			p(int(n), int(q.queued.Load()))
		}
	}
}

// Stale goes through the files under root, queueing those whose copies
// are missing or older than they are, and removing copies whose originals
// are gone, or that the encodings no longer include.
func (q *Queue) Stale(root string) error {
	return walkDirs(root, func(dir string, ents []fs.DirEntry) error {
		byName := make(map[string]fs.DirEntry, len(ents))
		for _, d := range ents {
			byName[d.Name()] = d
		}
		for _, d := range ents {
			name := d.Name()
			if d.IsDir() || strings.HasPrefix(name, ".tmp-") {
				continue
			}
			path := filepath.Join(dir, name)
			if base, ok := IsCopy(name); ok {
				if b, ok := byName[base]; !ok || b.IsDir() || !q.want[strings.TrimPrefix(name, base)] {
					if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
						return err
					}
					q.mu.Lock()
					q.st.Removed++
					q.mu.Unlock()
				}
				continue
			}
			if worth[filepath.Ext(name)] && len(q.enc) > 0 && q.stale(name, d, byName) {
				q.Add(path)
			}
		}
		return nil
	})
}

// stale reports whether a file's copies need making, going by its
// directory's entries.
func (q *Queue) stale(name string, d fs.DirEntry, byName map[string]fs.DirEntry) bool {
	fi, err := d.Info()
	if err != nil {
		return false
	}
	if fi.Size() < MinSize {
		// Too small to compress; copies of it go (compress does that).
		for _, e := range q.enc {
			if _, ok := byName[name+e.Ext]; ok {
				return true
			}
		}
		return false
	}
	for _, e := range q.enc {
		c, ok := byName[name+e.Ext]
		if !ok {
			return true
		}
		cfi, err := c.Info()
		if err != nil || cfi.ModTime().Before(fi.ModTime()) {
			return true
		}
	}
	return false
}

// walkDirs calls fn with each directory under root (root included) and
// its entries, a directory per CPU at a time, stopping at the first error.
// Directories that vanish meanwhile are skipped.
func walkDirs(root string, fn func(dir string, ents []fs.DirEntry) error) error {
	var (
		mu    sync.Mutex
		cond  = sync.NewCond(&mu)
		todo  = []string{root}
		busy  int
		first error
		wg    sync.WaitGroup
	)
	for range workers() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				for len(todo) == 0 && busy > 0 && first == nil {
					cond.Wait()
				}
				if len(todo) == 0 || first != nil {
					mu.Unlock()
					cond.Broadcast()
					return
				}
				dir := todo[len(todo)-1]
				todo = todo[:len(todo)-1]
				busy++
				mu.Unlock()

				ents, err := os.ReadDir(dir)
				if errors.Is(err, fs.ErrNotExist) {
					ents, err = nil, nil
				}
				var subs []string
				if err == nil {
					for _, d := range ents {
						if d.IsDir() {
							subs = append(subs, filepath.Join(dir, d.Name()))
						}
					}
					err = fn(dir, ents)
				}

				mu.Lock()
				todo = append(todo, subs...)
				busy--
				if err != nil && first == nil {
					first = err
				}
				mu.Unlock()
				cond.Broadcast()
			}
		}()
	}
	wg.Wait()
	return first
}

// Wait finishes what's queued (nothing more may be added) and reports.
func (q *Queue) Wait() (Stats, error) {
	close(q.ch)
	q.wg.Wait()
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.st, errors.Join(q.errs...)
}

// compress writes a file's copies if any is missing or older than it. A
// copy that wouldn't be smaller isn't written (and an old one goes), so a
// server sends the original instead.
func compress(path string, encodings []Encoding) (bool, int64, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, 0, 0, err
	}
	stale := false
	for _, e := range encodings {
		cfi, err := os.Stat(path + e.Ext)
		if err != nil || cfi.ModTime().Before(fi.ModTime()) {
			stale = true
			break
		}
	}
	if !stale {
		return false, 0, 0, nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return false, 0, 0, err
	}
	var best int64 = int64(len(src))
	for _, e := range encodings {
		out := path + e.Ext
		if len(src) < MinSize {
			_ = os.Remove(out)
			continue
		}
		data, err := e.Compress(src)
		if err != nil {
			return false, 0, 0, fmt.Errorf("%s: %s: %w", path, e.Name, err)
		}
		if len(data) >= len(src) {
			_ = os.Remove(out)
			continue
		}
		if err := writeFile(out, data, fi); err != nil {
			return false, 0, 0, err
		}
		best = min(best, int64(len(data)))
	}
	return true, int64(len(src)), int64(len(src)) - best, nil
}

// writeFile writes a copy atomically, with its original's permissions.
func writeFile(path string, data []byte, orig fs.FileInfo) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(orig.Mode().Perm()); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
