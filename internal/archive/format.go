// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package archive keeps a published site (website and repository) in a
// few big files, made to be served from straight off a network file
// system: segments of the files' bytes, each encoding (identity, zstd,
// brotli, gzip) stored as it's sent; an index from paths to where those
// bytes are; and a pointer to the current index, which serving processes
// watch. A publish appends only the files that changed, so most of an
// archive is written once. See doc/archive-format.md.
package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Format is the archive format's version.
const Format = 1

// File names in an archive directory.
const (
	// CurrentName is the pointer to the current index.
	CurrentName = "current"
	// SegmentDir holds the segments.
	SegmentDir = "segments"
	lockName   = ".lock"
)

// IndexName is an index's file name, from its hash.
func IndexName(sha string) string { return "index-" + sha + ".idx" }

// SegmentName is a segment's path in an archive, from its hash.
func SegmentName(sha string) string { return SegmentDir + "/" + sha + ".seg" }

const (
	indexMagic   = "GPAIDX1\n"
	segmentMagic = "GPASEG1\n"
	// restartEvery is how often an index record has its whole path (the
	// rest share a prefix with the one before), so lookups can binary
	// search the records that do.
	restartEvery = 16
)

// Encodings are the forms a file's bytes are stored in, by their
// Content-Encoding names, identity ("") first.
var Encodings = []string{"", "zstd", "br", "gzip"}

// encodingBit is an encoding's bit in a record's mask, by name.
func encodingBit(name string) (int, bool) {
	for i, e := range Encodings {
		if e == name {
			return i, true
		}
	}
	return 0, false
}

// Hash is a file's identity: the first half of its SHA-256.
type Hash [16]byte

// HashOf is a file's Hash.
func HashOf(b []byte) Hash {
	sum := sha256.Sum256(b)
	var h Hash
	copy(h[:], sum[:16])
	return h
}

func (h Hash) String() string { return fmt.Sprintf("%x", h[:]) }

// Blob is where one encoding of a file is: a range of a segment (by its
// place in the index's list of segments).
type Blob struct {
	Seg      int
	Off, Len int64
}

// Entry is one file in an archive.
type Entry struct {
	Path string
	Hash Hash
	// Blobs, by encoding (as Encodings), Len -1 for those it hasn't.
	Blobs [4]Blob
}

// Has reports whether the entry has an encoding.
func (e *Entry) Has(encoding string) bool {
	i, ok := encodingBit(encoding)
	return ok && e.Blobs[i].Len >= 0
}

// Blob is an encoding's blob.
func (e *Entry) Blob(encoding string) (Blob, bool) {
	i, ok := encodingBit(encoding)
	if !ok || e.Blobs[i].Len < 0 {
		return Blob{}, false
	}
	return e.Blobs[i], true
}

func emptyBlobs() [4]Blob {
	var b [4]Blob
	for i := range b {
		b[i].Len = -1
	}
	return b
}

// Segment names a segment and its size.
type Segment struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Header is an index's metadata.
type Header struct {
	Format    int       `json:"format"`
	Generated time.Time `json:"generated"`
	// Segments are the segments entries' blobs are in.
	Segments []Segment `json:"segments"`
	Entries  int       `json:"entries"`
	// Live is how many bytes of the segments entries use (once each).
	Live int64 `json:"live"`
}

// Current is the pointer to the current index.
type Current struct {
	Format    int       `json:"format"`
	Index     string    `json:"index"`
	Generated time.Time `json:"generated"`
}

// Index is a loaded index: its header, and its records, sorted by path.
type Index struct {
	Header
	// SHA256 is the index file's hash (its name).
	SHA256   string
	records  []byte
	restarts []int
}

// encodeIndex writes an index's file: the magic, then zstd of the
// header's length and JSON, then the records.
func encodeIndex(h Header, entries []Entry) ([]byte, error) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	for i := 1; i < len(entries); i++ {
		if entries[i].Path == entries[i-1].Path {
			return nil, fmt.Errorf("%s twice", entries[i].Path)
		}
	}
	h.Format, h.Entries = Format, len(entries)
	hj, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	var tmp [binary.MaxVarintLen64]byte
	uv := func(v uint64) { body.Write(tmp[:binary.PutUvarint(tmp[:], v)]) }
	uv(uint64(len(hj)))
	body.Write(hj)
	prev := ""
	for i, e := range entries {
		shared := 0
		if i%restartEvery != 0 {
			for shared < len(prev) && shared < len(e.Path) && prev[shared] == e.Path[shared] {
				shared++
			}
		}
		uv(uint64(shared))
		uv(uint64(len(e.Path) - shared))
		body.WriteString(e.Path[shared:])
		body.Write(e.Hash[:])
		var mask byte
		for b, bl := range e.Blobs {
			if bl.Len >= 0 {
				mask |= 1 << b
			}
		}
		if mask&1 == 0 {
			return nil, fmt.Errorf("%s has no identity encoding", e.Path)
		}
		body.WriteByte(mask)
		for _, bl := range e.Blobs {
			if bl.Len >= 0 {
				uv(uint64(bl.Seg))
				uv(uint64(bl.Off))
				uv(uint64(bl.Len))
			}
		}
		prev = e.Path
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return append([]byte(indexMagic), enc.EncodeAll(body.Bytes(), nil)...), nil
}

