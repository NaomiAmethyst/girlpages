// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package archive

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// table is an index as a server keeps it in memory: just what serving
// needs (no content hashes), as small as it'll go. Records are in the
// index's (path) order, each:
//
//	shared   varint: bytes the same as the record before's key
//	length   varint, then that many bytes of key
//	mask     byte: the forms it has (as in the index)
//	seg off  varints: the file itself's segment and offset
//	length   varint: its length
//	for each other form: varint (length<<1 | 1) if it follows the form
//	before in the same segment, else (length<<1), seg, off
//
// A key is the path, but with each run of exactly 64 lower-case hex
// digits (the hashes most paths are named by) as a zero byte and the 32
// bytes they spell. Every 16th record has shared 0.
type table struct {
	data     []byte
	restarts []uint32
}

// hexRun finds the next run of exactly 64 hex digits in s, from i: its
// start, or -1.
func hexRun(s string, i int) int {
	for i < len(s) {
		if !isHex(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isHex(s[j]) {
			j++
		}
		if j-i == 64 {
			return i
		}
		i = j
	}
	return -1
}

func isHex(c byte) bool { return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' }

// key encodes a path as a table key (appending to dst).
func key(dst []byte, path string) []byte {
	for {
		i := hexRun(path, 0)
		if i < 0 {
			return append(dst, path...)
		}
		dst = append(dst, path[:i]...)
		dst = append(dst, 0)
		var raw [32]byte
		for k := 0; k < 32; k++ {
			raw[k] = unhex(path[i+2*k])<<4 | unhex(path[i+2*k+1])
		}
		dst = append(dst, raw[:]...)
		path = path[i+64:]
	}
}

func unhex(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return c - 'a' + 10
}

const hexDigits = "0123456789abcdef"

// unkey decodes a table key back to its path (appending to dst).
func unkey(dst, k []byte) []byte {
	for {
		i := bytes.IndexByte(k, 0)
		if i < 0 || len(k)-i < 33 {
			return append(dst, k...)
		}
		dst = append(dst, k[:i]...)
		for _, b := range k[i+1 : i+33] {
			dst = append(dst, hexDigits[b>>4], hexDigits[b&15])
		}
		k = k[i+33:]
	}
}

// tableEntry is a decoded table record.
type tableEntry struct {
	key   []byte
	blobs [4]Blob
}

// next decodes the record at *pos (whose key shares with prev's).
func (t *table) next(pos *int, prev []byte, e *tableEntry) error {
	b := t.data
	uv := func() (uint64, error) {
		v, n := binary.Uvarint(b[*pos:])
		if n <= 0 {
			return 0, errBadIndex
		}
		*pos += n
		return v, nil
	}
	shared, err := uv()
	if err != nil {
		return err
	}
	klen, err := uv()
	if err != nil || shared > uint64(len(prev)) || uint64(len(b)-*pos) < klen+1 {
		return errBadIndex
	}
	e.key = append(append(e.key[:0], prev[:shared]...), b[*pos:*pos+int(klen)]...)
	*pos += int(klen)
	mask := b[*pos]
	*pos++
	e.blobs = emptyBlobs()
	var last Blob
	for i := range e.blobs {
		if mask&(1<<i) == 0 {
			continue
		}
		if i == 0 {
			var v [3]uint64
			for k := range v {
				if v[k], err = uv(); err != nil {
					return err
				}
			}
			last = Blob{Seg: int(v[0]), Off: int64(v[1]), Len: int64(v[2])}
		} else {
			lf, err := uv()
			if err != nil {
				return err
			}
			if lf&1 == 1 {
				last = Blob{Seg: last.Seg, Off: last.Off + last.Len, Len: int64(lf >> 1)}
			} else {
				seg, err := uv()
				if err != nil {
					return err
				}
				off, err := uv()
				if err != nil {
					return err
				}
				last = Blob{Seg: int(seg), Off: int64(off), Len: int64(lf >> 1)}
			}
		}
		e.blobs[i] = last
	}
	return nil
}

// lookup finds a path's blobs.
func (t *table) lookup(path string) ([4]Blob, bool) {
	var e tableEntry
	var raw []byte
	cmp := func() int {
		raw = unkey(raw[:0], e.key)
		return strings.Compare(string(raw), path)
	}
	i := sort.Search(len(t.restarts), func(i int) bool {
		pos := int(t.restarts[i])
		if t.next(&pos, nil, &e) != nil {
			return true
		}
		return cmp() > 0
	}) - 1
	if i < 0 {
		return [4]Blob{}, false
	}
	pos := int(t.restarts[i])
	var prev []byte
	for k := 0; k < restartEvery && pos < len(t.data); k++ {
		if t.next(&pos, prev, &e) != nil {
			return [4]Blob{}, false
		}
		switch c := cmp(); {
		case c == 0:
			return e.blobs, true
		case c > 0:
			return [4]Blob{}, false
		}
		prev = append(prev[:0], e.key...)
	}
	return [4]Blob{}, false
}

// tableWriter builds a table, record by record.
type tableWriter struct {
	t    table
	buf  []byte
	prev []byte
	key  []byte
	n    int
}

func (w *tableWriter) add(path string, blobs [4]Blob) error {
	if strings.IndexByte(path, 0) >= 0 {
		return fmt.Errorf("%w: a path has a zero byte", errBadIndex)
	}
	w.key = key(w.key[:0], path)
	shared := 0
	if w.n%restartEvery != 0 {
		for shared < len(w.prev) && shared < len(w.key) && w.prev[shared] == w.key[shared] {
			shared++
		}
	} else {
		w.t.restarts = append(w.t.restarts, uint32(len(w.buf)))
	}
	if uint64(len(w.buf)) > 1<<32-1<<20 {
		return fmt.Errorf("%w: too big to keep", errBadIndex)
	}
	w.buf = binary.AppendUvarint(w.buf, uint64(shared))
	w.buf = binary.AppendUvarint(w.buf, uint64(len(w.key)-shared))
	w.buf = append(w.buf, w.key[shared:]...)
	var mask byte
	for i, b := range blobs {
		if b.Len >= 0 {
			mask |= 1 << i
		}
	}
	w.buf = append(w.buf, mask)
	var last Blob
	for i, b := range blobs {
		if b.Len < 0 {
			continue
		}
		if i == 0 {
			w.buf = binary.AppendUvarint(w.buf, uint64(b.Seg))
			w.buf = binary.AppendUvarint(w.buf, uint64(b.Off))
			w.buf = binary.AppendUvarint(w.buf, uint64(b.Len))
		} else if b.Seg == last.Seg && b.Off == last.Off+last.Len {
			w.buf = binary.AppendUvarint(w.buf, uint64(b.Len)<<1|1)
		} else {
			w.buf = binary.AppendUvarint(w.buf, uint64(b.Len)<<1)
			w.buf = binary.AppendUvarint(w.buf, uint64(b.Seg))
			w.buf = binary.AppendUvarint(w.buf, uint64(b.Off))
		}
		last = b
	}
	w.prev, w.key = w.key, w.prev
	w.n++
	return nil
}

func (w *tableWriter) done() *table {
	// Trimmed, if much of what was set aside wasn't used (copying makes a
	// second table, for a moment, so only then).
	w.t.data = w.buf
	if cap(w.buf) > len(w.buf)+len(w.buf)/4 {
		w.t.data = append([]byte(nil), w.buf...)
	}
	w.t.restarts = append([]uint32(nil), w.t.restarts...)
	w.buf = nil
	return &w.t
}

// loadTable reads an index file as a server keeps it: its header, and a
// table of its records. It's read as a stream, the records turned into
// the table as they're decompressed, so neither the file nor its records
// are ever all in memory.
func loadTable(path string) (*Index, *table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	h := sha256.New()
	in := io.TeeReader(io.LimitReader(f, maxIndexBytes), h)
	magic := make([]byte, len(indexMagic))
	if _, err := io.ReadFull(in, magic); err != nil || string(magic) != indexMagic {
		return nil, nil, fmt.Errorf("%s: %w", path, errBadIndex)
	}
	dec, err := zstd.NewReader(in, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	if err != nil {
		return nil, nil, err
	}
	defer dec.Close()
	r := bufio.NewReaderSize(dec, 256<<10)
	ix, t, err := readTable(r)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	// What's left (nothing, in a good file) goes through the hash.
	if _, err := io.Copy(io.Discard, in); err != nil {
		return nil, nil, err
	}
	ix.SHA256 = fmt.Sprintf("%x", h.Sum(nil))
	if name := filepath.Base(path); strings.HasPrefix(name, "index-") && name != IndexName(ix.SHA256) {
		return nil, nil, fmt.Errorf("%s doesn't match its hash", path)
	}
	return ix, t, nil
}

func readTable(r *bufio.Reader) (*Index, *table, error) {
	hlen, err := binary.ReadUvarint(r)
	if err != nil || hlen > 64<<20 {
		return nil, nil, errBadIndex
	}
	hj := make([]byte, hlen)
	if _, err := io.ReadFull(r, hj); err != nil {
		return nil, nil, errBadIndex
	}
	ix := &Index{}
	if err := json.Unmarshal(hj, &ix.Header); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errBadIndex, err)
	}
	if ix.Format != Format {
		return nil, nil, fmt.Errorf("archive index format %d, not %d", ix.Format, Format)
	}
	w := &tableWriter{buf: make([]byte, 0, ix.Entries*48)}
	var path []byte
	var suffix []byte
	for {
		shared, err := binary.ReadUvarint(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, errBadIndex
		}
		slen, err := binary.ReadUvarint(r)
		if err != nil || shared > uint64(len(path)) || slen > 1<<16 {
			return nil, nil, errBadIndex
		}
		if w.n%restartEvery == 0 && shared != 0 {
			return nil, nil, errBadIndex
		}
		if uint64(cap(suffix)) < slen {
			suffix = make([]byte, slen)
		}
		suffix = suffix[:slen]
		if _, err := io.ReadFull(r, suffix); err != nil {
			return nil, nil, errBadIndex
		}
		path = append(path[:shared], suffix...)
		if _, err := r.Discard(len(Hash{})); err != nil {
			return nil, nil, errBadIndex
		}
		mask, err := r.ReadByte()
		if err != nil || mask&1 == 0 {
			return nil, nil, errBadIndex
		}
		blobs := emptyBlobs()
		for i := range blobs {
			if mask&(1<<i) == 0 {
				continue
			}
			var v [3]uint64
			for k := range v {
				if v[k], err = binary.ReadUvarint(r); err != nil {
					return nil, nil, errBadIndex
				}
			}
			b := Blob{Seg: int(v[0]), Off: int64(v[1]), Len: int64(v[2])}
			if b.Seg >= len(ix.Segments) || b.Off < int64(len(segmentMagic)) || b.Off+b.Len > ix.Segments[b.Seg].Bytes {
				return nil, nil, fmt.Errorf("%w: %s is outside its segment", errBadIndex, path)
			}
			blobs[i] = b
		}
		if err := w.add(string(path), blobs); err != nil {
			return nil, nil, err
		}
		if w.n > ix.Entries {
			return nil, nil, fmt.Errorf("%w: more records than it says", errBadIndex)
		}
	}
	if w.n != ix.Entries {
		return nil, nil, fmt.Errorf("%w: %d records, not %d", errBadIndex, w.n, ix.Entries)
	}
	return ix, w.done(), nil
}
