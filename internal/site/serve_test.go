// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"strings"
	"testing"
)

func TestCacheControlPacks(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	for rel, want := range map[string]string{
		"repo/packs/" + sha + ".tar.zst": CacheImmutable,
		"repo/packs/index.json":          CachePointer,
		"repo/manifest.json":             CachePointer,
	} {
		if got := CacheControl(rel); got != want {
			t.Errorf("%s: %q, want %q", rel, got, want)
		}
	}
}
