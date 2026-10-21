// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package serve

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/archive"
)

// suffixes are the compressed copies' file name suffixes, by encoding.
var suffixes = map[string]string{"zstd": ".zst", "br": ".br", "gzip": ".gz"}

// DirSource serves a site from a directory, as publish writes it:
// compressed copies (index.html.br…) beside their files.
type DirSource string

func (d DirSource) Ready() bool {
	_, err := os.Stat(filepath.Join(string(d), "index.html"))
	return err == nil
}

func (d DirSource) Find(rel string) (File, bool) {
	if strings.HasPrefix(path0(rel), ".") {
		return nil, false // hidden: locks, temporary files
	}
	p := filepath.Join(string(d), filepath.FromSlash(rel))
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	return &dirFile{path: p, mod: fi.ModTime()}, true
}

// path0 is a path's last element.
func path0(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

type dirFile struct {
	path string
	mod  time.Time
}

func (f *dirFile) Has(encoding string) bool {
	s, ok := suffixes[encoding]
	if !ok {
		return false
	}
	_, err := os.Stat(f.path + s)
	return err == nil
}

func (f *dirFile) Size(encoding string) int64 {
	p := f.path
	if encoding != "" {
		s, ok := suffixes[encoding]
		if !ok {
			return -1
		}
		p += s
	}
	fi, err := os.Stat(p)
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (f *dirFile) Open(encoding string) (io.ReadSeeker, error) {
	p := f.path
	if encoding != "" {
		p += suffixes[encoding]
	}
	return os.Open(p)
}

func (f *dirFile) ETag(string) string { return "" } // Last-Modified does instead
func (f *dirFile) ModTime() time.Time { return f.mod }
func (f *dirFile) Close() error       { return nil }

// ArchiveSource serves a site from an archive, as it's reloaded.
type ArchiveSource struct{ *archive.Archive }

func (a ArchiveSource) Ready() bool { return a.Current() != nil }

func (a ArchiveSource) Find(rel string) (File, bool) {
	s := a.Current()
	if s == nil {
		return nil, false
	}
	e, ok := s.Lookup(rel)
	if !ok {
		return nil, false
	}
	// The snapshot found it in is the one it's read from, even if a
	// reload happens meanwhile.
	return &archiveFile{snap: s, e: e}, true
}

type archiveFile struct {
	snap *archive.Snapshot
	e    archive.Entry
}

func (f *archiveFile) Has(encoding string) bool { return f.e.Has(encoding) }

func (f *archiveFile) Size(encoding string) int64 {
	if b, ok := f.e.Blob(encoding); ok {
		return b.Len
	}
	return -1
}

func (f *archiveFile) Open(encoding string) (io.ReadSeeker, error) {
	r, _, err := f.snap.Open(f.e, encoding)
	return r, err
}

func (f *archiveFile) ETag(encoding string) string { return f.snap.ETag(f.e, encoding) }

// ModTime is unknown: an archive's files are told apart by their ETags.
func (f *archiveFile) ModTime() time.Time { return time.Time{} }
func (f *archiveFile) Close() error       { return nil }
