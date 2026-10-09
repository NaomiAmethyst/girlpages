// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package cache stores converted pages.
//
// Layout, under each cache directory:
//
//	objects/ab/abcdef….girl   converted markup, named by the SHA-256 of the
//	                          plain-text page it came from
//	index/<source path>.json  which object a source file maps to, plus the
//	                          source's size and mtime for cheap staleness checks
//
// Content addressing means aliases (symlinks, .so stubs) and identical
// pages share one conversion.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry is an index record for one source file.
type Entry struct {
	Source        string    `json:"source"`
	Name          string    `json:"name"`
	Section       string    `json:"section"`
	Title         string    `json:"title,omitempty"`
	Size          int64     `json:"size"`
	ModTime       int64     `json:"mtime"` // unix nanoseconds
	Hash          string    `json:"hash"`
	Backend       string    `json:"backend,omitempty"`
	Model         string    `json:"model,omitempty"`
	PromptVersion int       `json:"prompt_version,omitempty"`
	Generated     time.Time `json:"generated"`
}

// Fresh reports whether the source file still has the size and mtime it had
// when the entry was written.
func (e *Entry) Fresh() bool {
	fi, err := os.Stat(e.Source)
	if err != nil {
		return false
	}
	return fi.Size() == e.Size && fi.ModTime().UnixNano() == e.ModTime
}

// Stamp records the source's current size and mtime on the entry.
func (e *Entry) Stamp() error {
	fi, err := os.Stat(e.Source)
	if err != nil {
		return err
	}
	e.Size = fi.Size()
	e.ModTime = fi.ModTime().UnixNano()
	return nil
}

// Hash is the content key for a plain-text page.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Dir is a single cache directory.
type Dir string

func (d Dir) indexPath(source string) string {
	return filepath.Join(string(d), "index", filepath.Clean("/"+source)+".json")
}

func (d Dir) objectPath(hash string) string {
	return filepath.Join(string(d), "objects", hash[:2], hash+".girl")
}

// Entry reads the index record for a source, or nil if there is none.
func (d Dir) Entry(source string) (*Entry, error) {
	b, err := os.ReadFile(d.indexPath(source))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var e Entry
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("%s: %w", d.indexPath(source), err)
	}
	if len(e.Hash) < 2 {
		return nil, nil
	}
	return &e, nil
}

// HasObject reports whether a conversion for hash is stored here.
func (d Dir) HasObject(hash string) bool {
	_, err := os.Stat(d.objectPath(hash))
	return err == nil
}

// Object reads a stored conversion.
func (d Dir) Object(hash string) (string, error) {
	b, err := os.ReadFile(d.objectPath(hash))
	return string(b), err
}

// PutObject stores a conversion.
func (d Dir) PutObject(hash, markup string) error {
	return writeAtomic(d.objectPath(hash), []byte(markup))
}

// PutEntry stores an index record.
func (d Dir) PutEntry(e *Entry) error {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(d.indexPath(e.Source), append(b, '\n'))
}

// RemoveEntry deletes an index record.
func (d Dir) RemoveEntry(source string) error {
	err := os.Remove(d.indexPath(source))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Entries calls fn for every index record in the directory.
func (d Dir) Entries(fn func(*Entry) error) error {
	root := filepath.Join(string(d), "index")
	err := filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if de.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var e Entry
		if json.Unmarshal(b, &e) != nil || len(e.Hash) < 2 {
			return nil
		}
		return fn(&e)
	})
	return err
}

// GC removes objects that no index record refers to, returning how many
// were removed.
func (d Dir) GC() (int, error) {
	live := map[string]bool{}
	if err := d.Entries(func(e *Entry) error { live[e.Hash] = true; return nil }); err != nil {
		return 0, err
	}
	n := 0
	root := filepath.Join(string(d), "objects")
	err := filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if de.IsDir() || !strings.HasSuffix(path, ".girl") {
			return nil
		}
		if !live[strings.TrimSuffix(de.Name(), ".girl")] {
			if err := os.Remove(path); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// MarkerPath is touched after a complete, unfiltered bulk run; update runs
// use it to decide whether new pages should be converted too.
func (d Dir) MarkerPath() string { return filepath.Join(string(d), "bulk-complete") }

// LockPath is held for the duration of a bulk run.
func (d Dir) LockPath() string { return filepath.Join(string(d), "bulk.lock") }

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	// CreateTemp makes 0600 files; the system cache must be world-readable.
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Set is the ordered list of caches girl reads from; the first is written.
type Set []Dir

// Writable is the cache new conversions go into.
func (s Set) Writable() Dir { return s[0] }

// Lookup finds the best cached conversion for a source: one whose source is
// unchanged if possible, otherwise the most preferred stale one. It returns
// the markup, the entry, and whether the entry is fresh.
func (s Set) Lookup(source string) (string, *Entry, bool) {
	var staleMarkup string
	var staleEntry *Entry
	for _, d := range s {
		e, err := d.Entry(source)
		if err != nil || e == nil {
			continue
		}
		markup, ok := s.Object(e.Hash)
		if !ok {
			continue
		}
		if e.Fresh() {
			return markup, e, true
		}
		if staleEntry == nil {
			staleMarkup, staleEntry = markup, e
		}
	}
	return staleMarkup, staleEntry, false
}

// Object reads a conversion from whichever cache has it.
func (s Set) Object(hash string) (string, bool) {
	for _, d := range s {
		if m, err := d.Object(hash); err == nil {
			return m, true
		}
	}
	return "", false
}

// HasObject reports whether any cache holds a conversion for hash.
func (s Set) HasObject(hash string) bool {
	for _, d := range s {
		if d.HasObject(hash) {
			return true
		}
	}
	return false
}
