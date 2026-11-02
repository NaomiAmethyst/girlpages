// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDebianToSPDX(t *testing.T) {
	for in, want := range map[string]string{
		"GPL-3+":                        "GPL-3.0-or-later",
		"GPL-2":                         "GPL-2.0-only",
		"LGPL-2.1+":                     "LGPL-2.1-or-later",
		"GPL-1+ or Artistic":            "GPL-1.0-or-later OR Artistic-1.0-Perl",
		"Expat":                         "MIT",
		"BSD-3-clause":                  "BSD-3-Clause",
		"GFDL-NIV-1.3+":                 "GFDL-1.3-or-later-no-invariants",
		"GPL-2+ with OpenSSL-exception": "GPL-2.0-or-later WITH OpenSSL-exception",
		"other":                         "other",
		"":                              "",
	} {
		if got := DebianToSPDX(in); got != want {
			t.Errorf("DebianToSPDX(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDebianLicense(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	write("usr/share/doc/tar/copyright", `Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: tar

Files: debian/*
Copyright: 2024 Someone
License: GPL-2+

Files: *
Copyright: 1985-2024 Free Software Foundation, Inc.
License: GPL-3+

License: GPL-3+
 On Debian systems, the complete text is in /usr/share/common-licenses/GPL-3.
`)
	if got := DebianLicense(dir, "tar"); got != "GPL-3.0-or-later" {
		t.Errorf("tar: %q", got)
	}
	// A -doc package whose doc directory is its source package's.
	if got := DebianLicense(dir, "tar-doc"); got != "GPL-3.0-or-later" {
		t.Errorf("tar-doc: %q", got)
	}
	// Not machine-readable: nothing to say.
	write("usr/share/doc/old/copyright", "This package was debianized by someone in 1999.\nIt's GPL.\n")
	os.RemoveAll(filepath.Join(dir, "usr/share/doc/tar"))
	if got := DebianLicense(dir, "old"); got != "" {
		t.Errorf("old: %q", got)
	}
}
