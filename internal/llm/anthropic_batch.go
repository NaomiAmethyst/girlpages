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

// AnthropicBatch talks to Anthropic's Message Batches API
// (https://api.anthropic.com/v1/messages/batches). Unlike OpenRouter's,
// each request names its model, and results are a JSONL file to download
// once the batch has ended.
type AnthropicBatch struct {
	URL       string
	APIKey    string
	Model     string
	MaxTokens int
	// Temperature is left to the API's default unless set.
	Temperature *float64
	Headers     map[string]string
	Client      *http.Client
	Retries     int
}

// anthropicVersion is the API version girlpages speaks.
const anthropicVersion = "2023-06-01"

func (c *AnthropicBatch) Kind() string      { return "anthropic" }
func (c *AnthropicBatch) ModelName() string { return c.Model }

// Limits: 256 MB or 100,000 requests; stay well under.
func (c *AnthropicBatch) Limits() (int, int) { return 100 << 20, 50000 }

type anthropicParams struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature *float64           `json:"temperature,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicBatchObject struct {
	ID               string `json:"id"`
	ProcessingStatus string `json:"processing_status"`
	RequestCounts    struct {
		Processing int `json:"processing"`
		Succeeded  int `json:"succeeded"`
		Errored    int `json:"errored"`
		Canceled   int `json:"canceled"`
		Expired    int `json:"expired"`
	} `json:"request_counts"`
	ResultsURL string `json:"results_url"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicResultLine struct {
	CustomID string `json:"custom_id"`
	Result   struct {
		Type    string `json:"type"`
		Message *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
		} `json:"message"`
		Error *struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"error"`
	} `json:"result"`
}

// item is a request as a submission has it.
func (c *AnthropicBatch) item(r BatchRequest) ([]byte, error) {
	maxTokens := c.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 32000
	}
	return encode(struct {
		CustomID string          `json:"custom_id"`
		Params   anthropicParams `json:"params"`
	}{r.CustomID, anthropicParams{
		Model: c.Model, MaxTokens: maxTokens, System: r.Part.System, Temperature: c.Temperature,
		Messages: []anthropicMessage{{Role: "user", Content: r.Part.User}},
	}})
}

func (c *AnthropicBatch) RequestBytes(r BatchRequest) (int, error) {
	b, err := c.item(r)
	return len(b) + 1, err
}

func (c *AnthropicBatch) Submit(ctx context.Context, reqs []BatchRequest) (*Batch, error) {
	if c.Model == "" {
		return nil, errors.New("anthropic batches need a model: set anthropic.model")
	}
	items, err := itemsOf(reqs, c.item)
	if err != nil {
		return nil, err
	}
	body, err := submission(struct{}{}, items)
	if err != nil {
		return nil, err
	}
	raw, err := c.request(ctx, http.MethodPost, strings.TrimRight(c.URL, "/"), body)
	if err != nil {
		return nil, err
	}
	return c.batch(ctx, raw, false)
}

func (c *AnthropicBatch) Get(ctx context.Context, id string) (*Batch, error) {
	raw, err := c.request(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/"+id, nil)
	if err != nil {
		return nil, err
	}
	return c.batch(ctx, raw, true)
}

// batch turns a batch object into a Batch, downloading its results if it
// has ended and results are wanted.
func (c *AnthropicBatch) batch(ctx context.Context, raw []byte, results bool) (*Batch, error) {
	var obj anthropicBatchObject
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("decoding batch: %w", err)
	}
	n := obj.RequestCounts
	b := &Batch{ID: obj.ID, Status: obj.ProcessingStatus,
		Total:     n.Processing + n.Succeeded + n.Errored + n.Canceled + n.Expired,
		Completed: n.Succeeded, Failed: n.Errored + n.Canceled + n.Expired}
	if obj.Error != nil {
		b.Error = obj.Error.Message
	}
	if obj.ProcessingStatus != "ended" {
		return b, nil
	}
	// Ended means every request has an outcome, even if it's "expired".
	b.Status = "completed"
	if !results {
		return b, nil
	}
	if obj.ResultsURL == "" {
		return nil, fmt.Errorf("batch %s ended without a results_url", obj.ID)
	}
	body, err := c.request(ctx, http.MethodGet, obj.ResultsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("batch %s results: %w", obj.ID, err)
	}
	b.Results = map[string]BatchResult{}
	dec := json.NewDecoder(bytes.NewReader(body))
	for {
		var line anthropicResultLine
		if err := dec.Decode(&line); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("batch %s results: %w", obj.ID, err)
		}
		b.Results[line.CustomID] = anthropicResult(line)
	}
	return b, nil
}

func anthropicResult(line anthropicResultLine) BatchResult {
	r := line.Result
	switch r.Type {
	case "succeeded":
		if r.Message == nil {
			return BatchResult{Err: errors.New("succeeded without a message")}
		}
		if r.Message.StopReason == "max_tokens" {
			return BatchResult{Err: errors.New("reply was cut off at max_tokens; raise anthropic.max_tokens or lower chunk_size")}
		}
		var text strings.Builder
		for _, c := range r.Message.Content {
			if c.Type == "text" { // not thinking
				text.WriteString(c.Text)
			}
		}
		return BatchResult{Reply: text.String()}
	case "errored":
		if r.Error != nil && r.Error.Error.Message != "" {
			return BatchResult{Err: fmt.Errorf("%s: %s", r.Error.Error.Type, r.Error.Error.Message)}
		}
		return BatchResult{Err: errors.New("errored")}
	case "":
		return BatchResult{Err: errors.New("no result")}
	}
	return BatchResult{Err: errors.New(r.Type)} // canceled, expired
}

func (c *AnthropicBatch) request(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	return retry(ctx, c.Retries, func() ([]byte, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rd)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("x-api-key", c.APIKey)
		req.Header.Set("anthropic-version", anthropicVersion)
		for k, v := range c.Headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			msg := strings.TrimSpace(string(raw))
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
				msg = e.Error.Message
			}
			if len(msg) > 500 {
				msg = msg[:500] + "…"
			}
			he := &httpError{status: resp.StatusCode, msg: msg}
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s < 600 {
				he.retryAfter = time.Duration(s) * time.Second
			}
			// Anthropic says 529 when it's overloaded.
			return nil, he
		}
		return raw, nil
	})
}
