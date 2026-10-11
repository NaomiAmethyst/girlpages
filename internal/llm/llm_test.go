// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const sample = `LS(1)                     User Commands                    LS(1)

NAME
       ls - list directory contents

SYNOPSIS
       ls [OPTION]... [FILE]...

DESCRIPTION
       List information about the FILEs.

       -a, --all
              do not ignore entries starting with .

       --color[=WHEN]
              colorize the output

GNU coreutils 9.4               April 2024                       LS(1)
`

func TestHeadings(t *testing.T) {
	got := strings.Join(Headings(sample), ",")
	if got != "NAME,SYNOPSIS,DESCRIPTION" {
		t.Errorf("Headings = %q", got)
	}
}

func TestSplit(t *testing.T) {
	if c := Split(sample, 10000); len(c) != 1 || c[0] != sample {
		t.Fatalf("short page was split: %q", c)
	}
	chunks := Split(sample, 120)
	if strings.Join(chunks, "") != sample {
		t.Fatal("chunks don't reassemble the page")
	}
	if len(chunks) < 3 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	// Sections that fit stay whole.
	for _, c := range chunks {
		if strings.HasPrefix(c, "SYNOPSIS") && !strings.Contains(c, "ls [OPTION]") {
			t.Errorf("SYNOPSIS split: %q", c)
		}
	}
	// A single enormous paragraph still gets cut, at line boundaries.
	big := strings.Repeat("       word word word word\n", 200)
	for _, c := range Split(big, 500) {
		if len(c) > 500 {
			t.Errorf("chunk of %d bytes", len(c))
		}
		if !strings.HasSuffix(c, "\n") {
			t.Error("chunk cut mid-line")
		}
	}
}

func TestMissingOptions(t *testing.T) {
	out := "- **`-a`, `--all`** — slay\n- `--color[=WHEN]` — colours!!"
	if m := MissingOptions(sample, out); len(m) != 0 {
		t.Errorf("missing = %v", m)
	}
	if m := MissingOptions(sample, "- `-a` only"); strings.Join(m, " ") != "--all --color" {
		t.Errorf("missing = %v", m)
	}
	// Hyphenated words and dashes aren't options.
	if m := MissingOptions("a non-zero value - see x-ray", ""); len(m) != 0 {
		t.Errorf("missing = %v", m)
	}
}

func TestClean(t *testing.T) {
	cases := map[string]string{
		"  # hi  \n":                    "# hi",
		"```markdown\n# hi\n```":        "# hi",
		"<think>hmm</think>\n# hi":      "# hi",
		"# a\n```\ncode\n```\ntext":     "# a\n```\ncode\n```\ntext",
		"```\n# a\n```\nx\n```\ny\n```": "```\n# a\n```\nx\n```\ny\n```",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeBackend struct {
	calls atomic.Int32
	reply func(user string) string
}

func (f *fakeBackend) Name() string  { return "fake" }
func (f *fakeBackend) Model() string { return "" }
func (f *fakeBackend) Complete(_ context.Context, _, user string) (string, error) {
	f.calls.Add(1)
	return f.reply(user), nil
}

func TestConvertChunksInOrder(t *testing.T) {
	fb := &fakeBackend{reply: func(user string) string {
		var part int
		fmt.Sscanf(user[strings.Index(user, "this is part ")+len("this is part "):], "%d", &part)
		// Echo the options so the completeness check is happy.
		return fmt.Sprintf("part %d %s", part, strings.Join(optionsIn(user), " "))
	}}
	out, err := Convert(context.Background(), fb, "ls(1)", sample, Options{ChunkSize: 120, Parallel: 3})
	if err != nil {
		t.Fatal(err)
	}
	n := len(Split(sample, 120))
	for i := 1; i <= n; i++ {
		if !strings.Contains(out, fmt.Sprintf("part %d", i)) {
			t.Errorf("missing part %d in %q", i, out)
		}
		if i > 1 && strings.Index(out, fmt.Sprintf("part %d", i)) < strings.Index(out, fmt.Sprintf("part %d", i-1)) {
			t.Error("parts out of order")
		}
	}
	if int(fb.calls.Load()) != n {
		t.Errorf("%d calls for %d chunks", fb.calls.Load(), n)
	}
}

