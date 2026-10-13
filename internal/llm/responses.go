// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Completion windows, as Sail (https://docs.sailresearch.com/completion-windows)
// has them: how long a request may wait, for a lower price.
const (
	WindowASAP     = "asap"     // straight away (the default)
	WindowBalanced = "balanced" // queued for a while
	WindowFlex     = "flex"     // whenever there's room; Responses in the background only
)

// ValidWindow reports whether w is a completion window ("" for none).
func ValidWindow(w string) bool {
	switch w {
	case "", WindowASAP, WindowBalanced, WindowFlex:
		return true
	}
	return false
}

// Responses talks to an OpenAI-compatible /responses endpoint (OpenAI's,
// Sail's). With a completion window that waits (balanced or flex), each
// request goes in the background and is polled until it's done.
type Responses struct {
	BaseURL     string
	APIKey      string
	ModelName   string
	Temperature *float64
	MaxTokens   int
	Headers     map[string]string
	// Extra is added to each request's body; a "metadata" object in it is
	// merged with the request's own.
	Extra map[string]any
	// CompletionWindow is "" (the server's default), asap, balanced or flex.
	CompletionWindow string
	// Poll is the longest wait between checks on a background request
	// (they start quicker); 30 seconds if it's 0.
	Poll   time.Duration
	Client *http.Client
	// Retries is how many times a rate-limited or failed call is retried.
	Retries int
}

func (r *Responses) Name() string  { return "openai" }
func (r *Responses) Model() string { return r.ModelName }

// background reports whether requests in a window go in the background.
func background(window string) bool { return window == WindowBalanced || window == WindowFlex }

// responsesParams is a Responses request's body for a part: system as the
// instructions, user as the input.
func responsesParams(model, system, user string, temperature *float64, maxTokens int, window string, bg bool, extra map[string]any) ([]byte, error) {
	body := map[string]any{"model": model, "instructions": system, "input": user}
	if temperature != nil {
		body["temperature"] = *temperature
	}
	if maxTokens > 0 {
		body["max_output_tokens"] = maxTokens
	}
	meta := map[string]any{}
	if window != "" {
		meta["completion_window"] = window
	}
	if bg {
		body["background"] = true
	}
	for k, v := range extra {
		if m, ok := v.(map[string]any); ok && k == "metadata" {
			for mk, mv := range m {
				meta[mk] = mv
			}
			continue
		}
		body[k] = v
	}
	if len(meta) > 0 {
		body["metadata"] = meta
	}
	return encode(body)
}

