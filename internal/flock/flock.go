// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package flock takes exclusive locks on open files, between processes:
// flock(2) on Unix, LockFileEx on Windows. A lock goes when its file is
// closed.
package flock

import (
	"errors"
	"os"
)

// ErrLocked is what TryLock returns when another process has the lock.
var ErrLocked = errors.New("locked by another process")

// Lock takes an exclusive lock on f, waiting for it if another process
// has it.
func Lock(f *os.File) error { return lock(f, true) }

// TryLock takes an exclusive lock on f if no other process has it, and
// otherwise returns ErrLocked.
func TryLock(f *os.File) error { return lock(f, false) }

// Unlock lets f's lock go (as closing it does).
func Unlock(f *os.File) error { return unlock(f) }
