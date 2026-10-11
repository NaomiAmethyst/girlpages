// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package llmtest has a fake OpenRouter batch API for tests.
package llmtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// BatchServer is a fake OpenRouter batch endpoint. Each batch reports
// in_progress for Polls lookups, then completed, replying to every request
// with Reply(user prompt).
type BatchServer struct {
	*httptest.Server
	// Polls is how many lookups a batch stays in progress for.
	Polls int
	// Reply makes a request's reply; returning an error string fails it.
	Reply func(user string) (reply, fail string)

	mu      sync.Mutex
	batches map[string]*fakeBatch
	// Submitted records each submission's raw body.
	Submitted []string
	// Auth records the Authorization headers seen.
	Auth []string
}

type fakeBatch struct {
	model string
	reqs  []struct {
		CustomID string `json:"custom_id"`
		Body     struct {
			Model    string `json:"model"`
			Messages []struct {
				Role, Content string
			} `json:"messages"`
		} `json:"body"`
	}
	polls int
}

// NewBatchServer starts a fake batch endpoint at URL + "/api/v1/batches".
func NewBatchServer() *BatchServer {
	s := &BatchServer{batches: map[string]*fakeBatch{}, Polls: 1}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// BatchURL is the endpoint to configure.
func (s *BatchServer) BatchURL() string { return s.URL + "/api/v1/batches" }

func (s *BatchServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Auth = append(s.Auth, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/batches":
		raw, _ := io.ReadAll(r.Body)
		s.Submitted = append(s.Submitted, string(raw))
		var sub struct {
			Endpoint, Model string
			Requests        json.RawMessage
		}
		b := &fakeBatch{}
		if err := json.Unmarshal(raw, &sub); err != nil || json.Unmarshal(sub.Requests, &b.reqs) != nil || sub.Model == "" {
			http.Error(w, `{"error":{"message":"bad batch"}}`, http.StatusBadRequest)
			return
		}
		b.model = sub.Model
		id := fmt.Sprintf("batch_%d", len(s.batches)+1)
		s.batches[id] = b
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"id":%q,"object":"batch","status":"validating","request_counts":{"total":%d,"completed":0,"failed":0}}`, id, len(b.reqs))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/batches/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/batches/")
		b := s.batches[id]
		if b == nil {
			http.Error(w, `{"error":{"message":"no such batch"}}`, http.StatusNotFound)
			return
		}
		b.polls++
		out := map[string]any{"id": id, "object": "batch", "model": b.model}
		counts := map[string]int{"total": len(b.reqs)}
		if b.polls <= s.Polls {
			out["status"] = "in_progress"
			out["results"] = nil
		} else {
			out["status"] = "completed"
			var results []map[string]any
			for _, q := range b.reqs {
				user := ""
				for _, m := range q.Body.Messages {
					if m.Role == "user" {
						user = m.Content
					}
				}
				reply, fail := s.Reply(user)
				if fail != "" {
					counts["failed"]++
					results = append(results, map[string]any{"custom_id": q.CustomID, "response": nil,
						"error": map[string]string{"message": fail}})
					continue
				}
				counts["completed"]++
				results = append(results, map[string]any{"custom_id": q.CustomID, "error": nil, "response": map[string]any{
					"status_code": 200,
					"body": map[string]any{"choices": []map[string]any{
						{"message": map[string]string{"role": "assistant", "content": reply}, "finish_reason": "stop"},
					}},
				}})
			}
			out["results"] = results
			out["usage"] = map[string]any{"cost": 0.0125}
		}
		out["request_counts"] = counts
		json.NewEncoder(w).Encode(out)
	default:
		http.NotFound(w, r)
	}
}

// Batches is how many batches were submitted.
func (s *BatchServer) Batches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.batches)
}

// Requests is how many requests were submitted in all.
func (s *BatchServer) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.batches {
		n += len(b.reqs)
	}
	return n
}

// EchoOptions is a Reply that keeps every option in the prompt, so
// conversions pass the completeness check.
func EchoOptions(prefix string) func(string) (string, string) {
	return func(user string) (string, string) {
		var opts []string
		for _, w := range strings.Fields(user) {
			if strings.HasPrefix(w, "-") {
				opts = append(opts, w)
			}
		}
		return "# ✨ NAME ✨\n\n" + prefix + " " + strings.Join(opts, " "), ""
	}
}
