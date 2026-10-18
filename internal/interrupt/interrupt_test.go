// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package interrupt

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTwice runs itself again as a program interrupted twice: the first
// time cancels its context, and the second stops it.
func TestTwice(t *testing.T) {
	if os.Getenv("INTERRUPT_CHILD") == "1" {
		ctx, stop := Context("child", os.Stdout)
		defer stop()
		syscall.Kill(os.Getpid(), syscall.SIGINT)
		<-ctx.Done()
		fmt.Println("cancelled")
		syscall.Kill(os.Getpid(), syscall.SIGINT)
		time.Sleep(10 * time.Second)
		fmt.Println("still running")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTwice$")
	cmd.Env = append(os.Environ(), "INTERRUPT_CHILD=1")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 130 {
		t.Fatalf("exit: %v\n%s", err, out)
	}
	for _, want := range []string{"child: stopping… (Ctrl+C again to stop right away)", "cancelled", "child: stopped 👋"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "still running") {
		t.Errorf("the second interrupt didn't stop it:\n%s", out)
	}
}

// TestHold checks that Ctrl+C while held (a pager has the terminal) does
// nothing, with Context's handler or without one, and works again after.
func TestHold(t *testing.T) {
	switch os.Getenv("INTERRUPT_CHILD") {
	case "hold":
		ctx, stop := Context("child", os.Stdout)
		defer stop()
		release := Hold()
		syscall.Kill(os.Getpid(), syscall.SIGINT)
		time.Sleep(200 * time.Millisecond)
		fmt.Println("held, cancelled:", ctx.Err() != nil)
		release()
		syscall.Kill(os.Getpid(), syscall.SIGINT)
		select {
		case <-ctx.Done():
			fmt.Println("released, cancelled: true")
		case <-time.After(5 * time.Second):
			fmt.Println("released, cancelled: false")
		}
		return
	case "bare":
		release := Hold()
		syscall.Kill(os.Getpid(), syscall.SIGINT)
		time.Sleep(200 * time.Millisecond)
		fmt.Println("survived")
		release()
		return
	}
	for mode, wants := range map[string][]string{"hold": {"held, cancelled: false", "released, cancelled: true"}, "bare": {"survived"}} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHold$")
		cmd.Env = append(os.Environ(), "INTERRUPT_CHILD="+mode)
		out, err := cmd.CombinedOutput()
		for _, want := range wants {
			if err != nil || !strings.Contains(string(out), want) {
				t.Errorf("%s: no %q (%v):\n%s", mode, want, err, out)
			}
		}
	}
}
