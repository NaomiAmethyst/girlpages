// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// progress shows how long jobs are going: on a terminal, a line rewritten
// in place; otherwise (a log), a line every few seconds.
type progress struct {
	mu    sync.Mutex
	w     io.Writer
	tty   bool
	stage string
	last  time.Time
	shown bool // a line is on the terminal, to be cleared
}

func newProgress(f *os.File) *progress {
	fi, err := f.Stat()
	return &progress{w: f, tty: err == nil && fi.Mode()&os.ModeCharDevice != 0}
}

// report is a repo.Progress.
func (p *progress) report(stage string, done, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	every := 5 * time.Second
	if p.tty {
		every = 100 * time.Millisecond
	}
	fresh := stage != p.stage
	if fresh {
		p.stage = stage
	} else if now.Sub(p.last) < every && (total == 0 || done < total) {
		return
	}
	if !p.tty && !fresh && total > 0 && done == total {
		return // the next stage, or the summary, says it's done
	}
	p.last = now
	msg := "⏳ " + stage
	switch {
	case total > 0 && done == 0:
		msg += fmt.Sprintf(" (%d)", total)
	case total > 0:
		msg += fmt.Sprintf(" %d/%d (%d%%)", done, total, done*100/total)
	case done > 0:
		msg += fmt.Sprintf(" %d", done)
	}
	if p.tty {
		fmt.Fprintf(p.w, "\r\x1b[K%s", msg)
		p.shown = true
	} else {
		fmt.Fprintln(p.w, msg+"…")
	}
}

// say prints a line, clearing any progress first.
func (p *progress) say(format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.shown {
		fmt.Fprint(p.w, "\r\x1b[K")
		p.shown = false
	}
	p.stage = ""
	fmt.Fprintf(p.w, format, a...)
}
