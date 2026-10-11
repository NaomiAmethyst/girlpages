// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Client fetches from a published repository: an http(s) URL, a file://
// URL or a local directory.
type Client struct {
	Base string
	HTTP *http.Client
	// UserAgent is sent with requests.
	UserAgent string
}

// NewClient makes a client for base with the given request timeout.
func NewClient(base string, timeout time.Duration) *Client {
	return &Client{Base: base, HTTP: &http.Client{Timeout: timeout}}
}

// maxDocument caps how much of any one document is read.
const maxDocument = 64 << 20

// Get fetches and decodes the document at rel. Missing documents give an
// error wrapping ErrNotFound.
func (c *Client) Get(ctx context.Context, rel string, v any) error {
	b, err := c.Raw(ctx, rel)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("repo %s: %w", rel, err)
	}
	return nil
}

// Raw fetches the document at rel as it is.
func (c *Client) Raw(ctx context.Context, rel string) ([]byte, error) {
	base := c.Base
	if dir, ok := strings.CutPrefix(base, "file://"); ok {
		base = dir
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		f, err := os.Open(filepath.Join(base, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", rel, ErrNotFound)
		}
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, maxDocument))
	}
	u := strings.TrimSuffix(base, "/") + "/" + escapePath(rel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, fmt.Errorf("%s: %w", rel, ErrNotFound)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("repo %s: %s", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDocument))
}

// escapePath percent-encodes each segment of a relative path, so the %XX
// in escaped names reaches the server intact.
func escapePath(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// Variant fetches one variant of a page version.
func (c *Client) Variant(ctx context.Context, source, variant string) (*Variant, error) {
	if !ValidHash(source) {
		return nil, fmt.Errorf("bad source hash %q", source)
	}
	var v Variant
	if err := c.Get(ctx, ContentPath(source, variant), &v); err != nil {
		return nil, err
	}
	if v.Source != source || v.Variant != variant {
		return nil, fmt.Errorf("repo sent %s/%s for %s/%s", v.Source, v.Variant, source, variant)
	}
	return &v, nil
}

// Manifest fetches manifest.json.
func (c *Client) Manifest(ctx context.Context) (*Manifest, error) {
	var m Manifest
	if err := c.Get(ctx, ManifestPath(), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Find finds a published repository at src (a URL or a directory): the
// repository itself, or a site with it in repo/.
func Find(ctx context.Context, src string, timeout time.Duration, userAgent string) (*Client, error) {
	src = strings.TrimSuffix(src, "/")
	var firstErr error
	for _, base := range []string{src, src + "/repo"} {
		c := NewClient(base, timeout)
		c.UserAgent = userAgent
		_, err := c.Manifest(ctx)
		if err == nil {
			return c, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("no published girlpages repository at %s (%v)", src, firstErr)
}

// Girlified says which versions a repository has girl variants of, from
// its girlified-sources index, fetching each shard once.
type Girlified struct {
	c *Client

	mu     sync.Mutex
	loaded bool
	ranges []ShardRange // nil if the repository has no index
	shards map[string]map[string]bool
}

// Girlified returns a Girlified for the repository.
func (c *Client) Girlified() *Girlified {
	return &Girlified{c: c, shards: map[string]map[string]bool{}}
}

// ErrNoIndex means a repository was published without a girlified-sources
// index (by an older girlrepo).
var ErrNoIndex = errors.New("the repository has no girlified-sources index")

// Has reports whether the repository has a girl variant of the version
// with this source hash.
func (g *Girlified) Has(ctx context.Context, source string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.loaded {
		m, err := g.c.Manifest(ctx)
		if err != nil {
			return false, err
		}
		if m.Index.Sources != "" {
			var sm ShardManifest
			if err := g.c.Get(ctx, IndexManifestPath("sources", m.Index.Sources), &sm); err != nil {
				return false, err
			}
			g.ranges = sm.Shards
		}
		g.loaded = true
	}
	if g.ranges == nil {
		return false, ErrNoIndex
	}
	if len(source) < SourceKeyLength {
		return false, nil
	}
	key := source[:SourceKeyLength]
	r := findRange(g.ranges, key)
	if r == nil {
		return false, nil
	}
	set, ok := g.shards[r.SHA256]
	if !ok {
		if !ValidHash(r.SHA256) {
			return false, fmt.Errorf("bad shard hash %q", r.SHA256)
		}
		var sh SourceShard
		if err := g.c.Get(ctx, ShardPath("sources", r.SHA256), &sh); err != nil {
			return false, err
		}
		set = make(map[string]bool, len(sh.Sources))
		for _, k := range sh.Sources {
			if len(k) >= SourceKeyLength {
				set[k[:SourceKeyLength]] = true
			}
		}
		g.shards[r.SHA256] = set
	}
	return set[key], nil
}