func optionsIn(s string) []string {
	var out []string
	for _, m := range optionRE.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestConvertRetriesDroppedOptions(t *testing.T) {
	fb := &fakeBackend{reply: func(user string) string {
		if strings.Contains(user, "a previous attempt left out") {
			return "fixed: -a --all --color"
		}
		return "lazy summary"
	}}
	out, err := Convert(context.Background(), fb, "ls(1)", sample, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "fixed") || fb.calls.Load() != 2 {
		t.Errorf("out = %q after %d calls", out, fb.calls.Load())
	}
}

func TestOpenAI(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
			return
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-test" ||
			r.Header.Get("X-Extra") != "yes" {
			t.Errorf("bad request: %s %v", r.URL.Path, r.Header)
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Model != "girlboss-1" || len(req.Messages) != 2 || req.Messages[0].Role != "system" {
			t.Errorf("bad body: %+v", req)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"# hiii"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	o := &OpenAI{BaseURL: srv.URL + "/v1/", APIKey: "sk-test", ModelName: "girlboss-1",
		Headers: map[string]string{"X-Extra": "yes"}, Retries: 2}
	got, err := o.Complete(context.Background(), "sys", "user")
	if err != nil || got != "# hiii" {
		t.Fatalf("got %q, %v", got, err)
	}
	if hits.Load() != 2 {
		t.Errorf("%d requests", hits.Load())
	}
}

func TestOpenAITruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"# hi"},"finish_reason":"length"}]}`)
	}))
	defer srv.Close()
	o := &OpenAI{BaseURL: srv.URL, ModelName: "m"}
	if _, err := o.Complete(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), "cut off") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenAINoRetryOnClientError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	o := &OpenAI{BaseURL: srv.URL, ModelName: "m", Retries: 3}
	_, err := o.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "bad key") || hits.Load() != 1 {
		t.Errorf("err = %v after %d hits", err, hits.Load())
	}
}

func TestClaudeArgs(t *testing.T) {
	c := &Claude{Command: "claude", ModelName: "sonnet", Args: []string{"--effort", "low"}}
	a := strings.Join(c.args("SYS"), " ")
	for _, want := range []string{"-p", "--system-prompt SYS", "--model sonnet", "--strict-mcp-config", "--effort low"} {
		if !strings.Contains(a, want) {
			t.Errorf("args %q missing %q", a, want)
		}
	}
}

type memParts struct {
	mu sync.Mutex
	m  map[string]string
}

func (p *memParts) Part(key string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.m[key]
	return s, ok
}

func (p *memParts) PutPart(key, out string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[key] = out
	return nil
}

// A conversion that fails part way picks up where it left off.
func TestConvertResumes(t *testing.T) {
	n := len(Split(sample, 120))
	if n < 3 {
		t.Fatalf("sample splits into %d parts", n)
	}
	fail := true
	var calls atomic.Int32
	b := &errBackend{fn: func(user string) (string, error) {
		calls.Add(1)
		if fail && strings.Contains(user, fmt.Sprintf("this is part %d.", n-1)) {
			return "", errors.New("boom")
		}
		return "ok " + strings.Join(optionsIn(user), " "), nil
	}}
	parts := &memParts{m: map[string]string{}}
	o := Options{ChunkSize: 120, Parallel: 1, Parts: parts}
	if _, err := Convert(context.Background(), b, "ls(1)", sample, o); err == nil {
		t.Fatal("no error")
	}
	// Parts are taken in no set order, so some may not have been tried.
	kept := len(parts.m)
	if kept >= n {
		t.Errorf("kept %d parts of %d", kept, n)
	}
	fail = false
	calls.Store(0)
	if _, err := Convert(context.Background(), b, "ls(1)", sample, o); err != nil {
		t.Fatal(err)
	}
	if got := int(calls.Load()); got != n-kept {
		t.Errorf("the second try made %d requests, want %d", got, n-kept)
	}
}

// Conversions sharing slots never have more requests out than it holds.
func TestConvertSlots(t *testing.T) {
	var cur, most atomic.Int32
	b := &errBackend{fn: func(user string) (string, error) {
		c := cur.Add(1)
		for {
			m := most.Load()
			if c <= m || most.CompareAndSwap(m, c) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		cur.Add(-1)
		return "ok " + strings.Join(optionsIn(user), " "), nil
	}}
	slots := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Convert(context.Background(), b, "ls(1)", sample, Options{ChunkSize: 120, Parallel: 8, Slots: slots}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if m := most.Load(); m != 3 {
		t.Errorf("at most %d requests at once, want 3", m)
	}
}

type errBackend struct {
	fn func(user string) (string, error)
}

func (b *errBackend) Name() string  { return "err" }
func (b *errBackend) Model() string { return "" }
func (b *errBackend) Complete(_ context.Context, _, user string) (string, error) {
	return b.fn(user)
}
