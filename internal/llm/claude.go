// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Claude runs the claude CLI in print mode (claude -p), with tools off and
// no session saved, sending the page on stdin.
type Claude struct {
	Command   string
	ModelName string
	Args      []string
	Timeout   time.Duration
}

func (c *Claude) Name() string  { return "claude" }
func (c *Claude) Model() string { return c.ModelName }

func (c *Claude) args(system string) []string {
	args := []string{
		"-p",
		"--output-format", "text",
		"--no-session-persistence",
		"--system-prompt", system,
		"--tools", "",
		// Keep the user's own Claude Code setup (MCP servers, skills,
		// hooks, settings) out of the conversation.
		"--strict-mcp-config",
		"--disable-slash-commands",
		"--setting-sources", "",
	}
	if c.ModelName != "" {
		args = append(args, "--model", c.ModelName)
	}
	return append(args, c.Args...)
}

func (c *Claude) Complete(ctx context.Context, system, user string) (string, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.Command, c.args(system)...)
	// Run somewhere neutral so no project CLAUDE.md or settings get pulled in.
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(user)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		if msg != "" {
			return "", fmt.Errorf("%s: %v: %s", c.Command, err, msg)
		}
		return "", fmt.Errorf("%s: %w", c.Command, err)
	}
	return stdout.String(), nil
}
