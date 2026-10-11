// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package crawl

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrNotFound is returned for files the server doesn't have.
var ErrNotFound = errors.New("not found")

// Fetcher downloads from package repositories.
type Fetcher struct {
	Client    *http.Client
	UserAgent string
	Retries   int
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return http.DefaultClient
}

// Open starts downloading u. file:// URLs and plain paths are read from
// disk, which is handy for local mirrors.
func (f *Fetcher) Open(ctx context.Context, u string) (io.ReadCloser, error) {
	if path, ok := strings.CutPrefix(u, "file://"); ok || !strings.Contains(u, "://") {
		if !ok {
			path = u
		}
		r, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", u, ErrNotFound)
		}
		return r, err
	}
	var lastErr error
	for attempt := 0; attempt <= f.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if f.UserAgent != "" {
			req.Header.Set("User-Agent", f.UserAgent)
		}
		resp, err := f.client().Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return resp.Body, nil
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusForbidden:
			resp.Body.Close()
			return nil, fmt.Errorf("%s: %w", u, ErrNotFound)
		default:
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: %s", u, resp.Status)
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return nil, lastErr
			}
		}
	}
	return nil, lastErr
}

// OpenFirst opens the first of urls that exists, returning which it was.
func (f *Fetcher) OpenFirst(ctx context.Context, urls ...string) (io.ReadCloser, string, error) {
	for _, u := range urls {
		r, err := f.Open(ctx, u)
		if err == nil {
			return r, u, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("none of %s: %w", strings.Join(urls, ", "), ErrNotFound)
}

// Download saves u to path, checking its SHA-256 if one is given.
func (f *Fetcher) Download(ctx context.Context, u, sum, path string) (int64, error) {
	r, err := f.Open(ctx, u)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	out, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("%s: %w", u, err)
	}
	if sum != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), sum) {
		return n, fmt.Errorf("%s: checksum doesn't match the index", u)
	}
	return n, nil
}

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

// openIndex opens the first index that exists and decompresses it.
func (f *Fetcher) openIndex(ctx context.Context, urls ...string) (io.ReadCloser, string, error) {
	r, u, err := f.OpenFirst(ctx, urls...)
	if err != nil {
		return nil, "", err
	}
	d, err := Decompress(r)
	if err != nil {
		r.Close()
		return nil, "", fmt.Errorf("%s: %w", u, err)
	}
	return &both{d, r}, u, nil
}

type both struct {
	io.ReadCloser
	under io.Closer
}

func (b *both) Close() error {
	err := b.ReadCloser.Close()
	b.under.Close()
	return err
}

// join resolves ref against a base URL (or path) that names a directory.
func join(base, ref string) string {
	if strings.Contains(ref, "://") {
		return ref
	}
	if !strings.Contains(base, "://") || strings.HasPrefix(base, "file://") {
		return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(ref, "/")
	}
	b, err := url.Parse(strings.TrimSuffix(base, "/") + "/")
	if err != nil {
		return strings.TrimSuffix(base, "/") + "/" + ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return strings.TrimSuffix(base, "/") + "/" + ref
	}
	return b.ResolveReference(r).String()
}
