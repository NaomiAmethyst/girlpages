// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package bulk

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/llm/llmtest"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

type fake struct{ calls atomic.Int32 }

func (f *fake) Name() string  { return "fake" }
func (f *fake) Model() string { return "fake-1" }
func (f *fake) Complete(_ context.Context, _, user string) (string, error) {
	f.calls.Add(1)
	// Echo the options so the completeness check passes without a retry.
	var opts []string
	for _, w := range strings.Fields(user) {
		if strings.HasPrefix(w, "-") {
			opts = append(opts, w)
		}
	}
	return "# ✨ NAME ✨\n\nslay " + strings.Join(opts, " "), nil
}

func page(name, sec, desc string) string {
	return ".TH " + strings.ToUpper(name) + " " + sec + " 2026-01-01 test\n.SH NAME\n" + name + " \\- " + desc + "\n.SH OPTIONS\n.TP\n.B \\-v\nbe loud\n"
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRun(t *testing.T) {
	if _, err := exec.LookPath("man"); err != nil {
		t.Skip("man not installed")
	}
	mp := t.TempDir()
	write(t, filepath.Join(mp, "man1", "foo.1"), page("foo", "1", "does foo"))
	write(t, filepath.Join(mp, "man8", "baz.8"), page("baz", "8", "does baz"))
	if err := os.Symlink("foo.1", filepath.Join(mp, "man1", "foo-alias.1")); err != nil {
		t.Fatal(err)
	}
	dir := cache.Dir(t.TempDir())
	caches := cache.Set{dir}
	b := &fake{}
	o := Options{Jobs: 2, Quiet: true, Prune: true, Man: manpage.Options{ManPath: mp}}
	ctx := context.Background()

	st, err := Run(ctx, o, caches, b, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if st.Pages != 3 || st.Converted != 3 || b.calls.Load() != 2 {
		t.Fatalf("first run: %+v, %d calls", st, b.calls.Load())
	}
	markup, e, fresh := caches.Lookup(filepath.Join(mp, "man1", "foo-alias.1"))
	if !strings.Contains(markup, "slay") || !fresh || e.Title != "FOO(1)" || e.Model != "fake-1" {
		t.Fatalf("alias lookup: %q %+v %v", markup, e, fresh)
	}
	if _, err := os.Stat(dir.MarkerPath()); err != nil {
		t.Error("no marker after a full run")
	}

	// Nothing changed: no calls.
	st, err = Run(ctx, o, caches, b, io.Discard)
	if err != nil || st.Fresh != 3 || b.calls.Load() != 2 {
		t.Fatalf("second run: %+v, %v, %d calls", st, err, b.calls.Load())
	}

	// Touched but same content: relinked without a call.
	later := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(mp, "man1", "foo.1"), later, later)
	st, err = Run(ctx, o, caches, b, io.Discard)
	if err != nil || st.Linked != 2 || b.calls.Load() != 2 {
		t.Fatalf("touch run: %+v, %v, %d calls", st, err, b.calls.Load())
	}

	// Real change plus a removal, in update mode: after a full run, update
	// mode looks at everything.
	write(t, filepath.Join(mp, "man8", "baz.8"), page("baz", "8", "does baz better"))
	write(t, filepath.Join(mp, "man1", "new.1"), page("new", "1", "is new"))
	os.Remove(filepath.Join(mp, "man1", "foo-alias.1"))
	o.Update = true
	st, err = Run(ctx, o, caches, b, io.Discard)
	if err != nil || st.Converted != 2 || b.calls.Load() != 4 || st.Pruned != 1 || st.Removed != 1 {
		t.Fatalf("update run: %+v, %v, %d calls", st, err, b.calls.Load())
	}
}

func TestUpdateWithoutFullRun(t *testing.T) {
	if _, err := exec.LookPath("man"); err != nil {
		t.Skip("man not installed")
	}
	mp := t.TempDir()
	write(t, filepath.Join(mp, "man1", "a.1"), page("a", "1", "is a"))
	write(t, filepath.Join(mp, "man1", "b.1"), page("b", "1", "is b"))
	dir := cache.Dir(t.TempDir())
	b := &fake{}
	ctx := context.Background()
	mo := manpage.Options{ManPath: mp}

	// A filtered bulk run converts a.1 but doesn't count as complete.
	st, err := Run(ctx, Options{Limit: 1, Quiet: true, Man: mo}, cache.Set{dir}, b, io.Discard)
	if err != nil || st.Converted != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := os.Stat(dir.MarkerPath()); err == nil {
		t.Fatal("marker written after a limited run")
	}
	// Update only refreshes what's cached, so b.1 stays unconverted.
	write(t, filepath.Join(mp, "man1", "a.1"), page("a", "1", "is a, but better"))
	st, err = Run(ctx, Options{Update: true, Quiet: true, Man: mo}, cache.Set{dir}, b, io.Discard)
	if err != nil || st.Pages != 1 || st.Converted != 1 || b.calls.Load() != 2 {
		t.Fatalf("%+v %v %d", st, err, b.calls.Load())
	}
}

func TestDryRun(t *testing.T) {
	if _, err := exec.LookPath("man"); err != nil {
		t.Skip("man not installed")
	}
	mp := t.TempDir()
	write(t, filepath.Join(mp, "man1", "a.1"), page("a", "1", "is a"))
	dir := cache.Dir(t.TempDir())
	var log strings.Builder
	st, err := Run(context.Background(), Options{DryRun: true, Man: manpage.Options{ManPath: mp}}, cache.Set{dir}, nil, &log)
	if err != nil || st.Converted != 0 || !strings.Contains(log.String(), "would girlify a(1)") {
		t.Fatalf("%+v %v %q", st, err, log.String())
	}
	if _, err := os.Stat(dir.MarkerPath()); err == nil {
		t.Error("dry run wrote the marker")
	}
}

func TestLock(t *testing.T) {
	dir := cache.Dir(t.TempDir())
	unlock, err := lock(dir.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock(dir.LockPath()); err != ErrLocked {
		t.Errorf("second lock: %v", err)
	}
	unlock()
	if u, err := lock(dir.LockPath()); err != nil {
		t.Errorf("relock: %v", err)
	} else {
		u()
	}
}

func TestFetchFromRepo(t *testing.T) {
	if _, err := exec.LookPath("man"); err != nil {
		t.Skip("man not installed")
	}
	mp := t.TempDir()
	write(t, filepath.Join(mp, "man1", "foo.1"), page("foo", "1", "does foo"))
	write(t, filepath.Join(mp, "man1", "bar.1"), page("bar", "1", "does bar"))
	dir := cache.Dir(t.TempDir())
	var asked atomic.Int32
	o := Options{Jobs: 2, Quiet: true, Man: manpage.Options{ManPath: mp},
		Fetch: func(_ context.Context, p manpage.Page) (string, *repo.Generator, error) {
			asked.Add(1)
			if p.Name == "foo" {
				return "# NAME\n\nfoo from the repo 💅\n", &repo.Generator{Model: "repo-model", PromptVersion: 1}, nil
			}
			return "", nil, repo.ErrNotFound
		}}
	// No model at all: what's in the repo still gets cached.
	st, err := Run(context.Background(), o, cache.Set{dir}, nil, io.Discard)
	if err == nil || st.Fetched != 1 || st.Failed != 1 || asked.Load() != 2 {
		t.Fatalf("no model: %+v, %v, asked %d", st, err, asked.Load())
	}
	markup, e, _ := cache.Set{dir}.Lookup(filepath.Join(mp, "man1", "foo.1"))
	if !strings.Contains(markup, "from the repo") || e.Backend != "repo" || e.Model != "repo-model" {
		t.Fatalf("foo: %q %+v", markup, e)
	}
	// With a model, the rest gets converted.
	b := &fake{}
	st, err = Run(context.Background(), o, cache.Set{dir}, b, io.Discard)
	if err != nil || st.Fresh != 1 || st.Converted != 1 || b.calls.Load() != 1 {
		t.Fatalf("with a model: %+v, %v", st, err)
	}
}

func TestBatch(t *testing.T) {
	if _, err := exec.LookPath("man"); err != nil {
		t.Skip("man not installed")
	}
	srv := llmtest.NewBatchServer()
	defer srv.Close()
	srv.Reply = llmtest.EchoOptions("batched")
	mp := t.TempDir()
	write(t, filepath.Join(mp, "man1", "foo.1"), page("foo", "1", "does foo"))
	write(t, filepath.Join(mp, "man8", "baz.8"), page("baz", "8", "does baz"))
	if err := os.Symlink("foo.1", filepath.Join(mp, "man1", "foo-alias.1")); err != nil {
		t.Fatal(err)
	}
	dir := cache.Dir(t.TempDir())
	caches := cache.Set{dir}
	o := Options{Jobs: 2, Quiet: true, Man: manpage.Options{ManPath: mp}, NoWait: true,
		Batch: &llm.Batcher{Client: &llm.OpenRouterBatch{URL: srv.BatchURL(), Model: "cheap-model"},
			Dir: BatchDir(dir), Poll: 10 * time.Millisecond, Options: llm.Options{ChunkSize: 12000}}}
	ctx := context.Background()

	st, err := Run(ctx, o, caches, nil, io.Discard)
	if err != nil || st.Batched != 3 || st.Converted != 0 || srv.Batches() != 1 || srv.Requests() != 2 {
		t.Fatalf("first run: %+v, %v, %d batches", st, err, srv.Batches())
	}
	// Still running: nothing is sent twice.
	st, err = Run(ctx, o, caches, nil, io.Discard)
	if err != nil || st.Pending != 3 || st.Batched != 0 || srv.Batches() != 1 {
		t.Fatalf("second run: %+v, %v, %d batches", st, err, srv.Batches())
	}
	// Done now: collected into the cache.
	st, err = Run(ctx, o, caches, nil, io.Discard)
	if err != nil || st.Converted != 3 || st.Pending != 0 || srv.Batches() != 1 {
		t.Fatalf("third run: %+v, %v", st, err)
	}
	markup, e, fresh := caches.Lookup(filepath.Join(mp, "man1", "foo-alias.1"))
	if !strings.Contains(markup, "batched") || !fresh || e.Backend != "openai" || e.Model != "cheap-model" || e.Title != "FOO(1)" {
		t.Fatalf("alias: %q %+v", markup, e)
	}
	// A new page, waiting this time.
	write(t, filepath.Join(mp, "man1", "new.1"), page("new", "1", "is new"))
	o.NoWait = false
	st, err = Run(ctx, o, caches, nil, io.Discard)
	if err != nil || st.Converted != 1 || st.Fresh != 3 || srv.Batches() != 2 {
		t.Fatalf("waiting run: %+v, %v", st, err)
	}
	if files, _ := filepath.Glob(filepath.Join(BatchDir(dir), "*")); len(files) != 0 {
		t.Errorf("batches left behind: %v", files)
	}
}

func TestFetchOnly(t *testing.T) {
	if _, err := exec.LookPath("man"); err != nil {
		t.Skip("man not installed")
	}
	mp := t.TempDir()
	write(t, filepath.Join(mp, "man1", "foo.1"), page("foo", "1", "does foo"))
	write(t, filepath.Join(mp, "man1", "bar.1"), page("bar", "1", "does bar"))
	dir := cache.Dir(t.TempDir())
	o := Options{Jobs: 2, Quiet: true, Man: manpage.Options{ManPath: mp}, FetchOnly: true,
		Fetch: func(_ context.Context, p manpage.Page) (string, *repo.Generator, error) {
			if p.Name == "foo" {
				return "# NAME\n\nfoo from the repo 💅\n", &repo.Generator{Model: "m", PromptVersion: 1}, nil
			}
			return "", nil, repo.ErrNotFound
		}}
	// Missing pages aren't failures, and it isn't a full run.
	st, err := Run(context.Background(), o, cache.Set{dir}, nil, io.Discard)
	if err != nil || st.Fetched != 1 || st.NotInRepo != 1 || st.Failed != 0 {
		t.Fatalf("import: %+v, %v", st, err)
	}
	if _, err := os.Stat(dir.MarkerPath()); err == nil {
		t.Error("an import shouldn't count as a full bulk run")
	}
}

func TestReprompt(t *testing.T) {
	if _, err := exec.LookPath("man"); err != nil {
		t.Skip("man not installed")
	}
	mp := t.TempDir()
	write(t, filepath.Join(mp, "man1", "foo.1"), page("foo", "1", "does foo"))
	write(t, filepath.Join(mp, "man1", "bar.1"), page("bar", "1", "does bar"))
	dir := cache.Dir(t.TempDir())
	caches := cache.Set{dir}
	ctx := context.Background()
	b := &fake{}
	o := Options{Jobs: 2, Quiet: true, Man: manpage.Options{ManPath: mp}}
	if _, err := Run(ctx, o, caches, b, io.Discard); err != nil {
		t.Fatal(err)
	}
	// Pretend foo was made with an older prompt.
	foo := filepath.Join(mp, "man1", "foo.1")
	e, _ := dir.Entry(foo)
	e.PromptVersion = llm.PromptVersion - 1
	dir.PutEntry(e)
	// An alias of foo turns up; an ordinary run links it to the old
	// conversion and records its maker faithfully.
	if err := os.Symlink("foo.1", filepath.Join(mp, "man1", "foo-alias.1")); err != nil {
		t.Fatal(err)
	}
	st, err := Run(ctx, o, caches, b, io.Discard)
	if err != nil || st.Linked != 1 || st.Converted != 0 {
		t.Fatalf("ordinary run: %+v, %v", st, err)
	}
	if a, _ := dir.Entry(filepath.Join(mp, "man1", "foo-alias.1")); a.PromptVersion != llm.PromptVersion-1 || a.Model != "fake-1" {
		t.Errorf("alias entry: %+v", a)
	}
	// A repo that only has the old conversion doesn't count when
	// reprompting.
	calls := b.calls.Load()
	o.Reprompt = true
	o.Fetch = func(context.Context, manpage.Page) (string, *repo.Generator, error) {
		return "# NAME\n\nold\n", &repo.Generator{PromptVersion: llm.PromptVersion - 1}, nil
	}
	st, err = Run(ctx, o, caches, b, io.Discard)
	if err != nil || st.Converted != 2 || st.Fresh != 1 || st.Fetched != 0 || b.calls.Load() != calls+1 {
		t.Fatalf("reprompt: %+v, %v, %d calls", st, err, b.calls.Load()-calls)
	}
	for _, p := range []string{"foo.1", "foo-alias.1"} {
		if e, _ := dir.Entry(filepath.Join(mp, "man1", p)); e.PromptVersion != llm.PromptVersion {
			t.Errorf("%s: prompt %d", p, e.PromptVersion)
		}
	}
}
