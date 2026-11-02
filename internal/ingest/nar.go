// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package ingest

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A NAR (Nix ARchive) is a store path's files, as Nix's binary caches serve
// them: strings, each its length (8 bytes, little-endian), its bytes and
// zeroes to a multiple of 8, making up nested nodes:
//
//	"nix-archive-1" node
//	node:  "(" "type" ( "regular" ["executable" ""] "contents" bytes
//	                  | "symlink" "target" string
//	                  | "directory" { "entry" "(" "name" string "node" node ")" } ) ")"

// maxNARString is the most a NAR's names and targets may be.
const maxNARString = 4096

// extractNAR unpacks the man pages (files with a man directory in their
// path, and symlinks among them pointing nearby) from a NAR, compressed or
// not, into dir.
func extractNAR(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := Decompress(f)
	if err != nil {
		return err
	}
	n := &narReader{r: bufio.NewReaderSize(r, 64<<10), dir: dir}
	if err := n.expect("nix-archive-1"); err != nil {
		r.Close()
		return fmt.Errorf("%s: not a NAR: %w", path, err)
	}
	err = n.node("")
	if cerr := r.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

type narReader struct {
	r   *bufio.Reader
	dir string
}

func (n *narReader) length() (uint64, error) {
	var b [8]byte
	if _, err := io.ReadFull(n.r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}

func (n *narReader) pad(l uint64) error {
	if p := (8 - l%8) % 8; p > 0 {
		_, err := n.r.Discard(int(p))
		return err
	}
	return nil
}

// str reads a string (a name, a target, a keyword).
func (n *narReader) str() (string, error) {
	l, err := n.length()
	if err != nil {
		return "", err
	}
	if l > maxNARString {
		return "", fmt.Errorf("a string of %d bytes, which is too long", l)
	}
	b := make([]byte, l)
	if _, err := io.ReadFull(n.r, b); err != nil {
		return "", err
	}
	return string(b), n.pad(l)
}

func (n *narReader) expect(want string) error {
	got, err := n.str()
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%q where %q should be", got, want)
	}
	return nil
}

// wanted reports whether a path in the NAR is a man page's.
func wanted(rel string) bool {
	return strings.HasPrefix(rel, "man/") || strings.Contains(rel, "/man/")
}

// node reads a node at rel (its slash path in the NAR, "" for the root).
func (n *narReader) node(rel string) error {
	if err := n.expect("("); err != nil {
		return err
	}
	if err := n.expect("type"); err != nil {
		return err
	}
	typ, err := n.str()
	if err != nil {
		return err
	}
	switch typ {
	case "regular":
		k, err := n.str()
		if err != nil {
			return err
		}
		if k == "executable" {
			if err := n.expect(""); err != nil {
				return err
			}
			if k, err = n.str(); err != nil {
				return err
			}
		}
		if k != "contents" {
			return fmt.Errorf("%q where \"contents\" should be", k)
		}
		if err := n.contents(rel); err != nil {
			return err
		}
	case "symlink":
		if err := n.expect("target"); err != nil {
			return err
		}
		target, err := n.str()
		if err != nil {
			return err
		}
		// Only links within the archive: one to another store path
		// (/nix/store/…) would point nowhere here.
		if wanted(rel) && !filepath.IsAbs(target) {
			p := filepath.Join(n.dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(target, p); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		}
	case "directory":
		for {
			k, err := n.str()
			if err != nil {
				return err
			}
			if k == ")" {
				return nil
			}
			if k != "entry" {
				return fmt.Errorf("%q where \"entry\" should be", k)
			}
			if err := n.expect("("); err != nil {
				return err
			}
			if err := n.expect("name"); err != nil {
				return err
			}
			name, err := n.str()
			if err != nil {
				return err
			}
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
				return fmt.Errorf("an entry called %q", name)
			}
			if err := n.expect("node"); err != nil {
				return err
			}
			child := name
			if rel != "" {
				child = rel + "/" + name
			}
			if err := n.node(child); err != nil {
				return err
			}
			if err := n.expect(")"); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("a node of type %q", typ)
	}
	return n.expect(")")
}

// contents reads a file's contents, writing them out if it's a man page.
func (n *narReader) contents(rel string) error {
	l, err := n.length()
	if err != nil {
		return err
	}
	w := io.Discard
	if wanted(rel) {
		p := filepath.Join(n.dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	if _, err := io.CopyN(w, n.r, int64(l)); err != nil {
		return err
	}
	return n.pad(l)
}
