// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Store is a repository on the local disk, for building and publishing.
type Store struct {
	Dir string

	mu sync.Mutex // serialises listing updates within this process
}

// Open opens (creating if needed) a repository directory.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

// ErrNotFound is returned for documents that don't exist.
var ErrNotFound = errors.New("not in the repo")

func (s *Store) abs(rel string) string { return filepath.Join(s.Dir, filepath.FromSlash(rel)) }

// Encode is how every document is serialised. Map keys come out sorted, so
// the same document always hashes the same.
func Encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Hash is the hex SHA-256 of b.
func Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Read decodes the document at rel.
func (s *Store) Read(rel string, v any) error {
	b, err := os.ReadFile(s.abs(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", rel, ErrNotFound)
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	return nil
}

// Write stores a document at rel, returning its hash and size.
func (s *Store) Write(rel string, v any) (string, int64, error) {
	b, err := Encode(v)
	if err != nil {
		return "", 0, err
	}
	return Hash(b), int64(len(b)), WriteFile(s.abs(rel), b)
}

// WriteAddressed stores a document under a name made from its own hash.
func (s *Store) WriteAddressed(name func(sha string) string, v any) (string, error) {
	b, err := Encode(v)
	if err != nil {
		return "", err
	}
	sha := Hash(b)
	p := s.abs(name(sha))
	if _, err := os.Stat(p); err == nil {
		return sha, nil
	}
	return sha, WriteFile(p, b)
}

// WriteFile writes data atomically, creating directories as needed.
func WriteFile(path string, data []byte) error {
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
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Lock takes the repository's lock, waiting for other writers. Every
// command that changes the repository holds it while it does.
func (s *Store) Lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.Dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// Listing reads a page's listing, returning nil if the page isn't known.
func (s *Store) Listing(name, section string) (*Listing, string, error) {
	var p Pointer
	if err := s.Read(NamePath(name, section), &p); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, "", nil
		}
		return nil, "", err
	}
	l, err := s.ListingByHash(p.SHA256)
	return l, p.SHA256, err
}

// ListingByHash reads a listing by its hash.
func (s *Store) ListingByHash(sha string) (*Listing, error) {
	if !ValidHash(sha) {
		return nil, fmt.Errorf("bad listing hash %q", sha)
	}
	var l Listing
	if err := s.Read(ListingPath(sha), &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// UpdateListing changes a page's listing (creating it if need be) and
// repoints the page at the result. The caller must hold Lock.
func (s *Store) UpdateListing(name, section string, fn func(*Listing) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, _, err := s.Listing(name, section)
	if err != nil {
		return err
	}
	if l == nil {
		l = &Listing{Format: Format, Name: name, Section: section}
	}
	if err := fn(l); err != nil {
		return err
	}
	// Newest first; ties (one ingest run) by hash, so it's stable.
	sort.SliceStable(l.Versions, func(i, j int) bool {
		a, b := l.Versions[i], l.Versions[j]
		if !a.Added.Equal(b.Added) {
			return a.Added.After(b.Added)
		}
		return a.Source < b.Source
	})
	if l.Current == "" && len(l.Versions) > 0 {
		l.Current = l.Versions[0].Source
	}
	sha, err := s.WriteAddressed(ListingPath, l)
	if err != nil {
		return err
	}
	_, _, err = s.Write(NamePath(name, section), &Pointer{Format: Format, Name: name, Section: section, SHA256: sha})
	return err
}

// Variant reads a stored variant.
func (s *Store) Variant(source, variant string) (*Variant, error) {
	var v Variant
	if err := s.Read(ContentPath(source, variant), &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// HasVariant reports whether a variant is stored.
func (s *Store) HasVariant(source, variant string) bool {
	_, err := os.Stat(s.abs(ContentPath(source, variant)))
	return err == nil
}

// PutVariant stores a variant.
func (s *Store) PutVariant(v *Variant) (VariantInfo, error) {
	sha, size, err := s.Write(ContentPath(v.Source, v.Variant), v)
	return VariantInfo{SHA256: sha, Size: size}, err
}

// Pages calls fn for every page pointer, in no particular order.
func (s *Store) Pages(fn func(Pointer) error) error {
	root := s.abs("page")
	ents, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, d := range ents {
		if !d.IsDir() || !strings.HasPrefix(d.Name(), "name-") {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			return err
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".json") || strings.HasPrefix(f.Name(), ".tmp-") {
				continue
			}
			var p Pointer
			if err := s.Read("page/"+d.Name()+"/"+f.Name(), &p); err != nil {
				return err
			}
			if err := fn(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// Manifest reads manifest.json.
func (s *Store) Manifest() (*Manifest, error) {
	var m Manifest
	if err := s.Read(ManifestPath(), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// now is time.Now, truncated for tidy JSON; tests replace it.
var now = func() time.Time { return time.Now().UTC().Truncate(time.Second) }