// responseObject is what a Responses request (or poll) returns.
type responseObject struct {
	ID                string          `json:"id"`
	Status            string          `json:"status"`
	Output            json.RawMessage `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// done reports whether a response has finished, one way or another.
func (o *responseObject) done() bool {
	switch o.Status {
	case "completed", "incomplete", "failed", "cancelled":
		return true
	}
	return false
}

// text is a finished response's reply, or why there isn't one.
func (o *responseObject) text() (string, error) {
	switch o.Status {
	case "completed":
	case "incomplete":
		reason := ""
		if o.IncompleteDetails != nil {
			reason = o.IncompleteDetails.Reason
		}
		if reason == "max_output_tokens" || reason == "" {
			return "", errors.New("reply was cut off at the model's output limit; lower chunk_size or raise openai.max_tokens")
		}
		return "", fmt.Errorf("reply stopped early (%s)", reason)
	case "failed", "cancelled":
		if o.Error != nil && o.Error.Message != "" {
			return "", fmt.Errorf("response %s: %s", o.Status, o.Error.Message)
		}
		return "", fmt.Errorf("response %s", o.Status)
	default:
		return "", fmt.Errorf("response is %s, not done", o.Status)
	}
	// The output is a list of items, of which messages have the text (and
	// reasoning, which isn't wanted, is others); some servers give a string.
	var s string
	if json.Unmarshal(o.Output, &s) == nil {
		return s, nil
	}
	var items []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(o.Output, &items); err != nil {
		return "", fmt.Errorf("reading the response's output: %w", err)
	}
	var b strings.Builder
	for _, it := range items {
		if it.Type != "message" && it.Type != "" {
			continue
		}
		for _, c := range it.Content {
			if c.Type == "output_text" {
				b.WriteString(c.Text)
			}
		}
	}
	if b.Len() == 0 {
		return "", errors.New("response had no text")
	}
	return b.String(), nil
}

// newKey is an idempotency key: the same for every try of one request, so
// retrying it can't start the work twice.
func newKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (r *Responses) Complete(ctx context.Context, system, user string) (string, error) {
	if r.ModelName == "" {
		return "", errors.New("openai backend needs a model: set openai.model in the config or GIRLPAGES_MODEL")
	}
	if !ValidWindow(r.CompletionWindow) {
		return "", fmt.Errorf("completion window %q: want asap, balanced or flex", r.CompletionWindow)
	}
	bg := background(r.CompletionWindow)
	body, err := responsesParams(r.ModelName, system, user, r.Temperature, r.MaxTokens, r.CompletionWindow, bg, r.Extra)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(r.BaseURL, "/") + "/responses"
	key := newKey()
	obj, err := retry(ctx, r.Retries, func() (*responseObject, error) {
		return r.call(ctx, http.MethodPost, base, body, key)
	})
	if err != nil {
		return "", err
	}
	// Then wait, checking quickly at first and less often as it goes on: a
	// poll that fails is tried again, as giving up wouldn't stop the work.
	most := r.Poll
	if most <= 0 {
		most = 30 * time.Second
	}
	wait := min(time.Second, most)
	for !obj.done() {
		if obj.ID == "" {
			return "", fmt.Errorf("response is %s, with no id to check on", obj.Status)
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		wait = min(wait*3/2, most)
		next, err := retry(ctx, r.Retries, func() (*responseObject, error) {
			return r.call(ctx, http.MethodGet, base+"/"+url.PathEscape(obj.ID), nil, "")
		})
		if err != nil {
			var he *httpError
			if errors.As(err, &he) && !he.retryable() || ctx.Err() != nil {
				return "", fmt.Errorf("checking on response %s: %w", obj.ID, err)
			}
			continue
		}
		obj = next
	}
	return obj.text()
}

func (r *Responses) call(ctx context.Context, method, u string, body []byte, key string) (*responseObject, error) {
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	var obj responseObject
	if err := doJSON(ctx, client, method, u, body, r.APIKey, r.Headers, key, &obj); err != nil {
		return nil, err
	}
	return &obj, nil
}

// doJSON makes a request, decoding a 2xx reply's JSON into v, and making
// anything else an httpError (with the server's message, and Retry-After).
func doJSON(ctx context.Context, client *http.Client, method, u string, body []byte, apiKey string, headers map[string]string, idempotencyKey string, v any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		var e struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		he := &httpError{status: resp.StatusCode, msg: msg}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s < 600 {
			he.retryAfter = time.Duration(s) * time.Second
		}
		return he
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("decoding the reply: %w", err)
	}
	return nil
}

// SailBatch talks to Sail's batch API (https://api.sailresearch.com/v1/batches),
// which runs Responses requests in the flex window: Sail's lowest prices,
// whenever there's room.
type SailBatch struct {
	URL         string
	APIKey      string
	Headers     map[string]string
	Model       string
	Temperature *float64
	MaxTokens   int
	// Extra is added to each request's parameters, as Responses.Extra is.
	Extra   map[string]any
	Client  *http.Client
	Retries int
}

func (c *SailBatch) Kind() string      { return "sail" }
func (c *SailBatch) ModelName() string { return c.Model }

// Limits: Sail takes up to 100,000 requests and 256 MB, but by its
// reckoning, which a batch of 200 MiB as sent (no more) was over; stay
// well under. (A batch it refuses as too big is sent again in smaller
// ones: see Batcher.Submit.)
func (c *SailBatch) Limits() (int, int) { return 100 << 20, 20000 }

// item is a request as a submission has it.
func (c *SailBatch) item(r BatchRequest) ([]byte, error) {
	p, err := responsesParams(c.Model, r.Part.System, r.Part.User, c.Temperature, c.MaxTokens, "", false, c.Extra)
	if err != nil {
		return nil, err
	}
	return encode(struct {
		CustomID string          `json:"custom_id"`
		Params   json.RawMessage `json:"params"`
	}{r.CustomID, p})
}

func (c *SailBatch) RequestBytes(r BatchRequest) (int, error) {
	b, err := c.item(r)
	return len(b) + 1, err
}

func (c *SailBatch) client() *http.Client {
	if c.Client == nil {
		return http.DefaultClient
	}
	return c.Client
}

// Submit sends requests as one batch.
func (c *SailBatch) Submit(ctx context.Context, reqs []BatchRequest) (*Batch, error) {
	if c.Model == "" {
		return nil, errors.New("batches need a model: set openai.model in the config or GIRLPAGES_MODEL")
	}
	items, err := itemsOf(reqs, c.item)
	if err != nil {
		return nil, err
	}
	body, err := submission(struct {
		Endpoint string `json:"endpoint"`
	}{"/v1/responses"}, items)
	if err != nil {
		return nil, err
	}
	key := newKey()
	var created struct {
		ID string `json:"id"`
	}
	_, err = retry(ctx, c.Retries, func() (struct{}, error) {
		return struct{}{}, doJSON(ctx, c.client(), http.MethodPost, strings.TrimRight(c.URL, "/"), body, c.APIKey, c.Headers, key, &created)
	})
	if err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, errors.New("the batch was made, but Sail didn't say its id")
	}
	return &Batch{ID: created.ID, Status: "in_progress", Total: len(reqs)}, nil
}

// Get fetches a batch's status, and its results once it has completed:
// inline, or one at a time if there are too many for that.
func (c *SailBatch) Get(ctx context.Context, id string) (*Batch, error) {
	var obj struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		RequestCounts struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
			Failed    int `json:"failed"`
		} `json:"request_counts"`
		RequestStatus map[string]struct {
			Status string `json:"status"`
		} `json:"request_status"`
		Results []struct {
			CustomID string `json:"custom_id"`
			Response *struct {
				Body responseObject `json:"body"`
			} `json:"response"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"results"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	u := strings.TrimRight(c.URL, "/") + "/" + url.PathEscape(id)
	_, err := retry(ctx, c.Retries, func() (struct{}, error) {
		return struct{}{}, doJSON(ctx, c.client(), http.MethodGet, u, nil, c.APIKey, c.Headers, "", &obj)
	})
	if err != nil {
		return nil, err
	}
	b := &Batch{ID: obj.ID, Status: obj.Status, Total: obj.RequestCounts.Total,
		Completed: obj.RequestCounts.Completed, Failed: obj.RequestCounts.Failed}
	if b.ID == "" {
		b.ID = id
	}
	if obj.Status != "completed" {
		return b, nil
	}
	b.Results = map[string]BatchResult{}
	if obj.Results == nil {
		// Too big to give inline: each result on its own.
		for cid, st := range obj.RequestStatus {
			if st.Status != "COMPLETED" {
				b.Results[cid] = BatchResult{Err: fmt.Errorf("request %s", strings.ToLower(st.Status))}
				continue
			}
			var ro responseObject
			_, err := retry(ctx, c.Retries, func() (struct{}, error) {
				return struct{}{}, doJSON(ctx, c.client(), http.MethodGet, u+"/"+url.PathEscape(cid), nil, c.APIKey, c.Headers, "", &ro)
			})
			if err != nil {
				return nil, fmt.Errorf("fetching result %s: %w", cid, err)
			}
			text, err := ro.text()
			b.Results[cid] = BatchResult{Reply: text, Err: err}
		}
		return b, nil
	}
	for _, r := range obj.Results {
		switch {
		case r.Error != nil:
			b.Results[r.CustomID] = BatchResult{Err: errors.New(r.Error.Message)}
		case r.Response == nil:
			b.Results[r.CustomID] = BatchResult{Err: errors.New("no response")}
		default:
			text, err := r.Response.Body.text()
			b.Results[r.CustomID] = BatchResult{Reply: text, Err: err}
		}
	}
	return b, nil
}
