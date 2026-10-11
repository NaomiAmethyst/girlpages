// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/NaomiAmethyst/girlpages/internal/precompress"
)

// Cache lifetimes for each kind of file on a published site. Production
// servers should send the same; see girlrepo(1).
const (
	// CacheImmutable is for files named after their own hash.
	CacheImmutable = "public, max-age=31536000, immutable"
	// CachePointer is for manifest.json and the files that point into the
	// immutable ones: they change whenever the repository is published.
	CachePointer = "public, max-age=300"
	// CacheContent is for page contents, which change only when a page is
	// girlified again.
	CacheContent = "public, max-age=86400"
	// CacheSite is for the website's pages, scripts and styles.
	CacheSite = "public, max-age=3600"
	// CacheRevalidate is for the service worker, which browsers must
	// always check for updates.
	CacheRevalidate = "no-cache"
)

var hashNamed = regexp.MustCompile(`(^|/|-)[0-9a-f]{64}\.json$`)

// CacheControl is the Cache-Control a file on the site (by its path from
// the site's root, without its compressed suffix) should be served with.
func CacheControl(rel string) string {
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	switch {
	case rel == "sw.js":
		return CacheRevalidate
	case strings.HasPrefix(rel, "repo/") && hashNamed.MatchString(rel):
		return CacheImmutable
	case strings.HasPrefix(rel, "repo/contents/"):
		return CacheContent
	case strings.HasPrefix(rel, "repo/"):
		return CachePointer
	}
	return CacheSite
}

// Handler serves a built site the way its production server should: a
// pre-compressed copy when the client accepts it (zstd, then brotli,
// then gzip), Cache-Control by CacheControl, and the site's 404 page.
func Handler(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		file := filepath.Join(dir, filepath.FromSlash(rel))
		if fi, err := os.Stat(file); err == nil && fi.IsDir() {
			if !strings.HasSuffix(r.URL.Path, "/") {
				http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
				return
			}
			rel = path.Join(rel, "index.html")
			file = filepath.Join(file, "index.html")
		}
		status := http.StatusOK
		if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
			rel, file, status = "404.html", filepath.Join(dir, "404.html"), http.StatusNotFound
		}
		h := w.Header()
		h.Set("Vary", "Accept-Encoding")
		if ct := mime.TypeByExtension(path.Ext(rel)); ct != "" {
			h.Set("Content-Type", ct)
		} else if path.Ext(rel) == ".webmanifest" {
			h.Set("Content-Type", "application/manifest+json")
		}
		if status == http.StatusOK {
			h.Set("Cache-Control", CacheControl(rel))
		}
		serve := file
		for _, e := range precompress.Encodings {
			if accepts(r.Header.Get("Accept-Encoding"), e.Name) {
				if _, err := os.Stat(file + e.Ext); err == nil {
					serve = file + e.Ext
					h.Set("Content-Encoding", e.Name)
					break
				}
			}
		}
		f, err := os.Open(serve)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			if r.Method == http.MethodGet {
				_, _ = f.WriteTo(w)
			}
			return
		}
		http.ServeContent(w, r, rel, fi.ModTime(), f)
	})
}

// accepts reports whether an Accept-Encoding header allows an encoding:
// it's listed (or * is) without q=0.
func accepts(header, name string) bool {
	for _, part := range strings.Split(header, ",") {
		tok, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		tok = strings.TrimSpace(tok)
		if !strings.EqualFold(tok, name) && tok != "*" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.TrimSpace(k) == "q" {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		return q > 0
	}
	return false
}
