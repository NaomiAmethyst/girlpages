// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package serve serves a published site (its website and repository)
// the way its production server should: a stored compressed copy when
// the client accepts one, Cache-Control by kind of file, strong ETags,
// the site's 404 page; from a directory (DirSource) or an archive
// (ArchiveSource). It also has what running that in production wants:
// health checks and Prometheus metrics (Admin), and access logs.
package serve

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cache lifetimes for each kind of file on a published site. Production
// servers should send the same; see girlrepo(1).
const (
	// CacheImmutable is for files named after their own hash.
	CacheImmutable = "public, max-age=31536000, immutable"
	// CachePointer is for manifest.json and the files that point into the
	// immutable ones (name and section pointers, variants.json): they
	// change whenever the repository is published.
	CachePointer = "public, max-age=300"
	// CacheSite is for the website's pages, scripts and styles.
	CacheSite = "public, max-age=3600"
	// CacheRevalidate is for the service worker, which browsers must
	// always check for updates.
	CacheRevalidate = "no-cache"
)

var hashNamed = regexp.MustCompile(`(^|/|-)[0-9a-f]{64}\.json$|^repo/packs/[0-9a-f]{64}\.tar\.zst$`)

// CacheControl is the Cache-Control a file on the site (by its path from
// the site's root) should be served with.
func CacheControl(rel string) string {
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	switch {
	case rel == "sw.js":
		return CacheRevalidate
	case strings.HasPrefix(rel, "repo/") && hashNamed.MatchString(rel):
		return CacheImmutable
	case strings.HasPrefix(rel, "repo/"):
		return CachePointer
	}
	return CacheSite
}

// Encodings are the compressed forms a source may have. Of those a
// client accepts, the smallest is sent; this order breaks ties.
var Encodings = []string{"zstd", "br", "gzip"}

// A Source is where a site's files come from.
type Source interface {
	// Find finds a file (by its slash path from the site's root, which
	// is never a directory).
	Find(rel string) (File, bool)
	// Ready reports whether there's a site to serve.
	Ready() bool
}

// A File is one file of a site.
type File interface {
	// Has reports whether the file has a compressed form.
	Has(encoding string) bool
	// Size is a form's size ("" for the file itself), or -1 if it's not
	// known.
	Size(encoding string) int64
	// Open opens a form of it ("" for the file itself).
	Open(encoding string) (io.ReadSeeker, error)
	// ETag is a form's strong entity tag, quoted, or "" for none.
	ETag(encoding string) string
	// ModTime is when it changed, if that's known.
	ModTime() time.Time
	// Close lets go of it.
	Close() error
}

// Options tunes Handler.
type Options struct {
	// AccessLog, if set, gets a line of JSON for each request.
	AccessLog io.Writer
	// Metrics, if set, counts requests.
	Metrics *Metrics
}

// Handler serves a site from src.
func Handler(src Source, o Options) http.Handler {
	var logMu sync.Mutex
	infos := &siteInfos{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &recorder{ResponseWriter: w, status: http.StatusOK}
		serveFile(rw, r, src, infos)
		d := time.Since(start)
		if o.Metrics != nil {
			o.Metrics.observe(rw.status, rw.bytes, d)
		}
		if o.AccessLog != nil {
			line, _ := json.Marshal(map[string]any{
				"time": start.UTC().Format(time.RFC3339Nano), "method": r.Method, "path": r.URL.Path,
				"status": rw.status, "bytes": rw.bytes, "ms": float64(d.Microseconds()) / 1000,
				"encoding": rw.Header().Get("Content-Encoding"), "remote": r.RemoteAddr,
				"referer": r.Referer(), "agent": r.UserAgent(),
			})
			logMu.Lock()
			o.AccessLog.Write(append(line, '\n'))
			logMu.Unlock()
		}
	})
}

func serveFile(w http.ResponseWriter, r *http.Request, src Source, infos *siteInfos) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !src.Ready() {
		h.Set("Retry-After", "10")
		http.Error(w, "the site isn't ready yet", http.StatusServiceUnavailable)
		return
	}
	rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	f, ok := (File)(nil), false
	if rel != "" {
		f, ok = src.Find(rel)
	}
	if !ok {
		// A directory: its index.html, at its address with a slash.
		idx := path.Join(rel, "index.html")
		if f, ok = src.Find(idx); ok {
			if rel != "" && !strings.HasSuffix(r.URL.Path, "/") {
				f.Close()
				target := r.URL.Path + "/"
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, target, http.StatusMovedPermanently)
				return
			}
			rel = idx
		}
	}
	status := http.StatusOK
	if !ok {
		// Not a file: a voice's address (the app shows it), or a series'
		// old one.
		shell, redirect := infos.get(src).route(rel)
		switch {
		case redirect != "":
			target := "/" + redirect
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		case shell:
			if f, ok = src.Find(ShellPath); ok {
				rel = ShellPath
			}
		}
	}
	if !ok {
		if f, ok = src.Find("404.html"); !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		rel, status = "404.html", http.StatusNotFound
	}
	defer f.Close()
	h.Set("Vary", "Accept-Encoding")
	if ct := mime.TypeByExtension(path.Ext(rel)); ct != "" {
		h.Set("Content-Type", ct)
	} else if path.Ext(rel) == ".webmanifest" {
		h.Set("Content-Type", "application/manifest+json")
	}
	enc := pick(f, r.Header.Get("Accept-Encoding"))
	rs, err := f.Open(enc)
	if err != nil {
		http.Error(w, "the file couldn't be read", http.StatusInternalServerError)
		return
	}
	if c, ok := rs.(io.Closer); ok {
		defer c.Close()
	}
	if enc != "" {
		h.Set("Content-Encoding", enc)
	}
	if status != http.StatusOK {
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		if r.Method == http.MethodGet {
			_, _ = io.Copy(w, rs)
		}
		return
	}
	h.Set("Cache-Control", CacheControl(rel))
	if tag := f.ETag(enc); tag != "" {
		h.Set("ETag", tag)
	}
	http.ServeContent(w, r, rel, f.ModTime(), rs)
}

// pick chooses the form of a file to send: the smallest of the
// compressed ones the client accepts (by their sizes, where they're
// known; else the first of Encodings), or the file itself.
func pick(f File, acceptEncoding string) string {
	best, bestSize := "", int64(-1)
	for _, e := range Encodings {
		if !accepts(acceptEncoding, e) || !f.Has(e) {
			continue
		}
		size := f.Size(e)
		switch {
		case best == "":
			best, bestSize = e, size
		case size >= 0 && (bestSize < 0 || size < bestSize):
			best, bestSize = e, size
		}
	}
	if best != "" {
		if id := f.Size(""); id >= 0 && bestSize >= id {
			return "" // (compressing didn't help)
		}
	}
	return best
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

// recorder notes a response's status and size.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// ReadFrom keeps io.Copy's fast paths (sendfile) for files.
func (r *recorder) ReadFrom(src io.Reader) (int64, error) {
	r.wrote = true
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(src)
		r.bytes += n
		return n, err
	}
	n, err := io.Copy(struct{ io.Writer }{r.ResponseWriter}, src)
	r.bytes += n
	return n, err
}
