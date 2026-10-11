// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package manpage

import (
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

func TestSource(t *testing.T) {
	root := filepath.Join(t.TempDir(), "man")
	const inode = ".TH INODE 7\n.SH NAME\ninode \\- file inode information\n"
	writeFile(t, filepath.Join(root, "man7", "inode.7.gz"), gz(t, inode))
	// A stub, as Debian ships them, pointing at a compressed page.
	writeFile(t, filepath.Join(root, "man3", "S_ISBLK.3.gz"), gz(t, ".so man7/inode.7\n"))
	// A symlink alias.
	if err := os.Symlink("inode.7.gz", filepath.Join(root, "man7", "alias.7.gz")); err != nil {
		t.Fatal(err)
	}
	// An include in the middle of a page, found relative to the page.
	writeFile(t, filepath.Join(root, "man1", "common.inc"), []byte(".SH SEE ALSO\nls(1)\n"))
	writeFile(t, filepath.Join(root, "man1", "tool.1"), []byte(".TH TOOL 1\n.so common.inc\n.\\\" end"))

	want := map[string]string{
		"man7/inode.7.gz":   inode,
		"man3/S_ISBLK.3.gz": inode,
		"man7/alias.7.gz":   inode,
		// No final newline in, none out.
		"man1/tool.1": ".TH TOOL 1\n.SH SEE ALSO\nls(1)\n.\\\" end",
	}
	hashes := map[string]string{}
	for rel, w := range want {
		got, err := Source(filepath.Join(root, rel), nil)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if string(got) != w {
			t.Errorf("%s = %q, want %q", rel, got, w)
		}
		h, err := SourceHash(filepath.Join(root, rel), nil)
		if err != nil {
			t.Fatal(err)
		}
		hashes[rel] = h
	}
	if hashes["man7/inode.7.gz"] != hashes["man3/S_ISBLK.3.gz"] || hashes["man7/inode.7.gz"] != hashes["man7/alias.7.gz"] {
		t.Errorf("a page and its aliases should hash the same: %v", hashes)
	}

	writeFile(t, filepath.Join(root, "man3", "broken.3"), []byte(".so man7/nope.7\n"))
	if _, err := Source(filepath.Join(root, "man3", "broken.3"), nil); err == nil || !strings.Contains(err.Error(), "nope.7") {
		t.Errorf("broken .so: err = %v", err)
	}
	writeFile(t, filepath.Join(root, "man3", "loop.3"), []byte(".so man3/loop.3\n"))
	if _, err := Source(filepath.Join(root, "man3", "loop.3"), nil); err == nil {
		t.Error("a .so loop should fail")
	}

	// A target that only exists in another manual root.
	other := filepath.Join(t.TempDir(), "man")
	writeFile(t, filepath.Join(other, "man5", "elsewhere.5"), []byte("elsewhere\n"))
	writeFile(t, filepath.Join(root, "man5", "stub.5"), []byte(".so man5/elsewhere.5\n"))
	if got, err := Source(filepath.Join(root, "man5", "stub.5"), []string{other}); err != nil || string(got) != "elsewhere\n" {
		t.Errorf("other root: %q, %v", got, err)
	}
}

func TestDecompress(t *testing.T) {
	const text = ".TH X 1\nhello\n"
	cases := map[string][]byte{"plain": []byte(text), "gz": gz(t, text)}
	for _, tool := range [][]string{{"bzip2", "bz2"}, {"xz", "xz"}, {"zstd", "zst"}} {
		if _, err := exec.LookPath(tool[0]); err != nil {
			continue
		}
		cmd := exec.Command(tool[0], "-c")
		cmd.Stdin = strings.NewReader(text)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		cases[tool[1]] = out
	}
	for name, data := range cases {
		got, err := Decompress(bytes.NewReader(data), "x.1."+name)
		if err != nil || string(got) != text {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
}

func TestSourceIn(t *testing.T) {
	host := t.TempDir()
	writeFile(t, filepath.Join(host, "secret"), []byte("hunter2\n"))
	pkg := t.TempDir()
	man := filepath.Join(pkg, "usr", "share", "man")
	writeFile(t, filepath.Join(man, "man1", "ok.1"), []byte(".TH OK 1\n"))
	writeFile(t, filepath.Join(man, "man1", "inc.1"), []byte(".so man1/ok.1\n"))
	writeFile(t, filepath.Join(man, "man1", "sneaky.1"), []byte(".so "+filepath.Join(host, "secret")+"\n"))
	if err := os.Symlink(filepath.Join(host, "secret"), filepath.Join(man, "man1", "link.1")); err != nil {
		t.Fatal(err)
	}
	jails := []string{pkg}
	for _, ok := range []string{"ok.1", "inc.1"} {
		if _, err := SourceIn(filepath.Join(man, "man1", ok), nil, jails); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"sneaky.1", "link.1"} {
		if b, err := SourceIn(filepath.Join(man, "man1", bad), nil, jails); err == nil {
			t.Errorf("%s escaped the package: %q", bad, b)
		}
	}
	// Without jails, as for the system's own pages, anything goes.
	if _, err := Source(filepath.Join(man, "man1", "link.1"), nil); err != nil {
		t.Error(err)
	}
}
