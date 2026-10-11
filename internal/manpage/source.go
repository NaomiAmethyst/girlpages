// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package manpage

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Source reads a man page's roff source: decompressed, with .so requests
// replaced by the files they name (the way soelim does), searching the
// page's own manual root and then roots. The result doesn't depend on how a
// distribution compresses its pages or whether an alias is a symlink or a
// .so stub, which makes it a good key for sharing conversions between
// machines: see SourceHash.
func Source(path string, roots []string) ([]byte, error) {
	return SourceIn(path, roots, nil)
}

// SourceIn is Source, refusing to read anything (the page, or what it
// includes) that isn't inside one of jails once symlinks are followed. It's
// for pages from packages, which mustn't be able to point at the host's
// files. No jails means no limit.
func SourceIn(path string, roots, jails []string) ([]byte, error) {
	r := resolver{roots: append([]string{Root(path)}, roots...)}
	for _, j := range jails {
		real, err := filepath.EvalSymlinks(j)
		if err != nil {
			return nil, err
		}
		r.jails = append(r.jails, real)
	}
	return r.expand(path, 0)
}

// SourceHash is the hex SHA-256 of Source.
func SourceHash(path string, roots []string) (string, error) {
	b, err := Source(path, roots)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Root is the manual root a page lives under: /usr/share/man for
// /usr/share/man/man1/ls.1.gz.
func Root(path string) string {
	return filepath.Dir(filepath.Dir(path))
}

type resolver struct {
	roots []string
	jails []string
}

func (r *resolver) allowed(path string) error {
	if len(r.jails) == 0 {
		return nil
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	for _, j := range r.jails {
		if rel, err := filepath.Rel(j, real); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return nil
		}
	}
	return fmt.Errorf("%s: leads outside the package, to %s", path, real)
}

var soRE = regexp.MustCompile(`^\.[ \t]*so[ \t]+(\S+)[ \t]*$`)

const maxSoDepth = 8

func (r *resolver) expand(path string, depth int) ([]byte, error) {
	raw, err := r.read(path)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(raw, []byte(".so")) {
		return raw, nil
	}
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if m := soRE.FindSubmatch(line); m != nil {
			if depth >= maxSoDepth {
				return nil, fmt.Errorf("%s: .so nested too deeply", path)
			}
			target, err := r.find(string(m[1]), path)
			if err != nil {
				return nil, err
			}
			inc, err := r.expand(target, depth+1)
			if err != nil {
				return nil, err
			}
			out.Write(inc)
			if len(inc) > 0 && inc[len(inc)-1] != '\n' {
				out.WriteByte('\n')
			}
			continue
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		// Keep a file without a final newline byte-for-byte the same.
		out.Truncate(out.Len() - 1)
	}
	return out.Bytes(), nil
}

// find locates the file a .so request names. Names are relative to a manual
// root ("man1/ls.1"), though some pages use paths relative to their own
// directory, so that's tried too; man-db also accepts compressed targets.
func (r *resolver) find(name, from string) (string, error) {
	var bases []string
	if filepath.IsAbs(name) {
		bases = []string{""}
	} else {
		bases = append(bases, r.roots...)
		bases = append(bases, filepath.Dir(from))
	}
	for _, base := range bases {
		p := filepath.Join(base, name)
		for _, ext := range append([]string{""}, compressionExts...) {
			if fi, err := os.Stat(p + ext); err == nil && fi.Mode().IsRegular() {
				return p + ext, nil
			}
		}
	}
	return "", fmt.Errorf("%s: can't find %s for .so", from, name)
}

// read returns a file's decompressed contents.
func (r *resolver) read(path string) ([]byte, error) {
	if err := r.allowed(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Decompress(f, path)
}

// Decompress reads everything from f, which came from the file at path,
// undoing any compression. gzip and bzip2 are handled here; anything else
// goes through the matching command-line tool.
func Decompress(f io.Reader, path string) ([]byte, error) {
	br := bufio.NewReader(f)
	magic, _ := br.Peek(6)
	var rd io.Reader
	var tool []string
	switch {
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		gz.Multistream(true)
		rd = gz
	case bytes.HasPrefix(magic, []byte("BZh")):
		rd = bzip2.NewReader(br)
	case bytes.HasPrefix(magic, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		tool = []string{"xz", "-dc"}
	case bytes.HasPrefix(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		tool = []string{"zstd", "-dcq"}
	case strings.HasSuffix(path, ".lzma"):
		tool = []string{"xz", "-dc", "--format=lzma"}
	case strings.HasSuffix(path, ".lz"):
		tool = []string{"lzip", "-dc"}
	case bytes.HasPrefix(magic, []byte{0x1f, 0x9d}):
		tool = []string{"gzip", "-dc"}
	default:
		rd = br
	}
	if tool != nil {
		cmd := exec.Command(tool[0], tool[1:]...)
		cmd.Stdin = br
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return nil, fmt.Errorf("%s: %s: %s", path, tool[0], msg)
		}
		return out, nil
	}
	b, err := io.ReadAll(rd)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return b, nil
}

// SourceText renders roff source to plain text the same way Text renders a
// file. name ("ls.1") is the file name to give it, which man uses to pick
// the section and encoding.
func SourceText(ctx context.Context, src []byte, name string, o Options) (string, error) {
	dir, err := os.MkdirTemp("", "girlpages-src-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, filepath.Base(name))
	if err := os.WriteFile(tmp, src, 0o600); err != nil {
		return "", err
	}
	text, err := Text(ctx, tmp, o)
	if err != nil {
		return "", errors.New(strings.Replace(err.Error(), tmp, name, 1))
	}
	return text, nil
}
