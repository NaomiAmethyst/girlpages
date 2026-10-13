// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package version

import "testing"

func TestDeb(t *testing.T) {
	// Each is older than the next.
	order := []string{"1.0~rc1", "1.0", "1.0-1", "1.0-1+b1", "1.0-2", "1.0a", "1.0.1", "1.2", "1.10", "1:0.9"}
	for i := 0; i+1 < len(order); i++ {
		if c := Deb(order[i], order[i+1]); c != -1 {
			t.Errorf("Deb(%q, %q) = %d, want -1", order[i], order[i+1], c)
		}
		if c := Deb(order[i+1], order[i]); c != 1 {
			t.Errorf("Deb(%q, %q) = %d, want 1", order[i+1], order[i], c)
		}
	}
	if Deb("2.10-5ubuntu0.25.10.1", "2.10-5ubuntu0.25.10.1") != 0 {
		t.Error("equal versions")
	}
}

func TestRPM(t *testing.T) {
	order := []string{"1.0~rc1-1", "1.0-1", "1.0-1.el9", "1.0-2", "1.0^git1-1", "1.0a-1", "1.0.1-1", "1.10-1", "2:1.34-11.el9", "2:1.34-13.el9_8"}
	for i := 0; i+1 < len(order); i++ {
		if c := RPM(order[i], order[i+1]); c != -1 {
			t.Errorf("RPM(%q, %q) = %d, want -1", order[i], order[i+1], c)
		}
		if c := RPM(order[i+1], order[i]); c != 1 {
			t.Errorf("RPM(%q, %q) = %d, want 1", order[i+1], order[i], c)
		}
	}
}

func TestUpstream(t *testing.T) {
	for in, want := range map[string]string{
		"1.35+dfsg-3.1build1": "1.35", "1.35-5": "1.35", "2:1.34-13.el9_8": "1.34", "9.7-3": "9.7",
		"1:2.51.0-1ubuntu1": "2.51.0", "v3.5a (549c35b06165)": "3.5a", "3.5a (549c35b06165)": "3.5a",
		"549c35b06165": "", "": "", "4:15.2.0-4ubuntu1": "15.2.0", "2.0+really1.9-1": "1.9",
		"0.0.0~ubuntu24": "0.0.0", "release-1.2 (abc1234)": "1.2", "1.0~rc1-1": "1.0~rc1", "2.21-30.el9_6": "2.21",
		"bcffefbdcaa7": "", "deadbeef1": "", "abcdef0123456789": "",
	} {
		if got := Upstream(in); got != want {
			t.Errorf("Upstream(%q) = %q, want %q", in, got, want)
		}
	}
	if Compare("1.35", "1.34") != 1 || Compare("1.0~rc1", "1.0") != -1 || Compare("3.5a", "3.5") != 1 {
		t.Error("Compare")
	}
}
