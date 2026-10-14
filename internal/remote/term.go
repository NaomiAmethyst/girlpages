// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package remote

import (
	"os"
	"strconv"

	"golang.org/x/term"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/render"
)

// Terminal is how pages are shown on stdout.
type Terminal struct {
	// TTY is whether stdout is a terminal (so pages are paged).
	TTY   bool
	Width int
	Color render.ColorMode
}

// Detect works out how pages are shown: color is a --color flag's value
// (auto, always or never).
func Detect(color string) Terminal {
	t := Terminal{TTY: term.IsTerminal(int(os.Stdout.Fd())), Width: manpage.Width}
	if w, err := strconv.Atoi(os.Getenv("MANWIDTH")); err == nil && w > 0 {
		t.Width = w
	} else if t.TTY {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			// Long lines of chatty prose are hard to read; man pages
			// traditionally top out around here anyway.
			t.Width = min(w, 100)
		}
	}
	switch color {
	case "always", "yes", "force":
		t.Color = render.DetectColor()
		if t.Color == render.NoColor {
			t.Color = render.Color256
		}
	case "never", "no", "none":
		t.Color = render.NoColor
	default:
		if t.TTY {
			t.Color = render.DetectColor()
		}
	}
	return t
}
