// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/llm/llmtest"
)

type liveFake struct{ calls atomic.Int32 }

func (f *liveFake) Name() string  { return "openai" }
func (f *liveFake) Model() string { return "live" }
func (f *liveFake) Complete(_ context.Context, _, user string) (string, error) {
	f.calls.Add(1)
	r, _ := llmtest.EchoOptions("live")(user)
	return r, nil
}

func page(name string, opts ...string) string {
	s := strings.ToUpper(name) + "(1)   General Commands   " + strings.ToUpper(name) + "(1)\n\nNAME\n       " + name + " - does things\n\nOPTIONS\n"
	for _, o := range opts {
		s += "       " + o + "\n              an option\n\n"
	}
	return s
}

func TestBatcher(t *testing.T) {
	t.Run("openrouter", func(t *testing.T) {
		srv := llmtest.NewBatchServer()
		defer srv.Close()
		testBatcher(t, srv, &OpenRouterBatch{URL: srv.BatchURL(), APIKey: "sk-test", Model: "anthropic/claude-haiku-5.5"}, "Bearer sk-test")
	})
	t.Run("anthropic", func(t *testing.T) {
		srv := llmtest.NewBatchServer()
		defer srv.Close()
		testBatcher(t, srv, &AnthropicBatch{URL: srv.AnthropicURL(), APIKey: "sk-ant-test", Model: "claude-haiku-5-5"}, "sk-ant-test")
	})
}

