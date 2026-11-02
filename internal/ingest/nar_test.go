// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package ingest

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// narString writes a NAR string: its length, itself, and padding.
func narString(b *bytes.Buffer, s string) {
	binary.Write(b, binary.LittleEndian, uint64(len(s)))
	b.WriteString(s)
	b.Write(make([]byte, (8-len(s)%8)%8))
}

func TestExtractNAR(t *testing.T) {
	var b bytes.Buffer
	w := func(ss ...string) {
		for _, s := range ss {
			narString(&b, s)
		}
	}
	entry := func(name string, node func()) {
		w("entry", "(", "name", name, "node")
		node()
		w(")")
	}
	file := func(body string, exec bool) func() {
		return func() {
			w("(", "type", "regular")
			if exec {
				w("executable", "")
			}
			w("contents", body, ")")
		}
	}
	link := func(target string) func() { return func() { w("(", "type", "symlink", "target", target, ")") } }
	dir := func(entries func()) func() {
		return func() {
			w("(", "type", "directory")
			entries()
			w(")")
		}
	}
	w("nix-archive-1")
	dir(func() {
		entry("bin", dir(func() { entry("ls", file("binary", true)) }))
		entry("share", dir(func() {
			entry("man", dir(func() {
				entry("man1", dir(func() {
					entry("dir.1", link("ls.1"))
					entry("ls.1", file(".TH LS 1\n", false))
					entry("vdir.1", link("/nix/store/abc-other/share/man/man1/vdir.1"))
				}))
			}))
		}))
	})()
	path := filepath.Join(t.TempDir(), "x.nar")
	os.WriteFile(path, b.Bytes(), 0o644)
	out := t.TempDir()
	if err := Extract(path, out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(out, "share/man/man1/ls.1")); string(got) != ".TH LS 1\n" {
		t.Errorf("ls.1: %q", got)
	}
	if target, err := os.Readlink(filepath.Join(out, "share/man/man1/dir.1")); err != nil || target != "ls.1" {
		t.Errorf("dir.1: %q %v", target, err)
	}
	for _, gone := range []string{"bin/ls", "share/man/man1/vdir.1"} {
		if _, err := os.Lstat(filepath.Join(out, gone)); err == nil {
			t.Errorf("%s was extracted", gone)
		}
	}
	// Truncated: an error, not a hang or a panic.
	os.WriteFile(path, b.Bytes()[:b.Len()/2], 0o644)
	if err := Extract(path, t.TempDir()); err == nil {
		t.Error("a truncated NAR extracted")
	}
}
