// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package flock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	open := func() *os.File {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	a, b := open(), open()
	defer a.Close()
	defer b.Close()
	if err := Lock(a); err != nil {
		t.Fatal(err)
	}
	// (Two opens of a file lock apart, on Unix as between processes.)
	if err := TryLock(b); !errors.Is(err, ErrLocked) {
		t.Fatalf("TryLock while locked: %v", err)
	}
	if err := Unlock(a); err != nil {
		t.Fatal(err)
	}
	if err := TryLock(b); err != nil {
		t.Fatalf("TryLock once unlocked: %v", err)
	}
}
