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
	"time"
)

const doneOutput = `[{"type":"reasoning","summary":[]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"# hi"},{"type":"output_text","text":"iii"}]}]`

func TestResponsesForeground(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/responses" || body["model"] != "glm" || body["instructions"] != "sys" || body["input"] != "user" ||
			body["background"] != nil || body["metadata"] != nil || body["max_output_tokens"] != 900.0 {
			t.Errorf("bad request %s: %v", r.URL.Path, body)
		}
		fmt.Fprintf(w, `{"id":"resp_1","status":"completed","output":%s}`, doneOutput)
	}))
	defer srv.Close()
	r := &Responses{BaseURL: srv.URL + "/v1", ModelName: "glm", MaxTokens: 900}
	if got, err := r.Complete(context.Background(), "sys", "user"); err != nil || got != "# hiiii" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Flex: in the background, retried with the same idempotency key, then
// polled until it's done.
func TestResponsesBackground(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost:
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			var body struct {
				Background bool           `json:"background"`
				Metadata   map[string]any `json:"metadata"`
				Provider   any            `json:"provider"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if !body.Background || body.Metadata["completion_window"] != "flex" || body.Metadata["label"] != "girl" || body.Provider == nil {
				t.Errorf("bad request: %+v", body)
			}
			if len(keys) == 1 {
				w.Header().Set("Retry-After", "1")
				http.Error(w, `{"error":{"message":"busy"}}`, http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"resp_9","status":"queued","output":[]}`)
		case r.URL.Path == "/v1/responses/resp_9":
			polls++
			if polls < 3 {
				fmt.Fprint(w, `{"id":"resp_9","status":"in_progress","output":[]}`)
				return
			}
			fmt.Fprintf(w, `{"id":"resp_9","status":"completed","output":%s}`, doneOutput)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	r := &Responses{BaseURL: srv.URL + "/v1", ModelName: "glm", CompletionWindow: WindowFlex, Poll: 10 * time.Millisecond, Retries: 2,
		Extra: map[string]any{"metadata": map[string]any{"label": "girl"}, "provider": map[string]any{"only": []any{"x"}}}}
	got, err := r.Complete(context.Background(), "sys", "user")
	if err != nil || got != "# hiiii" {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] || polls != 3 {
		t.Errorf("keys %v, %d polls", keys, polls)
	}
}

func TestResponsesUnfinished(t *testing.T) {
	for body, want := range map[string]string{
		`{"id":"a","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`: "cut off",
		`{"id":"a","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`:    "content_filter",
		`{"id":"a","status":"failed","error":{"message":"The model did not return visible text"}}`:         "did not return",
		`{"id":"a","status":"completed","output":[{"type":"reasoning"}]}`:                                  "no text",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		_, err := (&Responses{BaseURL: srv.URL, ModelName: "m"}).Complete(context.Background(), "s", "u")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", body, err)
		}
		srv.Close()
	}
}

// Chat completions can be balanced (as metadata), but not flex.
func TestOpenAICompletionWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Metadata map[string]any `json:"metadata"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Metadata["completion_window"] != "balanced" || body.Metadata["label"] != "girl" {
			t.Errorf("metadata: %v", body.Metadata)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	o := &OpenAI{BaseURL: srv.URL, ModelName: "m", CompletionWindow: WindowBalanced,
		Extra: map[string]any{"metadata": map[string]any{"label": "girl"}}}
	if got, err := o.Complete(context.Background(), "s", "u"); err != nil || got != "ok" {
		t.Fatalf("balanced: %q, %v", got, err)
	}
	o.CompletionWindow = WindowFlex
	if _, err := o.Complete(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), "Responses API") {
		t.Errorf("flex over chat: %v", err)
	}
}

func TestSailBatch(t *testing.T) {
	var mu sync.Mutex
	gets := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/batches":
			var sub struct {
				Endpoint string `json:"endpoint"`
				Requests []struct {
					CustomID string         `json:"custom_id"`
					Params   map[string]any `json:"params"`
				} `json:"requests"`
			}
			json.NewDecoder(r.Body).Decode(&sub)
			if sub.Endpoint != "/v1/responses" || len(sub.Requests) != 2 || sub.Requests[1].CustomID != "0-1" ||
				sub.Requests[1].Params["instructions"] != "sys" || sub.Requests[1].Params["input"] != "part two" ||
				sub.Requests[1].Params["background"] != nil || r.Header.Get("Idempotency-Key") == "" {
				t.Errorf("bad batch: %+v", sub)
			}
			fmt.Fprint(w, `{"id":"batch_1","request_counts":2,"created_at":1}`)
		case r.URL.Path == "/v1/batches/batch_1":
			gets["inline"]++
			if gets["inline"] == 1 {
				fmt.Fprint(w, `{"id":"batch_1","status":"in_progress","request_counts":{"total":2,"completed":1,"failed":0},"request_status":{},"results":null,"error":null}`)
				return
			}
			fmt.Fprintf(w, `{"id":"batch_1","status":"completed","request_counts":{"total":2,"completed":1,"failed":1},"request_status":{},
				"results":[{"id":"r1","custom_id":"0-0","response":{"status_code":200,"request_id":"r1","body":{"id":"r1","status":"completed","output":%s}},"error":null},
				{"id":"r2","custom_id":"0-1","response":null,"error":{"message":"cancelled"}}],"error":null}`, doneOutput)
		case r.URL.Path == "/v1/batches/batch_big":
			fmt.Fprint(w, `{"id":"batch_big","status":"completed","request_counts":{"total":2,"completed":1,"failed":1},
				"request_status":{"0-0":{"status":"COMPLETED"},"0-1":{"status":"FAILED"}},"results":null,
				"error":{"code":"batch_results_too_large","message":"fetch them one at a time"}}`)
		case r.URL.Path == "/v1/batches/batch_big/0-0":
			fmt.Fprintf(w, `{"id":"r1","status":"completed","output":%s}`, doneOutput)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &SailBatch{URL: srv.URL + "/v1/batches", Model: "glm"}
	ctx := context.Background()
	b, err := c.Submit(ctx, []BatchRequest{{CustomID: "0-0", Part: Part{System: "sys", User: "part one"}}, {CustomID: "0-1", Part: Part{System: "sys", User: "part two"}}})
	if err != nil || b.ID != "batch_1" {
		t.Fatalf("submit: %+v, %v", b, err)
	}
	if b, err = c.Get(ctx, "batch_1"); err != nil || b.Status != "in_progress" || b.Results != nil || b.Completed != 1 {
		t.Fatalf("in progress: %+v, %v", b, err)
	}
	if b, err = c.Get(ctx, "batch_1"); err != nil || b.Status != "completed" || b.Results["0-0"].Reply != "# hiiii" || b.Results["0-1"].Err == nil {
		t.Fatalf("completed: %+v, %v", b, err)
	}
	if b, err = c.Get(ctx, "batch_big"); err != nil || b.Results["0-0"].Reply != "# hiiii" || b.Results["0-1"].Err == nil {
		t.Fatalf("too big for inline: %+v, %v", b, err)
	}
}
