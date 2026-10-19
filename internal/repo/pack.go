// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Packs (see doc/repo-format.md): a log of puts and deletes to page/ and
// contents/, in a few big files, for mirroring a repository without a
// request for every file.

// PackLimit is how big a full pack gets.
const PackLimit = 256 << 20

// packWindow is the most zstd's window may be, for writers and readers.
const packWindow = 128 << 20

// PackEntry is a pack in a log, as manifest.json and packs/index.json list
// it.
type PackEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Seq    int    `json:"seq"`
	Tail   bool   `json:"tail"`
}

// PackHeader is a pack's pack.json: the files it puts follow it in the
// tar file (Puts is how many), and the paths it deletes are listed.
type PackHeader struct {
	Format    int       `json:"format"`
	Seq       int       `json:"seq"`
	Prev      string    `json:"prev"`
	Replaces  []string  `json:"replaces"`
	Generated time.Time `json:"generated"`
	Puts      int       `json:"puts"`
	Deletes   []string  `json:"deletes"`
}

// PackLog is a log's packs/index.json: the packs in the log, in order,
// and replaced ones not yet removed.
type PackLog struct {
	Format  int         `json:"format"`
	Packs   []PackEntry `json:"packs"`
	Retired []Retired   `json:"retired,omitempty"`
}

// Retired is a replaced pack, kept for a while for clients part of the way
// through fetching it.
type Retired struct {
	Path string    `json:"path"`
	At   time.Time `json:"at"`
}

// retireFor is how long a replaced pack stays.
const retireFor = 24 * time.Hour

// PackLogPath is a log's packs/index.json, from the root it's in.
func PackLogPath() string { return "packs/index.json" }

// PackPath is where a pack is, from its log's root.
func PackPath(sha string) string { return "packs/" + sha + ".tar.zst" }

// ReadPackLog reads the log in root (a repository, or a pack-only one),
// which is empty if there isn't one.
func ReadPackLog(root string) (*PackLog, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(PackLogPath())))
	if errors.Is(err, fs.ErrNotExist) {
		return &PackLog{Format: Format}, nil
	}
	if err != nil {
		return nil, err
	}
	var l PackLog
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", PackLogPath(), err)
	}
	return &l, nil
}

func writePackLog(root string, l *PackLog) error {
	b, err := Encode(l)
	if err != nil {
		return err
	}
	return WriteFile(filepath.Join(root, filepath.FromSlash(PackLogPath())), b)
}

// State is what's in page/ and contents/: each file's path and hash.
type State map[string]string

// packed reports whether a file (by its path from the root) is one packs
// hold: in page/ or contents/, a JSON document, and not one being written.
func packed(rel string) bool {
	return (strings.HasPrefix(rel, "page/") || strings.HasPrefix(rel, "contents/")) &&
		strings.HasSuffix(rel, ".json") && !strings.HasPrefix(path.Base(rel), ".tmp-")
}

var (
	listingName = regexp.MustCompile(`^page/page-[0-9a-f]{2}/([0-9a-f]{64})\.json$`)
	variantName = regexp.MustCompile(`^contents/[0-9a-f]{2}/[0-9a-f]{64}/[a-z][a-z0-9-]*-([0-9a-f]{64})\.json$`)
)

// selfHash is a file's hash, if its name says it.
func selfHash(rel string) (string, bool) {
	if m := listingName.FindStringSubmatch(rel); m != nil {
		return m[1], true
	}
	if m := variantName.FindStringSubmatch(rel); m != nil {
		return m[1], true
	}
	return "", false
}

