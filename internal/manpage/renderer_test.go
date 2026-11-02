// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package manpage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderers renders a page with each renderer there is, which should
// all give text as man-db's does: its title line, its sections, plain.
func TestRenderers(t *testing.T) {
	src := `.TH LS 1 2026-01-01 "GNU coreutils" "User Commands"
.SH NAME
ls \- list directory contents
.SH SYNOPSIS
.B ls
[\fIOPTION\fR]... [\fIFILE\fR]...
.SH DESCRIPTION
List information about the FILEs (the current directory by default).
.TP
\fB\-a\fR, \fB\-\-all\fR
do not ignore entries starting with .
`
	path := filepath.Join(t.TempDir(), "ls.1")
	os.WriteFile(path, []byte(src), 0o644)
	defer func() { renderer.kind = detectRenderer() }()
	renderer.once.Do(func() {})
	tried := 0
	for kind, tool := range map[string]string{RendererManDB: "man", RendererMandoc: "mandoc", RendererGroff: "groff"} {
		if _, err := exec.LookPath(tool); err != nil {
			continue
		}
		if kind == RendererManDB && detectRenderer() != RendererManDB {
			continue // a man that isn't man-db's
		}
		renderer.kind = kind
		text, err := Text(context.Background(), path, Options{})
		if err != nil {
			t.Errorf("%s: %v", kind, err)
			continue
		}
		tried++
		for _, want := range []string{"NAME\n       ls - list directory contents", "SYNOPSIS", "DESCRIPTION", "-a, --all"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s's text lacks %q:\n%s", kind, want, text)
			}
		}
		if HeaderTitle(text) != "LS(1)" || strings.ContainsAny(text, "\b\x1b") {
			t.Errorf("%s's text: title %q, or formatting left in:\n%q", kind, HeaderTitle(text), text)
		}
	}
	if tried == 0 {
		t.Skip("no man-db, mandoc or groff")
	}
}

func TestWhatisLines(t *testing.T) {
	for line, want := range map[string][]string{
		"ls (1)               - list directory contents": {"ls\x001", "list directory contents"},
		"ls(1) - list directory contents":                {"ls\x001", "list directory contents"},
		"chgrp, chown(8) - change file owner and group":  {"chown\x008", "change file owner and group"},
	} {
		m := whatisRE.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("%q didn't match", line)
			continue
		}
		names := strings.Split(m[1], ", ")
		if DescKey(names[len(names)-1], m[2]) != want[0] || m[3] != want[1] {
			t.Errorf("%q: %q", line, m)
		}
	}
}
