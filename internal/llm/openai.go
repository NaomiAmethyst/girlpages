// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// OpenAI talks to any OpenAI-compatible /chat/completions endpoint (OpenAI,
// OpenRouter, Ollama, llama.cpp, vLLM, LM Studio, …).
type OpenAI struct {
	BaseURL     string
	APIKey      string
	ModelName   string
	Temperature *float64
	MaxTokens   int
	Headers     map[string]string
	// Extra is added to each request's body (OpenRouter's "provider", say).
	Extra map[string]any
	// CompletionWindow is Sail's (see Responses): "", asap or balanced, as
	// chat completions can't wait in the background for flex.
	CompletionWindow string
	Client           *http.Client
	// Retries is how many times a rate-limited or failed request is retried.
	Retries int
}

func (o *OpenAI) Name() string  { return "openai" }
func (o *OpenAI) Model() string { return o.ModelName }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *OpenAI) Complete(ctx context.Context, system, user string) (string, error) {
	if o.ModelName == "" {
		return "", errors.New("openai backend needs a model: set openai.model in the config or GIRLPAGES_MODEL")
	}
	extra := o.Extra
	switch o.CompletionWindow {
	case "":
	case WindowFlex:
		return "", errors.New(`the flex completion window needs the Responses API: set openai.api = "responses"`)
	case WindowASAP, WindowBalanced:
		extra = withMetadata(extra, map[string]any{"completion_window": o.CompletionWindow})
	default:
		return "", fmt.Errorf("completion window %q: want asap, balanced or flex", o.CompletionWindow)
	}
	body, err := withExtra(chatRequest{
		Model: o.ModelName,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: o.Temperature,
		MaxTokens:   o.MaxTokens,
	}, extra)
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(o.BaseURL, "/") + "/chat/completions"
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	var lastErr error
	for attempt := 0; attempt <= o.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(backoff(attempt, lastErr)):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		out, err := o.do(ctx, client, url, body)
		if err == nil {
			return out, nil
		}
		lastErr = err
		var he *httpError
		if errors.As(err, &he) && !he.retryable() {
			break
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", lastErr
}

// withMetadata is extra with meta's fields in its "metadata" object.
func withMetadata(extra, meta map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range extra {
		out[k] = v
	}
	m := map[string]any{}
	if old, ok := out["metadata"].(map[string]any); ok {
		for k, v := range old {
			m[k] = v
		}
	}
	for k, v := range meta {
		m[k] = v
	}
	out["metadata"] = m
	return out
}

// withExtra encodes v, a JSON object, with extra's fields added (or
// replacing its own).
func withExtra(v any, extra map[string]any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, x := range extra {
		if m[k], err = json.Marshal(x); err != nil {
			return nil, fmt.Errorf("extra %s: %w", k, err)
		}
	}
	return json.Marshal(m)
}

type httpError struct {
	status     int
	msg        string
	retryAfter time.Duration
}

func (e *httpError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, e.msg)
}

func (e *httpError) retryable() bool {
	return e.status == http.StatusTooManyRequests || e.status >= 500
}

func backoff(attempt int, err error) time.Duration {
	var he *httpError
	if errors.As(err, &he) && he.retryAfter > 0 {
		return he.retryAfter
	}
	return time.Duration(1<<attempt) * time.Second
}

func (o *OpenAI) do(ctx context.Context, client *http.Client, url string, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	for k, v := range o.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return "", err
	}
	var cr chatResponse
	jsonErr := json.Unmarshal(raw, &cr)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if jsonErr == nil && cr.Error != nil && cr.Error.Message != "" {
			msg = cr.Error.Message
		}
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		he := &httpError{status: resp.StatusCode, msg: msg}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s < 600 {
			he.retryAfter = time.Duration(s) * time.Second
		}
		return "", he
	}
	if jsonErr != nil {
		return "", fmt.Errorf("decoding response: %w", jsonErr)
	}
	if cr.Error != nil && cr.Error.Message != "" {
		return "", errors.New(cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", errors.New("response had no choices")
	}
	c := cr.Choices[0]
	if c.FinishReason == "length" {
		return "", errors.New("reply was cut off at the model's output limit; lower chunk_size or raise openai.max_tokens")
	}
	return c.Message.Content, nil
}