// ScanState reads what's in the repository in dir: the hashes of files
// named after them from their names, and the others' by reading them.
func ScanState(ctx context.Context, dir string) (State, error) {
	var roots []string
	for _, top := range []string{"page", "contents"} {
		ents, err := os.ReadDir(filepath.Join(dir, top))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if e.IsDir() {
				roots = append(roots, top+"/"+e.Name())
			}
		}
	}
	st := State{}
	var mu sync.Mutex
	errs := make([]error, len(roots))
	parallel(len(roots), func(i int) {
		if ctx.Err() != nil {
			return
		}
		local := map[string]string{}
		errs[i] = filepath.WalkDir(filepath.Join(dir, filepath.FromSlash(roots[i])), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			rel = filepath.ToSlash(rel)
			if !packed(rel) {
				return nil
			}
			if sha, ok := selfHash(rel); ok {
				local[rel] = sha
				return nil
			}
			b, err := os.ReadFile(p)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			local[rel] = Hash(b)
			return nil
		})
		mu.Lock()
		for k, v := range local {
			st[k] = v
		}
		mu.Unlock()
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return st, errors.Join(errs...)
}

// LogState is what replaying packs (in a log in root) gives.
func LogState(ctx context.Context, root string, packs []PackEntry) (State, error) {
	st := State{}
	for _, e := range packs {
		if err := readPack(ctx, filepath.Join(root, filepath.FromSlash(e.Path)), func(h *PackHeader) error {
			for _, d := range h.Deletes {
				delete(st, d)
			}
			return nil
		}, func(path string, data []byte) error {
			st[path] = Hash(data)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// diff is what changes old into new: paths to put and paths to delete.
func diff(old, new State) (puts, deletes []string) {
	for p, sha := range new {
		if old[p] != sha {
			puts = append(puts, p)
		}
	}
	for p := range old {
		if _, ok := new[p]; !ok {
			deletes = append(deletes, p)
		}
	}
	sort.Strings(puts)
	sort.Strings(deletes)
	return puts, deletes
}

// orderPuts orders the paths to put page by page, so near-copies are
// together: each pointer, its listing, then each of its versions'
// variants.json and variants; then whatever's left.
func orderPuts(dir string, puts []string) []string {
	want := make(map[string]bool, len(puts))
	byDir := map[string][]string{}
	var pointers []string
	for _, p := range puts {
		want[p] = true
		switch {
		case strings.HasPrefix(p, "page/name-"):
			pointers = append(pointers, p)
		case strings.HasPrefix(p, "contents/"):
			d := path.Dir(p)
			byDir[d] = append(byDir[d], p)
		}
	}
	for _, ps := range byDir {
		// variants.json first, then the variants.
		sort.Slice(ps, func(i, j int) bool {
			if (path.Base(ps[i]) == "variants.json") != (path.Base(ps[j]) == "variants.json") {
				return path.Base(ps[i]) == "variants.json"
			}
			return ps[i] < ps[j]
		})
	}
	out := make([]string, 0, len(puts))
	used := make(map[string]bool, len(puts))
	emit := func(p string) {
		if want[p] && !used[p] {
			used[p] = true
			out = append(out, p)
		}
	}
	read := func(rel string, v any) bool {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		return err == nil && json.Unmarshal(b, v) == nil
	}
	for _, p := range pointers {
		emit(p)
		var ptr Pointer
		if !read(p, &ptr) || !ValidHash(ptr.SHA256) {
			continue
		}
		lp := ListingPath(ptr.SHA256)
		emit(lp)
		var l Listing
		if !read(lp, &l) {
			continue
		}
		for _, v := range l.Versions {
			if !ValidHash(v.Source) {
				continue
			}
			for _, c := range byDir[path.Dir(ContentsPath(v.Source))] {
				emit(c)
			}
		}
	}
	for _, p := range puts {
		emit(p)
	}
	return out
}

// countWriter counts what's written through it.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

func newPackEncoder(w io.Writer) (*zstd.Encoder, error) {
	return zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithWindowSize(packWindow), zstd.WithEncoderConcurrency(1))
}

// epoch is the time tar members say they're from: packs are the same
// whenever they're made.
var epoch = time.Unix(0, 0).UTC()

// packBatch writes packs of the puts (in order) and deletes, read from
// the repository in dir, into the log in root: full packs as each comes
// to limit, and the rest in a last one, a tail if tail. The first pack
// has seq and prev, the deletes, and says it replaces replaces.
type packBatch struct {
	root, dir string
	limit     int64
	seq       int
	prev      string
	replaces  []string
	tail      bool
	// progress, if set, is told how many puts are written of how many.
	progress func(done, total int)
	// written, if set, is given each file written, and its hash.
	written State
}

// write writes the packs, returning their entries.
func (b *packBatch) write(ctx context.Context, puts, deletes []string) ([]PackEntry, error) {
	packs := filepath.Join(b.root, "packs")
	if err := os.MkdirAll(packs, 0o755); err != nil {
		return nil, err
	}
	var out []PackEntry
	var (
		body     *os.File
		count    *countWriter
		enc      *zstd.Encoder
		tw       *tar.Writer
		header   PackHeader
		headerSz int64
		// pending is how much has gone in since the encoder last wrote
		// anything out: it holds a block at a time.
		pending, seen int64
	)
	slack := min(b.limit/8, 1<<20)
	start := func() error {
		f, err := os.CreateTemp(packs, ".tmp-body-*")
		if err != nil {
			return err
		}
		body, count = f, &countWriter{w: f}
		if enc, err = newPackEncoder(count); err != nil {
			return err
		}
		tw = tar.NewWriter(enc)
		header = PackHeader{Format: Format, Seq: b.seq, Prev: b.prev, Replaces: []string{}, Deletes: []string{}}
		headerSz, pending, seen = 0, 0, 0
		if len(out) == 0 {
			header.Replaces = append(header.Replaces, b.replaces...)
			header.Deletes = append(header.Deletes, deletes...)
			for _, d := range deletes {
				headerSz += int64(len(d)) + 3
			}
		}
		return nil
	}
	finish := func(tail bool) error {
		if err := tw.Close(); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		defer os.Remove(body.Name())
		defer body.Close()
		header.Generated = now()
		e, err := b.assemble(&header, body)
		if err != nil {
			return err
		}
		e.Tail = tail
		out = append(out, e)
		b.seq++
		b.prev = e.SHA256
		return nil
	}
	if err := start(); err != nil {
		return nil, err
	}
	for i, p := range puts {
		if err := ctx.Err(); err != nil {
			body.Close()
			os.Remove(body.Name())
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(b.dir, filepath.FromSlash(p)))
		if errors.Is(err, fs.ErrNotExist) {
			continue // gone since: the next pack will say so
		}
		if err != nil {
			return nil, err
		}
		if err := tw.WriteHeader(&tar.Header{Name: p, Mode: 0o644, Size: int64(len(data)), ModTime: epoch, Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
		header.Puts++
		if b.written != nil {
			b.written[p] = Hash(data)
		}
		if count.n != seen {
			seen, pending = count.n, 0
		}
		pending += int64(len(data)) + 512
		if b.progress != nil {
			b.progress(i+1, len(puts))
		}
		// How big it'd be: what's written, what the encoder holds (at a
		// guess, a little worse than these files go), and the header
		// (deletes, which compress to a third or so), with room to spare.
		if count.n+pending/8+headerSz/3+slack >= b.limit && i+1 < len(puts) {
			if err := finish(false); err != nil {
				return nil, err
			}
			if err := start(); err != nil {
				return nil, err
			}
		}
	}
	if err := finish(b.tail); err != nil {
		return nil, err
	}
	return out, nil
}

// assemble writes a pack: its header (pack.json, a frame of its own) then
// the body (the puts, compressed already), naming it after its hash.
func (b *packBatch) assemble(h *PackHeader, body *os.File) (PackEntry, error) {
	hj, err := Encode(h)
	if err != nil {
		return PackEntry{}, err
	}
	var head bytes.Buffer
	enc, err := newPackEncoder(&head)
	if err != nil {
		return PackEntry{}, err
	}
	tw := tar.NewWriter(enc)
	if err := tw.WriteHeader(&tar.Header{Name: "pack.json", Mode: 0o644, Size: int64(len(hj)), ModTime: epoch, Typeflag: tar.TypeReg}); err != nil {
		return PackEntry{}, err
	}
	if _, err := tw.Write(hj); err != nil {
		return PackEntry{}, err
	}
	// Flush, not Close: the body goes on from here, and ends the archive.
	if err := tw.Flush(); err != nil {
		return PackEntry{}, err
	}
	if err := enc.Close(); err != nil {
		return PackEntry{}, err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return PackEntry{}, err
	}
	f, err := os.CreateTemp(filepath.Join(b.root, "packs"), ".tmp-pack-*")
	if err != nil {
		return PackEntry{}, err
	}
	defer os.Remove(f.Name())
	sum := sha256.New()
	w := io.MultiWriter(f, sum)
	n1, err := w.Write(head.Bytes())
	if err != nil {
		f.Close()
		return PackEntry{}, err
	}
	n2, err := io.Copy(w, body)
	if err != nil {
		f.Close()
		return PackEntry{}, err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return PackEntry{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return PackEntry{}, err
	}
	if err := f.Close(); err != nil {
		return PackEntry{}, err
	}
	sha := hex.EncodeToString(sum.Sum(nil))
	rel := PackPath(sha)
	if err := os.Rename(f.Name(), filepath.Join(b.root, filepath.FromSlash(rel))); err != nil {
		return PackEntry{}, err
	}
	return PackEntry{Path: rel, SHA256: sha, Bytes: int64(n1) + n2, Seq: h.Seq}, nil
}

// openPack opens a pack to read: its header, and a tar reader at the
// first put. Close closes it.
type packReader struct {
	Header *PackHeader
	f      *os.File
	dec    *zstd.Decoder
	tr     *tar.Reader
}

func openPack(file string) (*packReader, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(f, zstd.WithDecoderMaxWindow(packWindow), zstd.WithDecoderConcurrency(1))
	if err != nil {
		f.Close()
		return nil, err
	}
	r := &packReader{f: f, dec: dec, tr: tar.NewReader(dec)}
	hdr, err := r.tr.Next()
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("%s: %w", filepath.Base(file), err)
	}
	if hdr.Name != "pack.json" {
		r.Close()
		return nil, fmt.Errorf("%s: it starts with %q, not pack.json", filepath.Base(file), hdr.Name)
	}
	var h PackHeader
	if err := json.NewDecoder(io.LimitReader(r.tr, 1<<30)).Decode(&h); err != nil {
		r.Close()
		return nil, fmt.Errorf("%s: pack.json: %w", filepath.Base(file), err)
	}
	if h.Format > Format {
		r.Close()
		return nil, fmt.Errorf("%s is format %d; this girlpages reads format %d", filepath.Base(file), h.Format, Format)
	}
	r.Header = &h
	return r, nil
}

func (r *packReader) Close() {
	r.dec.Close()
	r.f.Close()
}

// ReadPackHeader reads a pack's header.
func ReadPackHeader(file string) (*PackHeader, error) {
	r, err := openPack(file)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.Header, nil
}

// readPack reads a pack: its header (to header), then each file it puts
// (to put), checking each is a repository file, put once, and if it's
// named after its hash, has it; and that there are as many as the header
// says. (The pack as a whole is checked by its own hash, when it's
// fetched.)
func readPack(ctx context.Context, file string, header func(*PackHeader) error, put func(path string, data []byte) error) error {
	r, err := openPack(file)
	if err != nil {
		return err
	}
	defer r.Close()
	name := filepath.Base(file)
	h := r.Header
	for _, d := range h.Deletes {
		if !packed(d) || !safePath(d) {
			return fmt.Errorf("%s deletes %q, which isn't a repository file", name, d)
		}
	}
	if err := header(h); err != nil {
		return err
	}
	seen := map[string]bool{}
	for n := 0; ; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := r.tr.Next()
		if err == io.EOF {
			if n != h.Puts {
				return fmt.Errorf("%s has %d files, but says it has %d", name, n, h.Puts)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		p := hdr.Name
		if hdr.Typeflag != tar.TypeReg || !packed(p) || !safePath(p) || seen[p] {
			return fmt.Errorf("%s puts %q, which isn't a repository file (or puts it twice)", name, p)
		}
		seen[p] = true
		if hdr.Size > maxDocument {
			return fmt.Errorf("%s: %s is %d bytes", name, p, hdr.Size)
		}
		data, err := io.ReadAll(io.LimitReader(r.tr, hdr.Size))
		if err != nil {
			return fmt.Errorf("%s: %s: %w", name, p, err)
		}
		if sha, ok := selfHash(p); ok && Hash(data) != sha {
			return fmt.Errorf("%s: %s doesn't match its hash", name, p)
		}
		if err := put(p, data); err != nil {
			return err
		}
	}
}

// applyPack replays a pack into the files in dir, returning its header
// and the paths it put.
func applyPack(ctx context.Context, file, dir string) (*PackHeader, []string, error) {
	var h *PackHeader
	var puts []string
	err := readPack(ctx, file, func(ph *PackHeader) error {
		h = ph
		for _, d := range ph.Deletes {
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(d))); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return nil
	}, func(p string, data []byte) error {
		puts = append(puts, p)
		return WriteFile(filepath.Join(dir, filepath.FromSlash(p)), data)
	})
	return h, puts, err
}

// safePath reports whether a path stays inside the root: no .. or empty
// parts, and not absolute.
func safePath(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// PackOptions control AppendPacks.
type PackOptions struct {
	// Limit is how big full packs get (PackLimit if 0).
	Limit int64
	// Progress, if set, is told how the packing is going.
	Progress Progress
}

// PackStats say what AppendPacks did.
type PackStats struct {
	// Packs are the packs it wrote; Replaced, the tail packs they replace.
	Packs    []PackEntry
	Replaced []PackEntry
	// Puts and Deletes are how many changes they hold.
	Puts, Deletes int
}

// tailRatio is a rough guess at how much the files in a tail pack
// compress, for deciding whether they'd make a full pack, without
// compressing them to find out.
const tailRatio = 12

// AppendPacks brings the log in root up to date with the repository in
// dir (which may be the same): what's changed since the log's last pack
// goes in a new tail pack, unless the tail packs would then come to a
// full pack, in which case they're replaced by full packs (and a new tail)
// of everything since the last full pack. Replaced packs stay a day, then
// go. It doesn't touch dir, and takes root's packs/.lock while it works.
func AppendPacks(ctx context.Context, root, dir string, o PackOptions) (PackStats, error) {
	var st PackStats
	if o.Limit <= 0 {
		o.Limit = PackLimit
	}
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
	report := func(stage string, done, total int) {
		if o.Progress != nil {
			o.Progress(stage, done, total)
		}
	}

	report("reading the repository's files", 0, 0)
	cur, err := ScanState(ctx, dir)
	if err != nil {
		return st, err
	}
	// The full packs, then the tail packs after them.
	nFull := len(log.Packs)
	for nFull > 0 && log.Packs[nFull-1].Tail {
		nFull--
	}
	// What the log holds: as the last append left it, if it's cached,
	// or else from the packs themselves.
	var all State
	if n := len(log.Packs); n > 0 {
		all = readStateCache(root, log.Packs[n-1].SHA256)
	} else {
		all = State{}
	}
	if all == nil {
		report("reading the packs", 0, 0)
		if all, err = LogState(ctx, root, log.Packs); err != nil {
			return st, err
		}
	}
	puts, deletes := diff(all, cur)
	if len(puts) == 0 && len(deletes) == 0 {
		if n := len(log.Packs); n > 0 {
			writeStateCache(root, log.Packs[n-1].SHA256, all)
		}
		return st, retire(root, log, false)
	}

	// Would a tail of these, and the tail there is, come to a full pack?
	var tailBytes, raw int64
	for _, e := range log.Packs[nFull:] {
		tailBytes += e.Bytes
	}
	for _, p := range puts {
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err == nil {
			raw += fi.Size()
		}
	}
	b := &packBatch{root: root, dir: dir, limit: o.Limit, tail: true, written: State{},
		progress: func(done, total int) { report("packing", done, total) }}
	// What the log will hold, once these are added: from where it's to
	// go on from (all, or base when replacing tails), with these.
	from := all
	if last := len(log.Packs); last > 0 {
		b.seq, b.prev = log.Packs[last-1].Seq+1, log.Packs[last-1].SHA256
	} else {
		b.seq = 1
	}
	if tailBytes+raw/tailRatio >= o.Limit {
		// Everything since the last full pack, as full packs and a tail.
		base := all
		if nFull < len(log.Packs) {
			report("reading the packs", 0, 0)
			if base, err = LogState(ctx, root, log.Packs[:nFull]); err != nil {
				return st, err
			}
		}
		from = base
		puts, deletes = diff(base, cur)
		st.Replaced = append([]PackEntry(nil), log.Packs[nFull:]...)
		for _, e := range st.Replaced {
			b.replaces = append(b.replaces, e.SHA256)
		}
		if nFull > 0 {
			b.seq, b.prev = log.Packs[nFull-1].Seq+1, log.Packs[nFull-1].SHA256
		} else {
			b.seq, b.prev = 1, ""
		}
	}
	report("packing", 0, len(puts))
	written, err := b.write(ctx, orderPuts(dir, puts), deletes)
	if err != nil {
		return st, err
	}
	st.Packs, st.Puts, st.Deletes = written, len(puts), len(deletes)
	if len(st.Replaced) > 0 {
		log.Packs = log.Packs[:nFull]
		for _, e := range st.Replaced {
			log.Retired = append(log.Retired, Retired{Path: e.Path, At: now()})
		}
	}
	log.Packs = append(log.Packs, written...)
	log.Format = Format
	next := State{}
	for p, sha := range from {
		next[p] = sha
	}
	for _, d := range deletes {
		delete(next, d)
	}
	for p, sha := range b.written {
		next[p] = sha
	}
	if err := retire(root, log, true); err != nil {
		return st, err
	}
	writeStateCache(root, log.Packs[len(log.Packs)-1].SHA256, next)
	return st, nil
}

// The state of a log as of a pack, cached (in packs/.state-<sha>.zst) so
// the next append needn't read every pack: a line for each file, its path
// and hash (empty, for files named after it).

func stateCachePath(root, sha string) string {
	return filepath.Join(root, "packs", ".state-"+sha+".zst")
}

func writeStateCache(root, sha string, st State) {
	paths := make([]string, 0, len(st))
	for p := range st {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return
	}
	for _, p := range paths {
		sha := st[p]
		if self, ok := selfHash(p); ok && self == sha {
			sha = ""
		}
		fmt.Fprintf(enc, "%s\t%s\n", p, sha)
	}
	if enc.Close() != nil || WriteFile(stateCachePath(root, sha), buf.Bytes()) != nil {
		return
	}
	// Only the latest is any use.
	ents, _ := os.ReadDir(filepath.Join(root, "packs"))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".state-") && e.Name() != filepath.Base(stateCachePath(root, sha)) {
			os.Remove(filepath.Join(root, "packs", e.Name()))
		}
	}
}

// readStateCache reads the cached state as of a pack, or nil if there
// isn't one (or it can't be read).
func readStateCache(root, sha string) State {
	b, err := os.ReadFile(stateCachePath(root, sha))
	if err != nil {
		return nil
	}
	dec, err := zstd.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	defer dec.Close()
	raw, err := io.ReadAll(dec)
	if err != nil {
		return nil
	}
	st := State{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		p, sha, ok := strings.Cut(line, "\t")
		if !ok {
			return nil
		}
		if sha == "" {
			if sha, ok = selfHash(p); !ok {
				return nil
			}
		}
		st[p] = sha
	}
	return st
}

// retire removes replaced packs that have been kept long enough, and
// pack files being written by runs that didn't finish, then writes the
// log if anything changed (or changed is set).
func retire(root string, log *PackLog, changed bool) error {
	keep := log.Retired[:0]
	for _, r := range log.Retired {
		if now().Sub(r.At) < retireFor {
			keep = append(keep, r)
			continue
		}
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(r.Path))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		changed = true
	}
	log.Retired = keep
	ents, _ := os.ReadDir(filepath.Join(root, "packs"))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > time.Hour {
				os.Remove(filepath.Join(root, "packs", e.Name()))
			}
		}
	}
	if !changed {
		return nil
	}
	return writePackLog(root, log)
}
