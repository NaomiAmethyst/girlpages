// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package interrupt handles Ctrl+C for long runs: the first asks them to
// stop (cancelling a context, so they can finish what they're writing and
// say what they did), and the second stops them right away, so a run
// that's slow to notice can't hold the terminal hostage.
package interrupt

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

// held counts Holds: while there are any, interrupts are for whatever has
// the terminal (a pager), not this program.
var held atomic.Int32

// Hold keeps Ctrl+C from stopping the program until release is called:
// for while another program it's waiting on has the terminal (a pager,
// where Ctrl+C is for cancelling a search), as man does. (A program
// that's told to stop by SIGTERM still is.)
func Hold() (release func()) {
	held.Add(1)
	// Without a handler, Ctrl+C would stop a program that hasn't got
	// Context's, leaving the pager behind.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	go func() {
		for range ch {
		}
	}()
	return func() {
		signal.Stop(ch)
		close(ch)
		held.Add(-1)
	}
}

// Context is cancelled by the first interrupt (or SIGTERM); the second
// exits, as an interrupted program does (status 130). prog names the
// program in what it says to w. stop stops listening.
func Context(prog string, w io.Writer) (ctx context.Context, stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	// next waits for a signal that's for this program, reporting false
	// once stop is called.
	next := func() bool {
		for {
			select {
			case sig := <-sigs:
				if sig == os.Interrupt && held.Load() > 0 {
					continue
				}
				return true
			case <-done:
				return false
			}
		}
	}
	go func() {
		if !next() {
			return
		}
		fmt.Fprintf(w, "\n%s: stopping… (Ctrl+C again to stop right away)\n", prog)
		cancel()
		if !next() {
			return
		}
		fmt.Fprintf(w, "\n%s: stopped 👋\n", prog)
		os.Exit(130)
	}()
	return ctx, func() {
		signal.Stop(sigs)
		close(done)
		cancel()
	}
}
