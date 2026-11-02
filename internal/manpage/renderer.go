// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package manpage

import (
	"os"
	"os/exec"
	"strings"
	"sync"
)

// What pages are rendered with (see Text).
const (
	RendererManDB  = "man-db" // man -l, with man-db's own options
	RendererMandoc = "mandoc" // mandoc, as FreeBSD's man uses
	RendererGroff  = "groff"  // groff, with no man-db or mandoc
)

var renderer struct {
	once sync.Once
	kind string
}

// Renderer is what pages are rendered with: man-db's man, if that's the
// man there is, else mandoc, if it's there, else groff. GIRLPAGES_RENDERER
// picks one.
func Renderer() string {
	renderer.once.Do(func() {
		switch r := os.Getenv("GIRLPAGES_RENDERER"); r {
		case RendererManDB, RendererMandoc, RendererGroff:
			renderer.kind = r
			return
		}
		renderer.kind = detectRenderer()
	})
	return renderer.kind
}

func detectRenderer() string {
	// man-db's man --version says "man 2.12.0"; FreeBSD's and mandoc's
	// man don't know --version.
	if out, err := exec.Command("man", "--version").Output(); err == nil && strings.HasPrefix(string(out), "man ") {
		return RendererManDB
	}
	if _, err := exec.LookPath("mandoc"); err == nil {
		return RendererMandoc
	}
	if _, err := exec.LookPath("groff"); err == nil {
		return RendererGroff
	}
	return RendererManDB // (which will say what's missing)
}
