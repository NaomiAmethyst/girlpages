// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package crawl

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
	"github.com/NaomiAmethyst/girlpages/internal/version"
)

// Nix is a Nix channel, such as https://channels.nixos.org/nixos-25.05,
// with the binary cache its store paths are in.
//
// A channel lists its store paths (store-paths.xz), and the cache has, for
// each, a listing of its files (<hash>.ls) and where its NAR is
// (<hash>.narinfo). So a crawl looks at each store path that could hold
// man pages (a man or bin output, or the main one: not -dev, -lib, -doc,
// a source tarball…) once, remembering what it found in Listings (store
// paths never change), and takes those that do.
type Nix struct {
	URL string
	// Cache is the binary cache (by default https://cache.nixos.org).
	Cache string
	// Listings is the file remembering what's been found out about each
	// store path ("" for nowhere: every crawl looks again).
	Listings string
	// Jobs is how many store paths are looked up at once.
	Jobs int
	// Log, if set, hears how looking them up is going.
	Log io.Writer
}

// nixPath is what's known of a store path: whether it has man pages, and
// if so, its NAR.
type nixPath struct {
	Man    bool   `json:"man"`
	URL    string `json:"url,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// nixOutputs are outputs that never hold man pages: Nix puts them in the
// man output if there is one, else the bin output, else the main one.
var nixOutputs = map[string]bool{"dev": true, "lib": true, "doc": true, "devdoc": true, "info": true, "debug": true,
	"static": true, "data": true, "py": true, "dist": true, "lua": true, "tex": true, "modules": true, "locales": true,
	"examples": true, "test": true, "tests": true, "dbg": true, "scripts": true, "terminfo": true}

var (
	nixNotPackage = regexp.MustCompile(`\.(tar\.[a-z0-9]+|patch|diff|zip|tgz|gz|xz|bz2|zst|drv)$|-source$`)
	nixChannel    = regexp.MustCompile(`(nixos|nixpkgs)-([0-9]+\.[0-9]+|unstable)`)
	nixStorePath  = regexp.MustCompile(`^/nix/store/([0-9a-z]{32})-(.+)$`)
)

// nixName splits a store path's name into the package's name, its version
// and its output ("coreutils-9.7" is coreutils, 9.7 and the main output;
// "sqlite-interactive-3.48.0-man" is sqlite-interactive, 3.48.0 and man),
// the version starting, as Nix's parseDrvName has it, at the first dash
// that isn't followed by a letter.
func nixName(s string) (name, ver, output string) {
	if i := strings.LastIndexByte(s, '-'); i > 0 && (s[i+1:] == "man" || s[i+1:] == "bin" || nixOutputs[s[i+1:]]) {
		s, output = s[:i], s[i+1:]
	}
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '-' && !unicode.IsLetter(rune(s[i+1])) {
			return s[:i], s[i+1:], output
		}
	}
	return s, "", output
}

func (n *Nix) cache() string {
	if n.Cache != "" {
		return strings.TrimSuffix(n.Cache, "/")
	}
	return "https://cache.nixos.org"
}

func (n *Nix) logf(format string, a ...any) {
	if n.Log != nil {
		fmt.Fprintf(n.Log, format, a...)
	}
}

// Index reads the channel's store paths, and looks up those it hasn't yet.
func (n *Nix) Index(ctx context.Context, f *Fetcher) (*Index, error) {
	r, u, err := f.openIndex(ctx, join(n.URL, "store-paths.xz"))
	if err != nil {
		return nil, fmt.Errorf("reading the channel's store paths: %w", err)
	}
	type cand struct{ hash, name, ver string }
	var cands []cand
	idx := &Index{Compare: version.RPM}
	if m := nixChannel.FindStringSubmatch(n.URL); m != nil {
		idx.Distro, idx.Release = m[1], m[2]
	}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		m := nixStorePath.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		idx.Total++
		name, ver, output := nixName(m[2])
		if nixNotPackage.MatchString(m[2]) || nixOutputs[output] {
			continue
		}
		cands = append(cands, cand{m[1], name, ver})
	}
	err = sc.Err()
	if cerr := r.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}

	known := map[string]nixPath{}
	if n.Listings != "" {
		if b, err := os.ReadFile(n.Listings); err == nil {
			if err := json.Unmarshal(b, &known); err != nil {
				return nil, fmt.Errorf("%s: %w", n.Listings, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	var todo []string
	for _, c := range cands {
		if _, ok := known[c.hash]; !ok {
			todo = append(todo, c.hash)
		}
	}
	if len(todo) > 0 {
		n.logf("🔎 %d of %d store paths could have man pages; looking at the %d not seen before\n", len(cands), idx.Total, len(todo))
		if err := n.lookUp(ctx, f, todo, known); err != nil {
			return nil, err
		}
	}
	for _, c := range cands {
		if p := known[c.hash]; p.Man {
			idx.Packages = append(idx.Packages, Package{Name: c.name, Version: c.ver, Arch: "", URL: p.URL, SHA256: p.SHA256, Size: p.Size})
		}
	}
	return idx, nil
}

// lookUp finds out what each store path holds, adding it to known, which
// it saves now and then, so an interrupted crawl carries on where it was.
func (n *Nix) lookUp(ctx context.Context, f *Fetcher, todo []string, known map[string]nixPath) error {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
		done     int
	)
	save := func() error {
		if n.Listings == "" {
			return nil
		}
		b, err := json.Marshal(known)
		if err != nil {
			return err
		}
		return repo.WriteFile(n.Listings, b)
	}
	jobs := make(chan string)
	for range max(n.Jobs, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for h := range jobs {
				p, err := n.look(ctx, f, h)
				mu.Lock()
				switch {
				case errors.Is(err, errUnreadable):
					// Left for another crawl to try again.
					n.logf("⚠️  %v\n", err)
				case err != nil:
					if firstErr == nil {
						firstErr = err
					}
				default:
					known[h] = p
				}
				done++
				if done%5000 == 0 {
					n.logf("   %d of %d looked at\n", done, len(todo))
					if err := save(); err != nil && firstErr == nil {
						firstErr = err
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, h := range todo {
		mu.Lock()
		failed := firstErr != nil
		mu.Unlock()
		if failed || ctx.Err() != nil {
			break
		}
		jobs <- h
	}
	close(jobs)
	wg.Wait()
	if err := save(); err != nil && firstErr == nil {
		firstErr = err
	}
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	return firstErr
}

// errUnreadable is a listing that couldn't be read: worth carrying on
// without.
var errUnreadable = errors.New("unreadable")

// look finds out whether a store path has man pages, and if it has, where
// its NAR is. One the cache hasn't got (an unfree package, say) has none.
func (n *Nix) look(ctx context.Context, f *Fetcher, hash string) (nixPath, error) {
	r, err := f.Open(ctx, n.cache()+"/"+hash+".ls")
	if errors.Is(err, ErrNotFound) {
		return nixPath{}, nil
	}
	if err != nil {
		return nixPath{}, err
	}
	man, err := nixListingHasMan(r)
	r.Close()
	if err != nil {
		return nixPath{}, fmt.Errorf("%s.ls: %w: %w", hash, err, errUnreadable)
	}
	if !man {
		return nixPath{}, nil
	}
	r, err = f.Open(ctx, n.cache()+"/"+hash+".narinfo")
	if errors.Is(err, ErrNotFound) {
		return nixPath{}, nil
	}
	if err != nil {
		return nixPath{}, err
	}
	defer r.Close()
	info := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), ": "); ok {
			info[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return nixPath{}, err
	}
	p := nixPath{Man: true, URL: n.cache() + "/" + info["URL"]}
	p.Size, _ = strconv.ParseInt(info["FileSize"], 10, 64)
	if algo, h, ok := strings.Cut(info["FileHash"], ":"); ok && algo == "sha256" {
		if p.SHA256, err = nixHashHex(h); err != nil {
			return nixPath{}, fmt.Errorf("%s.narinfo: %w", hash, err)
		}
	}
	if info["URL"] == "" {
		return nixPath{}, fmt.Errorf("%s.narinfo: no URL", hash)
	}
	return p, nil
}

// nixListingHasMan reads a store path's listing (JSON, compressed as it
// was when it was uploaded, whatever's asked for: with brotli, saying so,
// or with zstd, not saying) for a man page.
func nixListingHasMan(r io.Reader) (bool, error) {
	br := bufio.NewReader(r)
	var in io.Reader = br
	magic, _ := br.Peek(4)
	switch {
	case len(magic) > 0 && magic[0] == '{':
	case bytes.HasPrefix(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		zr, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return false, err
		}
		defer zr.Close()
		in = zr
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(br)
		if err != nil {
			return false, err
		}
		in = gz
	default:
		in = brotli.NewReader(br) // which has no magic number of its own
	}
	var ls struct {
		Root nixEntry `json:"root"`
	}
	if err := json.NewDecoder(in).Decode(&ls); err != nil {
		return false, err
	}
	return ls.Root.hasMan(""), nil
}

type nixEntry struct {
	Type    string              `json:"type"`
	Entries map[string]nixEntry `json:"entries"`
}

func (e nixEntry) hasMan(at string) bool {
	if e.Type != "directory" {
		return e.Type == "regular" && IsManPath(at)
	}
	for name, c := range e.Entries {
		if c.hasMan(path.Join(at, name)) {
			return true
		}
	}
	return false
}

// nixHashHex turns a hash in Nix's base 32 (its own alphabet, least
// significant digit first), or base 64 or hex, into hex.
func nixHashHex(s string) (string, error) {
	switch len(s) {
	case 64:
		if _, err := hex.DecodeString(s); err == nil {
			return s, nil
		}
	case 44:
		if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
			return hex.EncodeToString(b), nil
		}
	}
	const alphabet = "0123456789abcdfghijklmnpqrsvwxyz"
	if len(s) != 52 {
		return "", fmt.Errorf("a sha256 hash of %d characters", len(s))
	}
	out := make([]byte, 32)
	for i := 0; i < len(s); i++ {
		d := strings.IndexByte(alphabet, s[len(s)-1-i])
		if d < 0 {
			return "", fmt.Errorf("%q isn't in Nix's base 32", s[len(s)-1-i])
		}
		b := i * 5
		out[b/8] |= byte(d << (b % 8))
		if b/8+1 < 32 {
			out[b/8+1] |= byte(d >> (8 - b%8))
		} else if d>>(8-b%8) != 0 {
			return "", errors.New("a base-32 hash too big for sha256")
		}
	}
	return hex.EncodeToString(out), nil
}
