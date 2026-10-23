// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package serve

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/archive"
	"github.com/NaomiAmethyst/girlpages/internal/archive/build"
	"github.com/NaomiAmethyst/girlpages/internal/precompress"
)

func put(t *testing.T, root, rel, s string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestArchiveSource(t *testing.T) {
	site, dir := t.TempDir(), filepath.Join(t.TempDir(), "archive")
	page := "<!doctype html>" + strings.Repeat("<p>bestie</p>", 100)
	sha := strings.Repeat("b", 64)
	put(t, site, "index.html", page)
	put(t, site, "404.html", "<!doctype html><p>not here, bestie</p>")
	put(t, site, "girl/1/ls/index.html", page+"ls")
	put(t, site, "repo/manifest.json", `{"format":1,"variants":{"girl":1,"man":1,"pirate":1},"sections":[{"section":"1"},{"section":"rfc"}]}`)
	put(t, site, "static/shell.html", "<!doctype html><html data-shell>")
	put(t, site, "repo/contents/ab/"+sha+"/man-"+sha+".json", `{"variant":"man"}`)
	if _, err := build.Write(context.Background(), site, dir, build.Options{}); err != nil {
		t.Fatal(err)
	}
	a, err := archive.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	src := ArchiveSource{a}
	m := NewMetrics()
	var log bytes.Buffer
	h := Handler(src, Options{Metrics: m, AccessLog: &log})

	w := get(t, h, "GET", "/girl/1/ls/", map[string]string{"Accept-Encoding": "gzip, br;q=0.9, zstd;q=0", "X-Forwarded-For": "203.0.113.7, 10.0.0.2"})
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "br" || w.Header().Get("Cache-Control") != CacheSite ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("page: %d %v", w.Code, w.Header())
	}
	dr, _ := precompress.NewReader("br", w.Body)
	if b, _ := io.ReadAll(dr); string(b) != page+"ls" {
		t.Errorf("body: %.40q", b)
	}
	tag := w.Header().Get("ETag")
	if !strings.HasSuffix(tag, `-br"`) {
		t.Errorf("etag %q", tag)
	}
	if w := get(t, h, "GET", "/girl/1/ls/", map[string]string{"Accept-Encoding": "br", "If-None-Match": tag}); w.Code != 304 {
		t.Errorf("conditional: %d", w.Code)
	}
	if w := get(t, h, "GET", "/girl/1/ls", nil); w.Code != 301 || w.Header().Get("Location") != "/girl/1/ls/" {
		t.Errorf("directory: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := get(t, h, "GET", "/", nil); w.Code != 200 || w.Body.String() != page || w.Header().Get("Content-Encoding") != "" {
		t.Errorf("root: %d %q", w.Code, w.Header().Get("Content-Encoding"))
	}
	w = get(t, h, "GET", "/repo/contents/ab/"+sha+"/man-"+sha+".json", map[string]string{"Range": "bytes=1-9"})
	if w.Code != 206 || w.Body.String() != `"variant"` || w.Header().Get("Cache-Control") != CacheImmutable {
		t.Errorf("range: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
	}
	// A variant's address is the app's: it gets the shell. An old series
	// address goes where it is now.
	for _, p := range []string{"/pirate/1/ls/", "/man/1/ls/", "/pirate/", "/pirate/section/1/", "/girl/section/1/"} {
		if w := get(t, h, "GET", p, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "data-shell") {
			t.Errorf("%s: %d %.40q", p, w.Code, w.Body.String())
		}
	}
	if w := get(t, h, "GET", "/witch/1/ls/", nil); w.Code != 404 {
		t.Errorf("a variant there isn't: %d", w.Code)
	}
	for from, to := range map[string]string{"/rfc/9110/": "/girl/rfc/9110/", "/rfc/": "/section/rfc/", "/rfc/page/2/": "/section/rfc/page/2/"} {
		if w := get(t, h, "GET", from, nil); w.Code != 301 || w.Header().Get("Location") != to {
			t.Errorf("%s: %d to %q, want %q", from, w.Code, w.Header().Get("Location"), to)
		}
	}
	if w := get(t, h, "GET", "/nope", nil); w.Code != 404 || !strings.Contains(w.Body.String(), "not here") {
		t.Errorf("404: %d %q", w.Code, w.Body.String())
	}
	if w := get(t, h, "HEAD", "/repo/manifest.json", nil); w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != CachePointer {
		t.Errorf("head: %d %d", w.Code, w.Body.Len())
	}
	if w := get(t, h, "POST", "/", nil); w.Code != 405 {
		t.Errorf("post: %d", w.Code)
	}
	if w := get(t, h, "GET", "/../../etc/passwd", nil); w.Code != 404 {
		t.Errorf("traversal: %d", w.Code)
	}

	// A new version: a reload serves it.
	put(t, site, "repo/manifest.json", `{"format":1,"pages":1}`)
	if _, err := build.Write(context.Background(), site, dir, build.Options{}); err != nil {
		t.Fatal(err)
	}
	if changed, err := a.Reload(); !changed || err != nil {
		t.Fatalf("reload: %v %v", changed, err)
	}
	if w := get(t, h, "GET", "/repo/manifest.json", nil); w.Body.String() != `{"format":1,"pages":1}` {
		t.Errorf("after reload: %q", w.Body.String())
	}

	if !strings.Contains(log.String(), `"path":"/girl/1/ls/"`) || !strings.Contains(log.String(), `"status":304`) ||
		!strings.Contains(log.String(), `"forwarded_for":"203.0.113.7, 10.0.0.2"`) || strings.Count(log.String(), `"forwarded_for"`) != 1 {
		t.Errorf("access log: %s", log.String())
	}
	adm := Admin(src, m, a)
	if w := get(t, adm, "GET", "/readyz", nil); w.Code != 200 {
		t.Errorf("readyz: %d", w.Code)
	}
	w = get(t, adm, "GET", "/metrics", nil)
	for _, want := range []string{`girlpages_http_requests_total{code="200"}`, `girlpages_http_requests_total{code="404"} 3`,
		"girlpages_archive_reloads_total 1", "girlpages_archive_files 6", `girlpages_http_request_duration_seconds_bucket{le="+Inf"}`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("metrics lack %q:\n%s", want, w.Body.String())
		}
	}
}

func TestNotReady(t *testing.T) {
	src := DirSource(t.TempDir())
	if w := get(t, Handler(src, Options{}), "GET", "/", nil); w.Code != 503 {
		t.Errorf("site: %d", w.Code)
	}
	if w := get(t, Admin(src, NewMetrics(), nil), "GET", "/readyz", nil); w.Code != 503 {
		t.Errorf("readyz: %d", w.Code)
	}
	if w := get(t, Admin(src, NewMetrics(), nil), "GET", "/healthz", nil); w.Code != 200 {
		t.Errorf("healthz: %d", w.Code)
	}
}

type sized map[string]int64

func (s sized) Has(e string) bool { _, ok := s[e]; return ok && e != "" }
func (s sized) Size(e string) int64 {
	if n, ok := s[e]; ok {
		return n
	}
	return -1
}
func (s sized) Open(string) (io.ReadSeeker, error) { return nil, nil }
func (s sized) ETag(string) string                 { return "" }
func (s sized) ModTime() (t time.Time)             { return }
func (s sized) Close() error                       { return nil }

func TestPickSmallest(t *testing.T) {
	f := sized{"": 1000, "zstd": 300, "br": 250, "gzip": 400}
	for accept, want := range map[string]string{
		"gzip, deflate, br, zstd": "br",
		"gzip, zstd":              "zstd",
		"gzip":                    "gzip",
		"br;q=0, zstd":            "zstd",
		"*":                       "br",
		"":                        "",
		"identity":                "",
	} {
		if got := pick(f, accept); got != want {
			t.Errorf("%q: %q, want %q", accept, got, want)
		}
	}
	if got := pick(sized{"": 100, "br": 100}, "br"); got != "" {
		t.Errorf("a copy no smaller: %q", got)
	}
	if got := pick(sized{"": 1000, "zstd": 300, "br": 300}, "br, zstd"); got != "zstd" {
		t.Errorf("a tie: %q", got)
	}
}