func testBatcher(t *testing.T, srv *llmtest.BatchServer, api BatchAPI, auth string) {
	srv.Polls = 1
	// "bar" comes back without its options; "baz" fails outright.
	srv.Reply = func(user string) (string, string) {
		switch {
		case strings.Contains(user, "BAZ(1)"):
			return "", "provider exploded"
		case strings.Contains(user, "BAR(1)"):
			return "# NAME\n\nbar, but she forgot the flags", ""
		}
		return llmtest.EchoOptions("batched")(user)
	}
	live := &liveFake{}
	dir := t.TempDir()
	b := &Batcher{
		Client:      api,
		Dir:         dir,
		Options:     Options{ChunkSize: 200},
		Live:        live,
		Repair:      RepairLive, // (what this test is of)
		Jobs:        2,
		Poll:        10 * time.Millisecond,
		MaxRequests: 3, // so the jobs go in several batches
	}
	texts := map[string]string{
		"foo":  page("foo", "-a", "--all"),
		"bar":  page("bar", "-b"),
		"baz":  page("baz", "-z"),
		"long": page("long", "-1", "-2", "-3", "-4", "-5", "-6", "-7", "-8"),
		"gone": page("gone", "-g"),
	}
	var jobs []BatchJob
	for _, k := range []string{"foo", "bar", "baz", "long", "gone"} {
		meta, _ := json.Marshal(map[string]string{"name": k})
		jobs = append(jobs, BatchJob{Key: k, Title: strings.ToUpper(k) + "(1)", Text: texts[k], Meta: meta})
	}
	ctx := context.Background()
	sent, err := b.Submit(ctx, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if sent != srv.Requests() || srv.Batches() < 2 {
		t.Fatalf("sent %d requests in %d batches (server saw %d)", sent, srv.Batches(), srv.Requests())
	}
	if parts := len(Plan("LONG(1)", texts["long"], b.Options)); parts < 2 {
		t.Fatalf("the long page should be in parts, got %d", parts)
	}
	raw := srv.Submitted[0]
	if api.Kind() == "openrouter" {
		if !(strings.Index(raw, `"endpoint"`) < strings.Index(raw, `"requests"`) && strings.Index(raw, `"model"`) < strings.Index(raw, `"requests"`)) {
			t.Errorf("endpoint and model must come before requests: %.200s", raw)
		}
		if strings.Count(raw, `"model"`) != 1 || strings.Contains(raw, `"stream"`) {
			t.Errorf("requests should inherit the batch's model: %.300s", raw)
		}
	} else if !strings.Contains(raw, `"max_tokens":32000`) || !strings.Contains(raw, `"system":`) {
		t.Errorf("anthropic requests need max_tokens and a system prompt: %.300s", raw)
	}
	if srv.Auth[0] != auth {
		t.Errorf("auth = %q", srv.Auth[0])
	}
	pending, err := b.Pending()
	if err != nil || len(pending) != 5 {
		t.Fatalf("pending = %v, %v", pending, err)
	}

	textOf := func(j SavedJob) (string, bool) {
		if j.Key == "gone" {
			return "", false
		}
		return texts[j.Key], true
	}
	var mu sync.Mutex
	got := map[string]Result{}
	done := func(r Result) {
		mu.Lock()
		got[r.Job.Key] = r
		mu.Unlock()
	}
	// Not waiting: everything is still running.
	cs, err := b.Collect(ctx, false, textOf, done)
	if err != nil || cs.Running != srv.Batches() || len(got) != 0 {
		t.Fatalf("no wait: %+v, %v, %v", cs, got, err)
	}
	cs, err = b.Collect(ctx, true, textOf, done)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Running != 0 || cs.Changed != 1 || cs.Pages != 4 || (cs.Cost == 0) != (api.Kind() == "anthropic") {
		t.Errorf("collect: %+v", cs)
	}
	if r := got["foo"]; r.Err != nil || !strings.Contains(r.Markup, "batched") || !strings.Contains(r.Markup, "--all") ||
		strings.Contains(r.Markup, "hmm") || r.Model != api.ModelName() {
		t.Errorf("foo: %+v", r)
	}
	if r := got["long"]; r.Err != nil || strings.Count(r.Markup, "batched") < 2 {
		t.Errorf("long: %+v", r)
	}
	// bar lost its options, so it was retried live; baz failed, so it was
	// converted live.
	if r := got["bar"]; r.Err != nil || !strings.Contains(r.Markup, "live") || !strings.Contains(r.Markup, "-b") {
		t.Errorf("bar: %+v", r)
	}
	if r := got["baz"]; r.Err != nil || !strings.Contains(r.Markup, "live") {
		t.Errorf("baz: %+v", r)
	}
	if live.calls.Load() != 2 {
		t.Errorf("%d live calls, want 2", live.calls.Load())
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*")); len(files) != 0 {
		t.Errorf("collected batches should be forgotten: %v", files)
	}

	// Without a live backend, failures come back as errors.
	b.Live = nil
	got = map[string]Result{}
	if _, err := b.Submit(ctx, jobs[2:3]); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Collect(ctx, true, textOf, done); err != nil {
		t.Fatal(err)
	}
	if r := got["baz"]; r.Err == nil || !strings.Contains(r.Err.Error(), "provider exploded") {
		t.Errorf("baz without live: %+v", r)
	}
}

func TestBatchErrors(t *testing.T) {
	srv := llmtest.NewBatchServer()
	defer srv.Close()
	c := &OpenRouterBatch{URL: srv.BatchURL()}
	if _, err := c.Submit(context.Background(), nil); err == nil {
		t.Error("no model should be an error")
	}
	c.Model = "m"
	if _, err := c.Get(context.Background(), "batch_nope"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing batch: %v", err)
	}
	// A batch that can't be saved after submitting is reported loudly.
	dir := filepath.Join(t.TempDir(), "file")
	os.WriteFile(dir, nil, 0o644)
	b := &Batcher{Client: c, Dir: dir}
	if _, err := b.Submit(context.Background(), []BatchJob{{Key: "k", Title: "X(1)", Text: "x\n"}}); err == nil {
		t.Error("expected an error")
	}
}

// Batches saved by one API are left alone by another.
func TestBatchOtherAPI(t *testing.T) {
	srv := llmtest.NewBatchServer()
	defer srv.Close()
	srv.Reply = llmtest.EchoOptions("x")
	dir := t.TempDir()
	or := &Batcher{Client: &OpenRouterBatch{URL: srv.BatchURL(), Model: "m"}, Dir: dir, Poll: time.Millisecond}
	if _, err := or.Submit(context.Background(), []BatchJob{{Key: "k", Title: "X(1)", Text: page("x", "-x")}}); err != nil {
		t.Fatal(err)
	}
	var logged []string
	an := &Batcher{Client: &AnthropicBatch{URL: srv.AnthropicURL(), Model: "m"}, Dir: dir, Poll: time.Millisecond,
		Log: func(f string, a ...any) { logged = append(logged, f) }}
	if p, _ := an.Pending(); len(p) != 0 {
		t.Errorf("anthropic sees openrouter's batches: %v", p)
	}
	cs, err := an.Collect(context.Background(), true, func(SavedJob) (string, bool) { return "", false }, func(Result) {})
	if err != nil || cs.Finished != 0 || len(logged) == 0 {
		t.Errorf("collect: %+v, %v, %q", cs, err, logged)
	}
	if p, _ := or.Pending(); len(p) != 1 {
		t.Error("openrouter's batch should still be there")
	}
}

// TestBatchRepair: a reply that dropped options is asked again: by
// default in the next batch (the page kept as it is meanwhile, and replaced
// by the better reply), or live, which is said, or not at all.
func TestBatchRepair(t *testing.T) {
	for _, policy := range []string{RepairBatch, RepairLive, RepairNone} {
		srv := llmtest.NewBatchServer()
		srv.Reply = func(user string) (string, string) {
			if strings.Contains(user, "a previous attempt left out") {
				return llmtest.EchoOptions("asked again")(user)
			}
			return "# NAME\n\nfoo, but she forgot the flags", ""
		}
		live := &liveFake{}
		var logs strings.Builder
		b := &Batcher{Client: &OpenRouterBatch{URL: srv.BatchURL(), Model: "m"}, Dir: t.TempDir(), Poll: time.Millisecond,
			Live: live, Repair: policy, Log: func(f string, a ...any) { fmt.Fprintf(&logs, f, a...) }}
		ctx := context.Background()
		text := page("foo", "-a", "--all")
		if _, err := b.Submit(ctx, []BatchJob{{Key: "foo", Title: "FOO(1)", Text: text}}); err != nil {
			t.Fatal(err)
		}
		var got []Result
		collect := func() {
			t.Helper()
			if _, err := b.Collect(ctx, true, func(SavedJob) (string, bool) { return text, true }, func(r Result) { got = append(got, r) }); err != nil {
				t.Fatalf("%s: collect: %v", policy, err)
			}
		}
		collect()
		if len(got) != 1 {
			t.Fatalf("%s: %+v", policy, got)
		}
		switch policy {
		case RepairBatch:
			if live.calls.Load() != 0 || !strings.Contains(got[0].Markup, "forgot the flags") || !b.HasRedos() {
				t.Fatalf("batch: %d live calls, %q, redos %v", live.calls.Load(), got[0].Markup, b.HasRedos())
			}
			if keys, _ := b.Pending(); !keys["foo"] {
				t.Error("a page with parts to ask again isn't pending")
			}
			// The next submission sends it, though there's nothing new.
			if n, err := b.Submit(ctx, nil); err != nil || n != 1 || b.HasRedos() {
				t.Fatalf("batch: resubmitted %d, %v", n, err)
			}
			collect()
			if len(got) != 2 || !strings.Contains(got[1].Markup, "asked again") || live.calls.Load() != 0 {
				t.Errorf("batch: the better reply didn't replace it: %+v", got)
			}
		case RepairLive:
			if live.calls.Load() != 1 || !strings.Contains(logs.String(), "🩹 FOO(1): its reply dropped") {
				t.Errorf("live: %d live calls; logs %q", live.calls.Load(), logs.String())
			}
		case RepairNone:
			if live.calls.Load() != 0 || !strings.Contains(got[0].Markup, "forgot the flags") || b.HasRedos() {
				t.Errorf("none: %d live calls, %q", live.calls.Load(), got[0].Markup)
			}
		}
		srv.Close()
	}
}

// TestBatchRedoFailedPart: a part that failed is asked again in the next
// batch, its page held till then (not sent whole), and done once it has it.
func TestBatchRedoFailedPart(t *testing.T) {
	srv := llmtest.NewBatchServer()
	failed := false
	srv.Reply = func(user string) (string, string) {
		if strings.Contains(user, "part 2") && !failed {
			failed = true
			return "", "provider exploded"
		}
		return llmtest.EchoOptions("ok")(user)
	}
	defer srv.Close()
	b := &Batcher{Client: &OpenRouterBatch{URL: srv.BatchURL(), Model: "m"}, Dir: t.TempDir(), Poll: time.Millisecond,
		Options: Options{ChunkSize: 200}, Live: &liveFake{}}
	ctx := context.Background()
	text := page("long", "-1", "-2", "-3", "-4", "-5", "-6", "-7", "-8")
	if n := len(Plan("LONG(1)", text, b.Options)); n < 2 {
		t.Fatalf("want a page in parts, got %d", n)
	}
	if _, err := b.Submit(ctx, []BatchJob{{Key: "long", Title: "LONG(1)", Text: text}}); err != nil {
		t.Fatal(err)
	}
	var got []Result
	collect := func() CollectStats {
		cs, err := b.Collect(ctx, true, func(SavedJob) (string, bool) { return text, true }, func(r Result) { got = append(got, r) })
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}
	if cs := collect(); cs.Held != 1 || len(got) != 0 {
		t.Fatalf("first: %+v, %+v", cs, got)
	}
	if keys, _ := b.Pending(); !keys["long"] {
		t.Error("a held page isn't pending, so it'd be sent whole")
	}
	if n, err := b.Submit(ctx, nil); err != nil || n != 1 {
		t.Fatalf("resubmitted %d, %v", n, err)
	}
	if collect(); len(got) != 1 || got[0].Err != nil || strings.Count(got[0].Markup, "ok") < 2 {
		t.Errorf("second: %+v", got)
	}
}
