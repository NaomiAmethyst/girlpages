// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/config"
	"github.com/NaomiAmethyst/girlpages/internal/core"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
)

type fake struct {
	calls atomic.Int32
	delay time.Duration
	fail  bool
}

func (f *fake) Name() string  { return "fake" }
func (f *fake) Model() string { return "fake-1" }
func (f *fake) Complete(ctx context.Context, _, user string) (string, error) {
	f.calls.Add(1)
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if f.fail {
		return "", io.ErrUnexpectedEOF
	}
	return "# 💖 NAME 💖\n\nslay <pink>queen</pink> `-v` see bar(8) and nope(1)", nil
}

func roff(name, sec, desc string) string {
	return ".TH " + strings.ToUpper(name) + " " + sec + " 2026-01-01 test\n.SH NAME\n" + name + " \\- " + desc +
		"\n.SH OPTIONS\n.TP\n.B \\-v\nbe loud\n"
}

func setup(t *testing.T, b *fake) (*Server, *httptest.Server, cache.Dir) {
	t.Helper()
	if _, err := exec.LookPath("man"); err != nil {
		t.Skip("man not installed")
	}
	mp := t.TempDir()
	for _, p := range []struct{ dir, name, sec, desc string }{
		{"man1", "foo", "1", "does foo things"},
		{"man1", "sparkle", "1", "adds glitter"},
		{"man8", "bar", "8", "administers bar"},
	} {
		path := filepath.Join(mp, p.dir, p.name+"."+p.sec)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(roff(p.name, p.sec, p.desc)), 0o644)
	}
	// Index the pages so man -k has descriptions.
	if err := exec.Command("mandb", "-q", mp).Run(); err != nil {
		t.Logf("mandb failed (%v); descriptions will be missing", err)
	}
	dir := cache.Dir(t.TempDir())
	cfg := config.Defaults()
	g := &core.Girl{Cfg: &cfg, Caches: cache.Set{dir}, Man: manpage.Options{ManPath: mp},
		NewBackend: func(*config.Config) (llm.Backend, error) { return b, nil }}
	s := &Server{Girl: g, Version: "test", WaitBeforeSpinner: 300 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ts := httptest.NewServer(s.Handler(ctx))
	t.Cleanup(ts.Close)
	return s, ts, dir
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func get(t *testing.T, u string) (int, string, string) {
	t.Helper()
	resp, err := noRedirect.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header.Get("Location")
}

func TestBrowse(t *testing.T) {
	_, ts, _ := setup(t, &fake{})

	code, body, _ := get(t, ts.URL+"/")
	if code != 200 || !strings.Contains(body, "User commands") || !strings.Contains(body, "System administration") ||
		!strings.Contains(body, "3 pages") {
		t.Errorf("home: %d\n%s", code, body)
	}
	code, body, _ = get(t, ts.URL+"/section/1")
	if code != 200 || !strings.Contains(body, `href="/man/1/foo"`) || !strings.Contains(body, "does foo things") ||
		strings.Contains(body, "/man/8/bar") {
		t.Errorf("section 1: %d\n%s", code, body)
	}
	if code, _, _ = get(t, ts.URL+"/section/5"); code != 404 {
		t.Errorf("empty section: %d", code)
	}
	code, body, _ = get(t, ts.URL+"/search?q=glitter")
	if code != 200 || !strings.Contains(body, "/man/1/sparkle") || strings.Contains(body, "/man/1/foo") {
		t.Errorf("search: %d\n%s", code, body)
	}
	if code, _, loc := get(t, ts.URL+"/search?q="+url.QueryEscape("bar(8)")); code != 303 || loc != "/man/8/bar" {
		t.Errorf("ref search: %d %s", code, loc)
	}
	if code, _, loc := get(t, ts.URL+"/man/bar"); code != 302 || loc != "/man/8/bar" {
		t.Errorf("bare name: %d %s", code, loc)
	}
	for _, bad := range []string{"/man/1/-H", "/man/1/..%2Ffoo", "/man/zz/foo", "/man/1/nope"} {
		if code, _, _ := get(t, ts.URL+bad); code != 404 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	code, body, _ = get(t, ts.URL+"/man/1/foo/original")
	if code != 200 || !strings.Contains(body, "does foo things") {
		t.Errorf("original: %d\n%s", code, body)
	}
}

func TestConvertOnDemand(t *testing.T) {
	b := &fake{}
	_, ts, dir := setup(t, b)
	code, body, _ := get(t, ts.URL+"/man/1/foo")
	if code != 200 || !strings.Contains(body, `<span class="c-pink">queen</span>`) ||
		!strings.Contains(body, `<a href="/man/8/bar">bar(8)</a>`) || !strings.Contains(body, "girlified by fake-1") ||
		strings.Contains(body, `href="/man/1/nope"`) {
		t.Fatalf("page: %d\n%s", code, body)
	}
	if b.calls.Load() != 1 {
		t.Errorf("%d calls", b.calls.Load())
	}
	// It's cached now.
	if e, _ := dir.Entry(mustPath(t, dir, "foo.1")); e == nil {
		t.Error("not cached")
	}
	get(t, ts.URL+"/man/1/foo")
	if b.calls.Load() != 1 {
		t.Errorf("cached page converted again: %d calls", b.calls.Load())
	}
	code, body, _ = get(t, ts.URL+"/section/1")
	if code != 200 || !strings.Contains(body, "1 girlified") {
		t.Errorf("section after convert: %s", body)
	}
}

func mustPath(t *testing.T, dir cache.Dir, base string) string {
	t.Helper()
	var found string
	dir.Entries(func(e *cache.Entry) error {
		if filepath.Base(e.Source) == base {
			found = e.Source
		}
		return nil
	})
	return found
}

func TestSlowConversionShowsWaiting(t *testing.T) {
	b := &fake{delay: 1500 * time.Millisecond}
	_, ts, _ := setup(t, b)
	code, body, _ := get(t, ts.URL+"/man/1/sparkle")
	if code != 202 || !strings.Contains(body, "girlifying sparkle(1)") || !strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatalf("first load: %d\n%s", code, body)
	}
	// A second visitor joins the same conversion.
	if code, _, _ := get(t, ts.URL+"/man/1/sparkle"); code != 202 {
		t.Errorf("second load: %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if code, _, _ = get(t, ts.URL+"/man/1/sparkle"); code == 200 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code != 200 || b.calls.Load() != 1 {
		t.Errorf("final: %d after %d calls", code, b.calls.Load())
	}
}

func TestFailureAndRetry(t *testing.T) {
	b := &fake{fail: true}
	_, ts, _ := setup(t, b)
	code, body, _ := get(t, ts.URL+"/man/1/foo")
	if code != 502 || !strings.Contains(body, "girlify foo(1)") || !strings.Contains(body, `action="/man/1/foo/convert"`) {
		t.Fatalf("failure: %d\n%s", code, body)
	}
	// Cross-site posts are refused.
	req, _ := http.NewRequest("POST", ts.URL+"/man/1/foo/convert", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("cross-site: %d", resp.StatusCode)
	}
	b.fail = false
	resp, err = noRedirect.PostForm(ts.URL+"/man/1/foo/convert", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("retry: %d", resp.StatusCode)
	}
	time.Sleep(200 * time.Millisecond)
	if code, body, _ := get(t, ts.URL+"/man/1/foo"); code != 200 || !strings.Contains(body, "queen") {
		t.Errorf("after retry: %d\n%s", code, body)
	}
}

func TestCachedOnly(t *testing.T) {
	b := &fake{}
	s, ts, _ := setup(t, b)
	s.CachedOnly = true
	code, body, _ := get(t, ts.URL+"/man/1/foo")
	if code != 404 || !strings.Contains(body, "not girlified yet") || b.calls.Load() != 0 {
		t.Errorf("cached-only: %d %d calls\n%s", code, b.calls.Load(), body)
	}
	resp, err := noRedirect.PostForm(ts.URL+"/man/1/foo/convert", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("convert while cached-only: %d", resp.StatusCode)
	}
}

func TestWriteDocument(t *testing.T) {
	var b strings.Builder
	err := WriteDocument(&b, []DocPage{{
		Page:   manpage.Page{Name: "ls", Section: "1"},
		Title:  "LS(1)",
		Markup: "# NAME\n\nsee ls(1) <mint>cute</mint>",
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"<title>LS(1) · girlpages", "<style>", "--c-mint", `<span class="c-mint">cute</span>`, "see ls(1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, unwanted := range []string{`href="/man/`, `action="/search"`, "/static/style.css", "re-girlify"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("standalone document has %q", unwanted)
		}
	}
}
