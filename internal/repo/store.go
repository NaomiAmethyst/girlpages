// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"bytes"
	"context"
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
	"sync/atomic"
	"syscall"
	"time"
)

// Store is a repository on the local disk, for building and publishing.
type Store struct {
	Dir string
	// Progress, if set, is told how long jobs (reading every listing,
	// publishing, collecting garbage) are going.
	Progress Progress
	// Written, if set, is told of each file written (by its path), so it
	// can be compressed straight away.
	Written func(path string)

	mu  sync.Mutex // serialises listing updates within this process
	vmu sync.Mutex // and variants.json updates
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
	return Hash(b), int64(len(b)), s.writeFile(s.abs(rel), b)
}

// writeFile writes a file, and tells Written.
func (s *Store) writeFile(path string, b []byte) error {
	if err := WriteFile(path, b); err != nil {
		return err
	}
	if s.Written != nil {
		s.Written(path)
	}
	return nil
}

// WriteAddressed stores a document under a name made from its own hash.
func (s *Store) WriteAddressed(name func(sha string) string, v any) (string, error) {
	sha, _, err := s.writeSized(name, v)
	return sha, err
}

// writeSized is WriteAddressed, also returning the file's size.
func (s *Store) writeSized(name func(sha string) string, v any) (string, int64, error) {
	b, err := Encode(v)
	if err != nil {
		return "", 0, err
	}
	sha := Hash(b)
	p := s.abs(name(sha))
	if _, err := os.Stat(p); err == nil {
		return sha, int64(len(b)), nil
	}
	return sha, int64(len(b)), s.writeFile(p, b)
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
func (s *Store) Lock() (func(), error) { return LockFile(filepath.Join(s.Dir, ".lock")) }

// LockFile takes an exclusive lock on a file (creating it), waiting for
// it if another process has it, and returns what releases it.
func LockFile(path string) (func(), error) {
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
	l.derive()
	sha, err := s.WriteAddressed(ListingPath, l)
	if err != nil {
		return err
	}
	_, _, err = s.Write(NamePath(name, section), &Pointer{Format: Format, Name: name, Section: section, SHA256: sha})
	return err
}

// WriteRaw stores a document's bytes as they are, so a copy from another
// repository keeps its hash.
func (s *Store) WriteRaw(rel string, b []byte) error { return s.writeFile(s.abs(rel), b) }

// ReadRaw reads a stored document's bytes.
func (s *Store) ReadRaw(rel string) ([]byte, error) {
	b, err := os.ReadFile(s.abs(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", rel, ErrNotFound)
	}
	return b, err
}

// derive works out the fields that follow from the versions.
func (l *Listing) derive() {
	for i := range l.Versions {
		l.Versions[i].Version = l.Versions[i].Upstream()
	}
	l.Current = ""
	if v := l.Latest(); v != nil {
		l.Current = v.Source
	}
	l.Sources = nil
	for _, src := range sourcesOf(l) {
		if v := l.VersionFrom(src.Distro, src.Release); v != nil {
			if l.Sources == nil {
				l.Sources = map[string]string{}
			}
			l.Sources[src.Key()] = v.Source
		}
	}
}

// Refresh brings listings up to date with this version of girlrepo,
// returning how many changed: variants still kept the old way (one file
// per variant, changed in place) become generation 1 of the new, listings
// record each variant's latest generation as variants.json has it, and
// the fields derived from versions are worked out again. The caller must
// hold Lock.
func (s *Store) Refresh(pages []PageRef) (int, error) {
	return s.RefreshContext(context.Background(), pages)
}

// RefreshContext is Refresh, stopping early if ctx is done.
func (s *Store) RefreshContext(ctx context.Context, pages []PageRef) (int, error) {
	var n atomic.Int64
	var mu sync.Mutex
	contents := map[string]*Contents{}
	// A version's Contents, migrated if need be, once however many pages
	// share it.
	contentsOf := func(v Version) (*Contents, error) {
		mu.Lock()
		cs := contents[v.Source]
		mu.Unlock()
		if cs != nil {
			return cs, nil
		}
		cs, err := s.migrate(v.Source, v.Variants)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		contents[v.Source] = cs
		mu.Unlock()
		return cs, nil
	}
	c := s.counter("checking listings", len(pages))
	errs := make([]error, len(pages))
	parallel(len(pages), func(i int) {
		defer c.add()
		if ctx.Err() != nil {
			return
		}
		p := pages[i]
		want := *p.Listing
		want.Versions = append([]Version(nil), p.Listing.Versions...)
		want.derive()
		same := want.Current == p.Listing.Current && fmt.Sprint(want.Sources) == fmt.Sprint(p.Listing.Sources)
		for i := range want.Versions {
			same = same && want.Versions[i].Version == p.Listing.Versions[i].Version
		}
		latest := map[string]map[string]VariantInfo{}
		for _, v := range p.Listing.Versions {
			cs, err := contentsOf(v)
			if err != nil {
				errs[i] = err
				return
			}
			latest[v.Source] = map[string]VariantInfo{}
			for variant, info := range v.Variants {
				if g := cs.Latest(variant); g != nil {
					latest[v.Source][variant] = *g
					same = same && *g == info
				}
			}
		}
		if same {
			return
		}
		errs[i] = s.UpdateListing(p.Pointer.Name, p.Pointer.Section, func(l *Listing) error {
			for i := range l.Versions {
				v := &l.Versions[i]
				for variant, g := range latest[v.Source] {
					if _, ok := v.Variants[variant]; ok {
						v.Variants[variant] = g
					}
				}
			}
			return nil
		})
		if errs[i] == nil {
			n.Add(1)
		}
	})
	if err := ctx.Err(); err != nil {
		return int(n.Load()), err
	}
	return int(n.Load()), errors.Join(errs...)
}

// migrate moves a version's variants still kept the old way, one file per
// variant changed in place, into generation 1 of the new, returning its
// Contents. The old files are left for GC.
func (s *Store) migrate(source string, variants map[string]VariantInfo) (*Contents, error) {
	cs, err := s.Contents(source)
	if err != nil {
		return nil, err
	}
	moved := false
	for variant := range variants {
		if cs.Latest(variant) != nil || !ValidVariant(variant) {
			continue
		}
		b, err := s.ReadRaw(legacyVariantPath(source, variant))
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if _, err := s.PutVariantRaw(source, variant, b); err != nil {
			return nil, fmt.Errorf("%s/%s: %w", source, variant, err)
		}
		moved = true
	}
	if moved {
		return s.Contents(source)
	}
	return cs, nil
}

// Contents reads a page version's Contents, or returns an empty one.
func (s *Store) Contents(source string) (*Contents, error) {
	c := &Contents{Format: Format, Source: source, Variants: map[string][]VariantInfo{}}
	if err := s.Read(ContentsPath(source), c); err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if c.Variants == nil {
		c.Variants = map[string][]VariantInfo{}
	}
	return c, nil
}

// Variant reads the latest generation of a stored variant.
func (s *Store) Variant(source, variant string) (*Variant, error) {
	c, err := s.Contents(source)
	if err != nil {
		return nil, err
	}
	g := c.Latest(variant)
	if g == nil {
		return nil, fmt.Errorf("%s/%s: %w", source, variant, ErrNotFound)
	}
	var v Variant
	if err := s.Read(VariantPath(source, variant, g.SHA256), &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// HasVariant reports whether a variant is stored.
func (s *Store) HasVariant(source, variant string) bool {
	c, err := s.Contents(source)
	return err == nil && c.Latest(variant) != nil
}

// PutVariant stores a variant as its latest generation (unless it's the
// same as the latest already), returning what it's stored as.
func (s *Store) PutVariant(v *Variant) (VariantInfo, error) {
	b, err := Encode(v)
	if err != nil {
		return VariantInfo{}, err
	}
	return s.putVariant(v.Source, v.Variant, b)
}

// PutVariantRaw stores a variant's file as it is (one from another
// repository, say), after checking it is the variant it should be.
func (s *Store) PutVariantRaw(source, variant string, b []byte) (VariantInfo, error) {
	var v Variant
	if err := json.Unmarshal(b, &v); err != nil {
		return VariantInfo{}, err
	}
	if v.Source != source || v.Variant != variant {
		return VariantInfo{}, fmt.Errorf("that's %s/%s, not %s/%s", v.Source, v.Variant, source, variant)
	}
	return s.putVariant(source, variant, b)
}

func (s *Store) putVariant(source, variant string, b []byte) (VariantInfo, error) {
	if !ValidHash(source) || !ValidVariant(variant) {
		return VariantInfo{}, fmt.Errorf("bad variant %s/%s", source, variant)
	}
	info := InfoFor(b)
	p := s.abs(VariantPath(source, variant, info.SHA256))
	if _, err := os.Stat(p); err != nil {
		if err := WriteFile(p, b); err != nil {
			return VariantInfo{}, err
		}
	}
	s.vmu.Lock()
	defer s.vmu.Unlock()
	c, err := s.Contents(source)
	if err != nil {
		return VariantInfo{}, err
	}
	latest := c.Latest(variant)
	if latest != nil && latest.SHA256 == info.SHA256 {
		return *latest, nil
	}
	info.Generation = 1
	if latest != nil {
		info.Generation = latest.Generation + 1
	}
	c.Variants[variant] = append(c.Variants[variant], info)
	if _, _, err := s.Write(ContentsPath(source), c); err != nil {
		return VariantInfo{}, err
	}
	return info, nil
}

// Pages calls fn for every page pointer, in no particular order.
func (s *Store) Pages(fn func(Pointer) error) error {
	paths, err := s.pointerPaths()
	if err != nil {
		return err
	}
	for _, rel := range paths {
		var p Pointer
		if err := s.Read(rel, &p); err != nil {
			return err
		}
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}

// eachPage calls fn with every page's pointer, from every CPU at once.
func (s *Store) eachPage(ctx context.Context, fn func(Pointer) error) error {
	paths, err := s.pointerPaths()
	if err != nil {
		return err
	}
	errs := make([]error, len(paths))
	parallel(len(paths), func(i int) {
		if ctx.Err() != nil {
			return
		}
		var p Pointer
		if errs[i] = s.Read(paths[i], &p); errs[i] == nil {
			errs[i] = fn(p)
		}
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// pointerPaths lists every page's pointer.
func (s *Store) pointerPaths() ([]string, error) {
	var out []string
	root := s.abs("page")
	ents, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, d := range ents {
		if !d.IsDir() || !strings.HasPrefix(d.Name(), "name-") {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".json") || strings.HasPrefix(f.Name(), ".tmp-") {
				continue
			}
			out = append(out, "page/"+d.Name()+"/"+f.Name())
		}
	}
	return out, nil
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
