// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"context"
	"encoding/json"
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
	srv := llmtest.NewBatchServer()
	defer srv.Close()
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
		Client:      &BatchClient{URL: srv.BatchURL(), APIKey: "sk-test", Model: "anthropic/claude-haiku-5.5"},
		Dir:         dir,
		Options:     Options{ChunkSize: 200},
		Live:        live,
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
	if !(strings.Index(raw, `"endpoint"`) < strings.Index(raw, `"requests"`) && strings.Index(raw, `"model"`) < strings.Index(raw, `"requests"`)) {
		t.Errorf("endpoint and model must come before requests: %.200s", raw)
	}
	if strings.Count(raw, `"model"`) != 1 || strings.Contains(raw, `"stream"`) {
		t.Errorf("requests should inherit the batch's model: %.300s", raw)
	}
	if srv.Auth[0] != "Bearer sk-test" {
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
	if cs.Running != 0 || cs.Changed != 1 || cs.Pages != 4 || cs.Cost == 0 {
		t.Errorf("collect: %+v", cs)
	}
	if r := got["foo"]; r.Err != nil || !strings.Contains(r.Markup, "batched") || !strings.Contains(r.Markup, "--all") || r.Model != "anthropic/claude-haiku-5.5" {
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
	c := &BatchClient{URL: srv.BatchURL()}
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
