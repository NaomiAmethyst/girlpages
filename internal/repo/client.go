// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"compress/gzip"
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

	"github.com/andybalholm/brotli"
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
	// Repositories are served with pre-compressed copies of every file
	// (see girlrepo publish), and their JSON compresses very well.
	req.Header.Set("Accept-Encoding", AcceptEncoding)
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
	body, err := decode(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", u, err)
	}
	defer body.Close()
	// The limit is on what it decompresses to, so a small compressed file
	// can't swell to fill memory.
	b, err := io.ReadAll(io.LimitReader(body, maxDocument+1))
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", u, err)
	}
	if len(b) > maxDocument {
		return nil, fmt.Errorf("repo %s: bigger than %d bytes", u, maxDocument)
	}
	return b, nil
}

// AcceptEncoding is what the client asks for: brotli, which makes the
// smallest repository files, or gzip. (Not zstd, which repositories have
// too, for browsers: it's bigger here, and its decoder would be most of
// what girlc is built with besides Go's own.)
const AcceptEncoding = "br, gzip"

// decode undoes the Content-Encoding of a response body.
func decode(r io.Reader, encoding string) (io.ReadCloser, error) {
	encoding = strings.ToLower(strings.TrimSpace(encoding))
	if encoding == "" || encoding == "identity" {
		return io.NopCloser(r), nil
	}
	if strings.Contains(encoding, ",") {
		return nil, fmt.Errorf("can't undo layered Content-Encoding %q", encoding)
	}
	switch encoding {
	case "br":
		return io.NopCloser(brotli.NewReader(r)), nil
	case "gzip":
		return gzip.NewReader(r)
	}
	return nil, fmt.Errorf("unexpected Content-Encoding %q", encoding)
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

// Variant fetches the latest generation of one variant of a page version:
// its variants.json, then the file that names, checked against its hash.
func (c *Client) Variant(ctx context.Context, source, variant string) (*Variant, error) {
	if !ValidHash(source) {
		return nil, fmt.Errorf("bad source hash %q", source)
	}
	if !ValidVariant(variant) {
		return nil, fmt.Errorf("bad variant name %q", variant)
	}
	var cs Contents
	if err := c.Get(ctx, ContentsPath(source), &cs); err != nil {
		return nil, err
	}
	g := cs.Latest(variant)
	if g == nil {
		return nil, fmt.Errorf("%s/%s: %w", source, variant, ErrNotFound)
	}
	return c.VariantAt(ctx, source, variant, g.SHA256)
}

// VariantAt fetches a particular generation of a variant, by its hash.
func (c *Client) VariantAt(ctx context.Context, source, variant, sha string) (*Variant, error) {
	if !ValidHash(source) || !ValidHash(sha) || !ValidVariant(variant) {
		return nil, fmt.Errorf("bad variant %s/%s/%s", source, variant, sha)
	}
	raw, err := c.Raw(ctx, VariantPath(source, variant, sha))
	if err != nil {
		return nil, err
	}
	if Hash(raw) != sha {
		return nil, fmt.Errorf("repo sent something else for %s/%s", source, variant)
	}
	var v Variant
	if err := json.Unmarshal(raw, &v); err != nil {
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

// VariantIndex says which versions a repository has one variant of, from
// that variant's index, fetching each shard once.
type VariantIndex struct {
	c       *Client
	variant string

	mu     sync.Mutex
	loaded bool
	ranges []ShardRange // nil if the repository has no index for it
	shards map[string]map[string]bool
}

// VariantIndex returns the index of the versions that have a variant.
func (c *Client) VariantIndex(variant string) *VariantIndex {
	return &VariantIndex{c: c, variant: variant, shards: map[string]map[string]bool{}}
}

// ErrNoIndex means a repository has no index for a variant: it was
// published by an older girlrepo, or has none of that variant.
var ErrNoIndex = errors.New("the repository has no index for that variant")

// Has reports whether the repository has the variant of the version with
// this source hash.
func (x *VariantIndex) Has(ctx context.Context, source string) (bool, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	kind := VariantIndexKind(x.variant)
	if !x.loaded {
		if !ValidVariant(x.variant) {
			return false, fmt.Errorf("bad variant name %q", x.variant)
		}
		m, err := x.c.Manifest(ctx)
		if err != nil {
			return false, err
		}
		if sha := m.Index.Variants[x.variant]; sha != "" {
			var sm ShardManifest
			if err := x.c.Get(ctx, IndexManifestPath(kind, sha), &sm); err != nil {
				return false, err
			}
			x.ranges = sm.Shards
		}
		x.loaded = true
	}
	if x.ranges == nil {
		return false, ErrNoIndex
	}
	if len(source) < SourceKeyLength {
		return false, nil
	}
	key := source[:SourceKeyLength]
	r := findRange(x.ranges, key)
	if r == nil {
		return false, nil
	}
	set, ok := x.shards[r.SHA256]
	if !ok {
		if !ValidHash(r.SHA256) {
			return false, fmt.Errorf("bad shard hash %q", r.SHA256)
		}
		var sh SourceShard
		if err := x.c.Get(ctx, ShardPath(kind, r.SHA256), &sh); err != nil {
			return false, err
		}
		set = make(map[string]bool, len(sh.Sources))
		for _, k := range sh.Sources {
			if len(k) >= SourceKeyLength {
				set[k[:SourceKeyLength]] = true
			}
		}
		x.shards[r.SHA256] = set
	}
	return set[key], nil
}

// ChapterPages fetches every page in a chapter's index, shards and all.
func (c *Client) ChapterPages(ctx context.Context, e ChapterEntry) ([]IndexPage, error) {
	return chapterPages(func(path string, v any) error { return c.Get(ctx, path, v) }, e)
}

// Page fetches a page's listing by its name and section.
func (c *Client) Page(ctx context.Context, name, section string) (*Listing, error) {
	var p Pointer
	if err := c.Get(ctx, NamePath(name, section), &p); err != nil {
		return nil, err
	}
	var l Listing
	if err := c.Get(ctx, ListingPath(p.SHA256), &l); err != nil {
		return nil, err
	}
	return &l, nil
}
