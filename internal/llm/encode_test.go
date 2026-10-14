// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Each batch API's requests are the size it says, as sent, and man pages'
// <, > and & go as they are.
func TestBatchRequestBytes(t *testing.T) {
	escaped := "\\" + "u003c" // how json.Marshal writes <
	reqs := []BatchRequest{
		{CustomID: "0-0", Part: Part{System: "be girly", User: "SYNOPSIS\n  ls [<file>] && <b>bold</b> \"quoted\" é"}},
		{CustomID: "12-3", Part: Part{System: "be girly", User: strings.Repeat("<a&b> ", 500)}},
	}
	for _, c := range []interface {
		BatchAPI
		item(BatchRequest) ([]byte, error)
	}{
		&OpenRouterBatch{Model: "m", Extra: map[string]any{"reasoning": map[string]any{"enabled": false}}},
		&AnthropicBatch{Model: "m"},
		&SailBatch{Model: "m"},
	} {
		items, err := itemsOf(reqs, c.item)
		if err != nil {
			t.Fatal(err)
		}
		body, err := submission(struct {
			Endpoint string `json:"endpoint"`
		}{"/x"}, items)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(body) {
			t.Fatalf("%s: not JSON: %.200s", c.Kind(), body)
		}
		total := 0
		for _, r := range reqs {
			n, err := c.RequestBytes(r)
			if err != nil {
				t.Fatal(err)
			}
			total += n
		}
		// The head and brackets, less the last request's comma.
		if overhead := len(body) - total; overhead < 0 || overhead > 64 {
			t.Errorf("%s: requests measured %d bytes, sent in %d", c.Kind(), total, len(body))
		}
		if strings.Contains(string(body), escaped) || !strings.Contains(string(body), "<a&b>") {
			t.Errorf("%s: <, > and & should go as they are: %.300s", c.Kind(), body)
		}
	}
}

// A run too big for one batch is sent in several, none over the limit.
func TestBatcherSplitsByBytes(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sizes = append(sizes, len(b))
		n := len(sizes)
		mu.Unlock()
		if !json.Valid(b) {
			t.Errorf("not JSON: %.200s", b)
		}
		fmt.Fprintf(w, `{"id":"batch_%d","request_counts":1,"created_at":1}`, n)
	}))
	defer srv.Close()
	const limit = 20000
	b := &Batcher{Client: &SailBatch{URL: srv.URL + "/v1/batches", Model: "m"}, Dir: t.TempDir(),
		Options: Options{ChunkSize: 2000}, MaxBytes: limit}
	var jobs []BatchJob
	for i := 0; i < 30; i++ {
		jobs = append(jobs, BatchJob{Key: fmt.Sprint(i), Title: fmt.Sprintf("P%d(1)", i), Text: page(fmt.Sprintf("p%d", i), "-a <x> & -b <y>")})
	}
	if _, err := b.Submit(context.Background(), jobs); err != nil {
		t.Fatal(err)
	}
	if len(sizes) < 2 {
		t.Fatalf("one batch of %v bytes", sizes)
	}
	for _, n := range sizes {
		if n > limit {
			t.Errorf("a batch of %d bytes, over %d", n, limit)
		}
	}
}

// A batch the API refuses as too big (by a limit of its own) is sent again
// in smaller batches, from where it started; the ones before it stay sent.
func TestBatcherTooBig(t *testing.T) {
	const hidden = 30000
	var mu sync.Mutex
	var accepted []int
	refused := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if len(b) > hidden {
			refused++
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			fmt.Fprint(w, `{"error":{"message":"request body too large (max 256 MB)","type":"invalid_request_error"}}`)
			return
		}
		accepted = append(accepted, strings.Count(string(b), `"custom_id"`))
		fmt.Fprintf(w, `{"id":"batch_%d","request_counts":1,"created_at":1}`, len(accepted))
	}))
	defer srv.Close()
	b := &Batcher{Client: &SailBatch{URL: srv.URL + "/v1/batches", Model: "m"}, Dir: t.TempDir(),
		Options: Options{ChunkSize: 2000}, MaxBytes: 4 * hidden}
	var jobs []BatchJob
	for i := 0; i < 60; i++ {
		jobs = append(jobs, BatchJob{Key: fmt.Sprint(i), Title: fmt.Sprintf("P%d(1)", i), Text: page(fmt.Sprintf("p%d", i), "-a <x> & -b <y>")})
	}
	sent, err := b.Submit(context.Background(), jobs)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, n := range accepted {
		total += n
	}
	if refused == 0 || total != sent || sent < len(jobs) {
		t.Errorf("refused %d, accepted %v (%d requests), sent %d for %d pages", refused, accepted, total, sent, len(jobs))
	}
	saved, _ := b.Pending()
	if len(saved) != len(jobs) {
		t.Errorf("%d of %d pages saved as pending", len(saved), len(jobs))
	}
}

// A long page's parts say which sections came before, but only the last
// few, so a page with thousands doesn't make prompts bigger than its text.
func TestCoveredSections(t *testing.T) {
	var hs []string
	for i := 0; i < 5000; i++ {
		hs = append(hs, fmt.Sprintf("SECTION %d %s", i, strings.Repeat("x", i%200)))
	}
	p := UserPrompt("BIG(1)", 3, 9, hs, "text\n")
	if len(p) > 2000 || !strings.Contains(p, "4988 earlier ones, then SECTION 4988") || !strings.Contains(p, "SECTION 4999") || strings.Contains(p, "SECTION 4987 ") {
		t.Errorf("prompt (%d bytes): %.600s", len(p), p)
	}
	if got := covered([]string{"NAME", "SYNOPSIS"}); got != "NAME, SYNOPSIS" {
		t.Errorf("a few sections: %q", got)
	}
}

// A page too big for a batch even alone is left out, and the rest go on.
func TestBatcherSkipsHugePage(t *testing.T) {
	var mu sync.Mutex
	var accepted int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(string(b), "HUGE(1)") {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			fmt.Fprint(w, `{"error":{"message":"request body too large (max 256 MB)"}}`)
			return
		}
		accepted += strings.Count(string(b), `"custom_id"`)
		fmt.Fprintf(w, `{"id":"batch_%d","request_counts":1,"created_at":1}`, accepted)
	}))
	defer srv.Close()
	b := &Batcher{Client: &SailBatch{URL: srv.URL + "/v1/batches", Model: "m"}, Dir: t.TempDir(), Options: Options{ChunkSize: 2000}, MaxRequests: 1}
	jobs := []BatchJob{
		{Key: "a", Title: "A(1)", Text: page("a", "-a")},
		{Key: "huge", Title: "HUGE(1)", Text: page("huge", "-h")},
		{Key: "b", Title: "B(1)", Text: page("b", "-b")},
	}
	if _, err := b.Submit(context.Background(), jobs); err != nil {
		t.Fatal(err)
	}
	if saved, _ := b.Pending(); len(saved) != 2 || !saved["a"] || !saved["b"] {
		t.Errorf("pending: %v", saved)
	}
}
