// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package ingest

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Decompress wraps r, undoing whatever compression it has (sniffed from
// its first bytes), as a stream: indexes can be far bigger uncompressed.
func Decompress(r io.Reader) (io.ReadCloser, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	magic, _ := br.Peek(6)
	var tool []string
	switch {
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		return gz, nil
	case bytes.HasPrefix(magic, []byte("BZh")):
		return io.NopCloser(bzip2.NewReader(br)), nil
	case bytes.HasPrefix(magic, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		tool = []string{"xz", "-dc"}
	case bytes.HasPrefix(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		tool = []string{"zstd", "-dcq"}
	default:
		return io.NopCloser(br), nil
	}
	cmd := exec.Command(tool[0], tool[1:]...)
	cmd.Stdin = br
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", tool[0], err)
	}
	return &cmdReader{ReadCloser: out, cmd: cmd, stderr: &stderr}, nil
}

type cmdReader struct {
	io.ReadCloser
	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

func (c *cmdReader) Close() error {
	c.ReadCloser.Close()
	if err := c.cmd.Wait(); err != nil && c.stderr.Len() > 0 {
		return fmt.Errorf("%s: %s", c.cmd.Path, strings.TrimSpace(c.stderr.String()))
	}
	return nil
}
