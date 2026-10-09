// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package pager shows output through a pager, the way man does.
package pager

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Command picks the pager: an explicit choice, then $GIRLPAGER, then
// $PAGER, then less. $MANPAGER is deliberately skipped because it is often
// set up to strip the formatting girl is all about.
func Command(explicit string) string {
	for _, p := range []string{explicit, os.Getenv("GIRLPAGER"), os.Getenv("PAGER")} {
		if strings.TrimSpace(p) != "" {
			return p
		}
	}
	return "less"
}

// Show writes text through the pager. A pager of "cat" (or one that can't
// be found) just writes to stdout.
func Show(pagerCmd, text, prompt string) error {
	fields := strings.Fields(pagerCmd)
	if len(fields) == 0 || pagerCmd == "cat" || pagerCmd == "-" {
		_, err := io.WriteString(os.Stdout, text)
		return err
	}
	if _, err := exec.LookPath(fields[0]); err != nil && !strings.ContainsAny(fields[0], "=$'\"") {
		// No such pager: just print, rather than losing the page to sh.
		_, err := io.WriteString(os.Stdout, text)
		return err
	}
	env := os.Environ()
	// less needs -R to pass colours through. Keep any LESS the user set and
	// add what we need, much as man-db does.
	less := os.Getenv("LESS")
	if !strings.Contains(less, "R") {
		less = strings.TrimSpace("-R " + less)
	}
	if prompt != "" && !strings.Contains(less, "P") {
		p := escapeLessPrompt(prompt) + ` ?ltline %lt?L/%L.:byte %bB?s/%s..?e (END):?pB %pB\%.. (press h for help or q to quit)$`
		less += " -Ps" + p + " -Pm" + p + " -PM" + p
	}
	env = append(env, "LESS="+less, "LESSCHARSET="+lessCharset())
	cmd := exec.Command("/bin/sh", "-c", pagerCmd)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		_, werr := io.WriteString(os.Stdout, text)
		return werr
	}
	_, werr := io.WriteString(stdin, text)
	stdin.Close()
	err = cmd.Wait()
	// Quitting the pager early closes the pipe; that's not an error.
	if werr != nil && !errors.Is(werr, syscall.EPIPE) {
		return werr
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil
	}
	return err
}

func lessCharset() string {
	if v := os.Getenv("LESSCHARSET"); v != "" {
		return v
	}
	return "utf-8"
}

// escapeLessPrompt escapes characters that are special in less prompts.
func escapeLessPrompt(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `?`, `\?`, `:`, `\:`, `.`, `\.`, `%`, `\%`)
	return r.Replace(s)
}
