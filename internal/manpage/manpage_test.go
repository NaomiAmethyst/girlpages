// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package manpage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFilename(t *testing.T) {
	cases := map[string][2]string{
		"/usr/share/man/man1/ls.1.gz":            {"ls", "1"},
		"printf.3p.gz":                           {"printf", "3p"},
		"/x/man3/Foo::Bar.3pm.xz":                {"Foo::Bar", "3pm"},
		"EVP_SIGNATURE-CMAC.7ssl.gz":             {"EVP_SIGNATURE-CMAC", "7ssl"},
		"systemd.exec.5":                         {"systemd.exec", "5"},
		"README":                                 {"README", ""},
		"/usr/share/man/man8/x86_64-linux.8.zst": {"x86_64-linux", "8"},
		"/usr/share/man/man1/git-commit.1.bz2":   {"git-commit", "1"},
	}
	for in, want := range cases {
		n, s := ParseFilename(in)
		if n != want[0] || s != want[1] {
			t.Errorf("ParseFilename(%q) = %q, %q; want %q, %q", in, n, s, want[0], want[1])
		}
	}
}

func TestIsSection(t *testing.T) {
	for _, s := range []string{"1", "3p", "3ssl", "8", "n", "l"} {
		if !IsSection(s) {
			t.Errorf("%q should be a section", s)
		}
	}
	for _, s := range []string{"ls", "printf", "", "-a"} {
		if IsSection(s) {
			t.Errorf("%q shouldn't be a section", s)
		}
	}
}

func TestHeaderTitle(t *testing.T) {
	if got := HeaderTitle("TRUE(1)       User Commands       TRUE(1)\n\nNAME\n"); got != "TRUE(1)" {
		t.Errorf("got %q", got)
	}
	if got := HeaderTitle("NAME\n  x\n"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestCleanText(t *testing.T) {
	in := "N\bNA\bAM\bME\bE\n\n       \x1b[1mbold\x1b[0m   \n_\bu_\bl\n\n\n"
	if got := CleanText(in); got != "NAME\n\n       bold\nul\n" {
		t.Errorf("got %q", got)
	}
}

func TestEnumerate(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	mk := func(root, rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(".TH X 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(a, "man1/ls.1.gz")
	mk(a, "man3/printf.3.gz")
	mk(a, "man3/SSL_new.3ssl.gz")
	mk(a, "de/man1/ls.1.gz") // translations are skipped
	mk(b, "man1/ls.1.gz")    // shadowed by a's
	mk(b, "man8/mount.8")
	if err := os.Symlink("nowhere", filepath.Join(b, "man8", "dangling.8")); err != nil {
		t.Fatal(err)
	}

	pages, err := Enumerate([]string{a, b, "/nonexistent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range pages {
		got = append(got, p.Title())
	}
	// Section order is 1, 8, 3; within 3, "3" before "3ssl".
	want := []string{"ls(1)", "mount(8)", "printf(3)", "SSL_new(3ssl)"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if pages[0].Path != filepath.Join(a, "man1/ls.1.gz") {
		t.Errorf("ls came from %s", pages[0].Path)
	}

	pages, _ = Enumerate([]string{a, b}, []string{"3"})
	if len(pages) != 2 {
		t.Errorf("section 3 gave %d pages", len(pages))
	}
}