var errBadIndex = errors.New("not an archive index")

// decodeIndex reads an index file.
func decodeIndex(b []byte) (*Index, error) {
	if !bytes.HasPrefix(b, []byte(indexMagic)) {
		return nil, errBadIndex
	}
	sum := sha256.Sum256(b)
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(4<<30))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	body, err := dec.DecodeAll(b[len(indexMagic):], nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errBadIndex, err)
	}
	hlen, n := binary.Uvarint(body)
	if n <= 0 || uint64(len(body)-n) < hlen {
		return nil, errBadIndex
	}
	ix := &Index{SHA256: fmt.Sprintf("%x", sum)}
	if err := json.Unmarshal(body[n:n+int(hlen)], &ix.Header); err != nil {
		return nil, fmt.Errorf("%w: %v", errBadIndex, err)
	}
	if ix.Format != Format {
		return nil, fmt.Errorf("archive index format %d, not %d", ix.Format, Format)
	}
	ix.records = body[n+int(hlen):]
	// Find the restart records, checking every record as we go.
	r := recordReader{b: ix.records}
	var e Entry
	for i := 0; r.pos < len(r.b); i++ {
		start := r.pos
		prevPath := e.Path
		if i%restartEvery == 0 {
			prevPath = ""
		}
		if err := r.next(&e, prevPath); err != nil {
			return nil, fmt.Errorf("%w: record %d: %v", errBadIndex, i, err)
		}
		if i%restartEvery == 0 {
			ix.restarts = append(ix.restarts, start)
		}
		for _, bl := range e.Blobs {
			if bl.Len >= 0 && (bl.Seg >= len(ix.Segments) || bl.Off < int64(len(segmentMagic)) || bl.Off+bl.Len > ix.Segments[bl.Seg].Bytes) {
				return nil, fmt.Errorf("%w: %s is outside its segment", errBadIndex, e.Path)
			}
		}
		if i+1 > ix.Entries {
			return nil, fmt.Errorf("%w: more records than it says", errBadIndex)
		}
	}
	return ix, nil
}

// recordReader decodes records one after another.
type recordReader struct {
	b   []byte
	pos int
}

func (r *recordReader) uvarint() (uint64, error) {
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 {
		return 0, errBadIndex
	}
	r.pos += n
	return v, nil
}

// next decodes the record at pos into e, whose path is prev's (the record
// before's) shared prefix and its own suffix.
func (r *recordReader) next(e *Entry, prev string) error {
	shared, err := r.uvarint()
	if err != nil {
		return err
	}
	slen, err := r.uvarint()
	if err != nil {
		return err
	}
	if shared > uint64(len(prev)) || uint64(len(r.b)-r.pos) < slen+uint64(len(Hash{}))+1 {
		return errBadIndex
	}
	e.Path = prev[:shared] + string(r.b[r.pos:r.pos+int(slen)])
	r.pos += int(slen)
	copy(e.Hash[:], r.b[r.pos:])
	r.pos += len(Hash{})
	mask := r.b[r.pos]
	r.pos++
	e.Blobs = emptyBlobs()
	for i := range e.Blobs {
		if mask&(1<<i) == 0 {
			continue
		}
		var v [3]uint64
		for k := range v {
			if v[k], err = r.uvarint(); err != nil {
				return err
			}
		}
		e.Blobs[i] = Blob{Seg: int(v[0]), Off: int64(v[1]), Len: int64(v[2])}
	}
	return nil
}

// Lookup finds a path's entry.
func (ix *Index) Lookup(path string) (Entry, bool) {
	var e Entry
	// The last restart at or before path.
	i := sort.Search(len(ix.restarts), func(i int) bool {
		r := recordReader{b: ix.records, pos: ix.restarts[i]}
		if err := r.next(&e, ""); err != nil {
			return true
		}
		return e.Path > path
	}) - 1
	if i < 0 {
		return Entry{}, false
	}
	r := recordReader{b: ix.records, pos: ix.restarts[i]}
	prev := ""
	for k := 0; k < restartEvery && r.pos < len(r.b); k++ {
		if err := r.next(&e, prev); err != nil {
			return Entry{}, false
		}
		switch strings.Compare(e.Path, path) {
		case 0:
			return e, true
		case 1:
			return Entry{}, false
		}
		prev = e.Path
	}
	return Entry{}, false
}

// Each calls fn for every entry, in path order, until it returns false.
func (ix *Index) Each(fn func(Entry) bool) {
	r := recordReader{b: ix.records}
	var e Entry
	prev := ""
	for r.pos < len(r.b) {
		if err := r.next(&e, prev); err != nil {
			return
		}
		if !fn(e) {
			return
		}
		prev = e.Path
	}
}

// readAllLimit reads r, failing past max bytes.
func readAllLimit(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("bigger than %d bytes", max)
	}
	return b, nil
}
