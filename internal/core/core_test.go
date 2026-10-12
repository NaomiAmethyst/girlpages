// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/config"
	"github.com/NaomiAmethyst/girlpages/internal/ingest"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

type fake struct{ reply string }

func (f *fake) Name() string  { return "fake" }
func (f *fake) Model() string { return "fake-" + f.reply }
func (f *fake) Complete(_ context.Context, _, user string) (string, error) {
	var opts []string
	for _, w := range strings.Fields(user) {
		if strings.HasPrefix(w, "-") {
			opts = append(opts, w)
		}
	}
	return "# NAME\n\n" + f.reply + " " + strings.Join(opts, " "), nil
}

func roff(name, desc string) string {
	return ".TH " + strings.ToUpper(name) + " 1 2026-01-01 test\n.SH NAME\n" + name + " \\- " + desc +
		"\n.SH OPTIONS\n.TP\n.B \\-v\nbe loud\n"
}

func TestRepo(t *testing.T) {
	if _, err := exec.LookPath("man"); err != nil {
		t.Skip("man not installed")
	}
	ctx := context.Background()
	mp := filepath.Join(t.TempDir(), "man")
	for name, desc := range map[string]string{"foo": "does foo", "bar": "does bar"} {
		p := filepath.Join(mp, "man1", name+".1")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(roff(name, desc)), 0o644)
	}

	// A published repo with foo girlified (by "the repo's model") but not bar.
	site := t.TempDir()
	s, err := repo.Open(filepath.Join(site, "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.Paths(ctx, s, []string{mp}, ingest.Options{Quiet: true, Log: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.Girlify(ctx, s, ingest.GirlifyOptions{Backend: &fake{"from-repo"}, Names: []string{"foo"},
		Quiet: true, Log: io.Discard}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(site)))
	defer srv.Close()

	cfg := config.Defaults()
	newGirl := func() *Girl {
		return &Girl{Cfg: &cfg, Caches: cache.Set{cache.Dir(t.TempDir())}, Man: manpage.Options{ManPath: mp},
			Repo:       repo.NewClient(srv.URL+"/repo", 5*time.Second),
			NewBackend: func(*config.Config) (llm.Backend, error) { return &fake{"from-model"}, nil }}
	}
	foo, _ := manpage.NewPage(filepath.Join(mp, "man1", "foo.1"))
	bar, _ := manpage.NewPage(filepath.Join(mp, "man1", "bar.1"))

	g := newGirl()
	res, err := g.Get(ctx, foo, GetOptions{CachedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Markup, "from-repo") || res.Entry.Backend != BackendRepo || res.Entry.Model != "fake-from-repo" ||
		res.Entry.PromptVersion != llm.PromptVersion {
		t.Fatalf("from the repo: %q %+v", res.Markup, res.Entry)
	}
	// It's cached now.
	if r, ok := g.Cached(foo); !ok || r.Markup != res.Markup || r.Stale {
		t.Errorf("not cached after fetching: %+v %v", r, ok)
	}
	// Not in the repo, and not allowed to convert.
	if _, err := g.Get(ctx, bar, GetOptions{CachedOnly: true}); !errors.Is(err, ErrNotCached) {
		t.Errorf("bar: %v", err)
	}
	// Allowed to convert: the model does it.
	if res, err := g.Get(ctx, bar, GetOptions{}); err != nil || !strings.Contains(res.Markup, "from-model") {
		t.Errorf("bar converted: %q, %v", res.Markup, err)
	}
	// No cache asks the model, though foo is cached, and leaves the cache be.
	if res, err := g.Get(ctx, foo, GetOptions{NoCache: true}); err != nil || !strings.Contains(res.Markup, "from-model") {
		t.Errorf("no cache: %q, %v", res.Markup, err)
	}
	if r, ok := g.Cached(foo); !ok || !strings.Contains(r.Markup, "from-repo") {
		t.Errorf("no cache changed the cache: %q %v", r.Markup, ok)
	}
	// Nor does it look in the repo, or save anything for an uncached page.
	fresh := newGirl()
	if res, err := fresh.Get(ctx, foo, GetOptions{NoCache: true}); err != nil || !strings.Contains(res.Markup, "from-model") {
		t.Errorf("no cache, uncached: %q, %v", res.Markup, err)
	}
	if r, ok := fresh.Cached(foo); ok {
		t.Errorf("no cache saved %q", r.Markup)
	}
	if _, err := fresh.Get(ctx, foo, GetOptions{CachedOnly: true}); err != nil {
		t.Errorf("repo after no cache: %v", err)
	}
	// Refreshing asks the model, not the repo.
	if res, err := g.Get(ctx, foo, GetOptions{Refresh: true}); err != nil || !strings.Contains(res.Markup, "from-model") {
		t.Errorf("refresh: %q, %v", res.Markup, err)
	}
	// Custom instructions mean the repo's pages aren't wanted.
	cfg.ExtraInstructions = "talk like a pirate"
	if _, err := newGirl().Get(ctx, foo, GetOptions{CachedOnly: true}); !errors.Is(err, ErrNotCached) {
		t.Errorf("with extra instructions: %v", err)
	}
	cfg.ExtraInstructions = ""
	// An unreachable repo just means converting as usual.
	g = newGirl()
	g.Repo = repo.NewClient("http://127.0.0.1:1/repo", time.Second)
	if res, err := g.Get(ctx, foo, GetOptions{}); err != nil || !strings.Contains(res.Markup, "from-model") {
		t.Errorf("unreachable repo: %q, %v", res.Markup, err)
	}
}

// BulkFetch asks only for pages the repository's index lists.
func TestBulkFetch(t *testing.T) {
	if _, err := exec.LookPath("man"); err != nil {
		t.Skip("man not installed")
	}
	ctx := context.Background()
	mp := filepath.Join(t.TempDir(), "man")
	for name, desc := range map[string]string{"foo": "does foo", "bar": "does bar"} {
		p := filepath.Join(mp, "man1", name+".1")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(roff(name, desc)), 0o644)
	}
	site := t.TempDir()
	s, _ := repo.Open(filepath.Join(site, "repo"))
	ingest.Paths(ctx, s, []string{mp}, ingest.Options{Quiet: true, Log: io.Discard})
	ingest.Girlify(ctx, s, ingest.GirlifyOptions{Backend: &fake{"from-repo"}, Names: []string{"foo"}, Quiet: true, Log: io.Discard})
	pages, _ := s.AllPages()
	s.Publish(pages)
	var contents atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			contents.Add(1)
		}
		http.FileServer(http.Dir(site)).ServeHTTP(w, r)
	}))
	defer srv.Close()
	fetch := BulkFetch(repo.NewClient(srv.URL+"/repo", 5*time.Second))
	foo, _ := manpage.NewPage(filepath.Join(mp, "man1", "foo.1"))
	bar, _ := manpage.NewPage(filepath.Join(mp, "man1", "bar.1"))
	if markup, _, err := fetch(ctx, foo); err != nil || !strings.Contains(markup, "from-repo") {
		t.Errorf("foo: %q, %v", markup, err)
	}
	if _, _, err := fetch(ctx, bar); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("bar: %v", err)
	}
	if n := contents.Load(); n != 2 {
		t.Errorf("%d content requests, want 2 (foo's variants.json and its girl variant)", n)
	}
}
