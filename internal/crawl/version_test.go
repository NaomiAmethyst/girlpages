// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package crawl

import "testing"

func TestCompareDeb(t *testing.T) {
	// Each is older than the next.
	order := []string{"1.0~rc1", "1.0", "1.0-1", "1.0-1+b1", "1.0-2", "1.0a", "1.0.1", "1.2", "1.10", "1:0.9"}
	for i := 0; i+1 < len(order); i++ {
		if c := CompareDeb(order[i], order[i+1]); c != -1 {
			t.Errorf("CompareDeb(%q, %q) = %d, want -1", order[i], order[i+1], c)
		}
		if c := CompareDeb(order[i+1], order[i]); c != 1 {
			t.Errorf("CompareDeb(%q, %q) = %d, want 1", order[i+1], order[i], c)
		}
	}
	if CompareDeb("2.10-5ubuntu0.25.10.1", "2.10-5ubuntu0.25.10.1") != 0 {
		t.Error("equal versions")
	}
}

func TestCompareRPM(t *testing.T) {
	order := []string{"1.0~rc1-1", "1.0-1", "1.0-1.el9", "1.0-2", "1.0^git1-1", "1.0a-1", "1.0.1-1", "1.10-1", "2:1.34-11.el9", "2:1.34-13.el9_8"}
	for i := 0; i+1 < len(order); i++ {
		if c := CompareRPM(order[i], order[i+1]); c != -1 {
			t.Errorf("CompareRPM(%q, %q) = %d, want -1", order[i], order[i+1], c)
		}
		if c := CompareRPM(order[i+1], order[i]); c != 1 {
			t.Errorf("CompareRPM(%q, %q) = %d, want 1", order[i+1], order[i], c)
		}
	}
}
