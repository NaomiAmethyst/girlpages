// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import "testing"

func TestVersionStem(t *testing.T) {
	for w, want := range map[string]string{
		"icecast2": "icecast", "python3": "python", "perl5": "perl", "libssl3": "libssl",
		"x86_64": "", "x11": "", "md5sum": "", "ls": "", "fail2ban": "", "2to3": "", "9110": "",
	} {
		if got := versionStem(w); got != want {
			t.Errorf("versionStem(%q) = %q, want %q", w, got, want)
		}
	}
}
