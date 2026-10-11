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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BatchAPI is a provider's batch API: many requests submitted at once,
// answered within a day at about half the price.
type BatchAPI interface {
	// Kind names the API ("openrouter", "anthropic"); saved batches are
	// only collected by a client of the same kind.
	Kind() string
	ModelName() string
	Submit(ctx context.Context, reqs []BatchRequest) (*Batch, error)
	// Get fetches a batch's status, and its results once it has completed.
	Get(ctx context.Context, id string) (*Batch, error)
	// Limits are how big a single batch may be.
	Limits() (maxBytes, maxRequests int)
}

// OpenRouterBatch talks to OpenRouter's batch API
// (https://openrouter.ai/api/v1/batches), which takes OpenAI-style chat
// completions and returns results inline.
type OpenRouterBatch struct {
	URL         string
	APIKey      string
	Headers     map[string]string
	Model       string
	Temperature *float64
	MaxTokens   int
	Client      *http.Client
	// Retries is how many times a rate-limited or failed call is retried.
	Retries int
}

// Batch is a submitted batch as the server last described it.
type Batch struct {
	ID     string
	Status string
	// Total, Completed and Failed count requests.
	Total, Completed, Failed int
	// Cost is what the batch cost, once it's finished.
	Cost  float64
	Error string
	// Results are the replies by custom ID, once the batch has completed.
	Results map[string]BatchResult
}

// BatchResult is one request's outcome.
type BatchResult struct {
	Reply string
	Err   error
}

func (c *OpenRouterBatch) Kind() string      { return "openrouter" }
func (c *OpenRouterBatch) ModelName() string { return c.Model }

// Limits: OpenRouter takes up to 200 MB; stay well under.
func (c *OpenRouterBatch) Limits() (int, int) { return 100 << 20, 10000 }

// Finished reports whether the batch is done, one way or another.
func (b *Batch) Finished() bool {
	switch b.Status {
	case "completed", "failed", "expired", "cancelled":
		return true
	}
	return false
}

// BatchRequest is one request to submit.
type BatchRequest struct {
	CustomID string
	Part     Part
}

type batchBody struct {
	Messages    []chatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type batchItem struct {
	CustomID string    `json:"custom_id"`
	Body     batchBody `json:"body"`
}

// The batch API wants endpoint and model before requests, so field order
// here matters.
type batchSubmit struct {
	Endpoint string      `json:"endpoint"`
	Model    string      `json:"model"`
	Requests []batchItem `json:"requests"`
}

type batchObject struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	RequestCounts struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
	} `json:"request_counts"`
	Usage *struct {
		Cost float64 `json:"cost"`
	} `json:"usage"`
	Results []struct {
		CustomID string `json:"custom_id"`
		Response *struct {
			StatusCode int          `json:"status_code"`
			Body       chatResponse `json:"body"`
		} `json:"response"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"results"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// RequestSize estimates how many bytes a request adds to a submission.
func RequestSize(p Part) int { return len(p.System) + len(p.User) + 200 }

// Submit sends requests as one batch.
func (c *OpenRouterBatch) Submit(ctx context.Context, reqs []BatchRequest) (*Batch, error) {
	if c.Model == "" {
		return nil, errors.New("batches need a model: set openai.model in the config or GIRLPAGES_MODEL")
	}
	sub := batchSubmit{Endpoint: "/v1/chat/completions", Model: c.Model}
	for _, r := range reqs {
		sub.Requests = append(sub.Requests, batchItem{CustomID: r.CustomID, Body: batchBody{
			Messages:    []chatMessage{{Role: "system", Content: r.Part.System}, {Role: "user", Content: r.Part.User}},
			Temperature: c.Temperature,
			MaxTokens:   c.MaxTokens,
		}})
	}
	body, err := json.Marshal(sub)
	if err != nil {
		return nil, err
	}
	return c.call(ctx, http.MethodPost, strings.TrimRight(c.URL, "/"), body)
}

// Get fetches a batch's status, and its results once it has completed.
func (c *OpenRouterBatch) Get(ctx context.Context, id string) (*Batch, error) {
	return c.call(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/"+id, nil)
}

func (c *OpenRouterBatch) call(ctx context.Context, method, url string, body []byte) (*Batch, error) {
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	return retry(ctx, c.Retries, func() (*Batch, error) { return c.do(ctx, client, method, url, body) })
}

// retry calls fn until it succeeds, it fails in a way retrying won't fix,
// or retries run out.
func retry[T any](ctx context.Context, retries int, fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(backoff(attempt, lastErr)):
			case <-ctx.Done():
				return zero, ctx.Err()
			}
		}
		v, err := fn()
		if err == nil {
			return v, nil
		}
		lastErr = err
		var he *httpError
		if errors.As(err, &he) && !he.retryable() || ctx.Err() != nil {
			break
		}
	}
	return zero, lastErr
}

func (c *OpenRouterBatch) do(ctx context.Context, client *http.Client, method, url string, body []byte) (*Batch, error) {
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
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
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
	var obj batchObject
	jsonErr := json.Unmarshal(raw, &obj)
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		if jsonErr == nil && obj.Error != nil && obj.Error.Message != "" {
			msg = obj.Error.Message
		}
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		he := &httpError{status: resp.StatusCode, msg: msg}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s < 600 {
			he.retryAfter = time.Duration(s) * time.Second
		}
		return nil, he
	}
	if jsonErr != nil {
		return nil, fmt.Errorf("decoding batch: %w", jsonErr)
	}
	b := &Batch{ID: obj.ID, Status: obj.Status, Total: obj.RequestCounts.Total,
		Completed: obj.RequestCounts.Completed, Failed: obj.RequestCounts.Failed}
	if obj.Usage != nil {
		b.Cost = obj.Usage.Cost
	}
	if obj.Error != nil {
		b.Error = obj.Error.Message
	}
	if obj.Status == "completed" {
		b.Results = map[string]BatchResult{}
		for _, r := range obj.Results {
			b.Results[r.CustomID] = batchResult(r.Response, r.Error)
		}
	}
	return b, nil
}

func batchResult(resp *struct {
	StatusCode int          `json:"status_code"`
	Body       chatResponse `json:"body"`
}, e *struct {
	Message string `json:"message"`
}) BatchResult {
	switch {
	case e != nil:
		return BatchResult{Err: errors.New(e.Message)}
	case resp == nil:
		return BatchResult{Err: errors.New("no response")}
	case resp.StatusCode != http.StatusOK:
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if resp.Body.Error != nil {
			msg += ": " + resp.Body.Error.Message
		}
		return BatchResult{Err: errors.New(msg)}
	case len(resp.Body.Choices) == 0:
		return BatchResult{Err: errors.New("response had no choices")}
	case resp.Body.Choices[0].FinishReason == "length":
		return BatchResult{Err: errors.New("reply was cut off at the model's output limit")}
	}
	return BatchResult{Reply: resp.Body.Choices[0].Message.Content}
}

// BatchJob is a page to convert in a batch.
type BatchJob struct {
	// Key identifies the page to the caller, such as its content hash.
	Key   string
	Title string
	Text  string
	// Meta is anything else the caller wants back with the result. It's
	// saved with the batch; the text isn't.
	Meta json.RawMessage
}

// SavedJob is a job as saved with a submitted batch.
type SavedJob struct {
	Key   string          `json:"key"`
	Title string          `json:"title"`
	Parts int             `json:"parts"`
	Meta  json.RawMessage `json:"meta,omitempty"`
}

type batchState struct {
	// API is the BatchAPI's Kind; empty in older files, meaning openrouter.
	API       string    `json:"api,omitempty"`
	ID        string    `json:"id"`
	Model     string    `json:"model"`
	Submitted time.Time `json:"submitted"`
	// The planning options in force when it was submitted, so the parts
	// can be planned again exactly to match the replies.
	ChunkSize         int        `json:"chunk_size"`
	ExtraInstructions string     `json:"extra_instructions,omitempty"`
	PromptVersion     int        `json:"prompt_version"`
	Jobs              []SavedJob `json:"jobs"`
}

// Batcher converts pages in batches, keeping track of submitted batches
// in Dir so a later run can collect what an earlier one started.
type Batcher struct {
	Client  BatchAPI
	Dir     string
	Options Options
	// Live, if set, redoes parts that failed or dropped options in a batch.
	Live Backend
	// Jobs is how many finished pages are checked (and fixed up live) at
	// once.
	Jobs int
	// Poll is how long to wait between looks at an unfinished batch.
	Poll time.Duration
	// MaxBytes and MaxRequests split big submissions into several batches;
	// zero means the API's own limits.
	MaxBytes, MaxRequests int
	// Log, if set, gets progress messages.
	Log func(format string, a ...any)
}

func (b *Batcher) logf(format string, a ...any) {
	if b.Log != nil {
		b.Log(format, a...)
	}
}

// saved reads the saved batches this Batcher's API can collect. Batches
// sent to a different API (before the config changed) are left alone.
func (b *Batcher) saved() ([]*batchState, error) {
	files, err := filepath.Glob(filepath.Join(b.Dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*batchState
	other := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var st batchState
		if err := json.Unmarshal(raw, &st); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if api := cmpOr(st.API, "openrouter"); api != b.Client.Kind() {
			other++
			continue
		}
		out = append(out, &st)
	}
	if other > 0 {
		b.logf("🤷 %d batches were sent with another batch API; switch back to collect them, or delete them from %s\n", other, b.Dir)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Submitted.Before(out[j].Submitted) })
	return out, nil
}

func (b *Batcher) statePath(id string) string {
	return filepath.Join(b.Dir, strings.Map(func(r rune) rune {
		if r == '/' || r == os.PathSeparator {
			return '_'
		}
		return r
	}, id)+".json")
}

// Pending lists the keys of every job in a batch that hasn't been
// collected yet, so they aren't submitted twice.
func (b *Batcher) Pending() (map[string]bool, error) {
	states, err := b.saved()
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, st := range states {
		for _, j := range st.Jobs {
			keys[j.Key] = true
		}
	}
	return keys, nil
}

// Submit plans the jobs and sends them, in as many batches as their size
// needs, saving each batch before returning. It returns the number of
// requests sent.
func (b *Batcher) Submit(ctx context.Context, jobs []BatchJob) (int, error) {
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return 0, err
	}
	maxBytes, maxReqs := b.Client.Limits()
	if b.MaxBytes > 0 {
		maxBytes = b.MaxBytes
	}
	if b.MaxRequests > 0 {
		maxReqs = b.MaxRequests
	}
	sent := 0
	var reqs []BatchRequest
	st := b.newState()
	size := 0
	flush := func() error {
		if len(reqs) == 0 {
			return nil
		}
		batch, err := b.Client.Submit(ctx, reqs)
		if err != nil {
			return err
		}
		st.ID = batch.ID
		st.Submitted = time.Now().UTC()
		raw, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFile(b.statePath(batch.ID), raw); err != nil {
			return fmt.Errorf("batch %s was submitted but couldn't be saved, so it can't be collected: %w", batch.ID, err)
		}
		b.logf("📦 sent batch %s: %d pages in %d requests\n", batch.ID, len(st.Jobs), len(reqs))
		sent += len(reqs)
		reqs, st, size = nil, b.newState(), 0
		return nil
	}
	for _, j := range jobs {
		parts := Plan(j.Title, j.Text, b.Options)
		n := 0
		for _, p := range parts {
			n += RequestSize(p)
		}
		if len(reqs) > 0 && (size+n > maxBytes || len(reqs)+len(parts) > maxReqs) {
			if err := flush(); err != nil {
				return sent, err
			}
		}
		ji := len(st.Jobs)
		st.Jobs = append(st.Jobs, SavedJob{Key: j.Key, Title: j.Title, Parts: len(parts), Meta: j.Meta})
		for pi, p := range parts {
			reqs = append(reqs, BatchRequest{CustomID: fmt.Sprintf("%d-%d", ji, pi), Part: p})
		}
		size += n
	}
	return sent, flush()
}

func (b *Batcher) newState() *batchState {
	return &batchState{API: b.Client.Kind(), Model: b.Client.ModelName(), ChunkSize: b.Options.ChunkSize,
		ExtraInstructions: b.Options.ExtraInstructions, PromptVersion: PromptVersion}
}

// Result is a page that came back from a batch.
type Result struct {
	Job    SavedJob
	Markup string
	Model  string
	// Err is set if the page couldn't be converted; it will be submitted
	// again next time.
	Err error
}

// CollectStats summarises a Collect.
type CollectStats struct {
	// Batches finished and still running.
	Finished, Running int
	// Pages converted, failed, and skipped because they changed.
	Pages, Failed, Changed int
	Cost                   float64
}

// Collect looks at every saved batch. Finished ones have their pages
// checked (redoing failed parts live, if there's a Live backend) and
// handed to done, then are forgotten. With wait, it keeps looking until
// every batch has finished. text supplies a saved job's current text, or
// false if the page has changed or gone since. done may be called from
// several goroutines at once.
func (b *Batcher) Collect(ctx context.Context, wait bool, text func(SavedJob) (string, bool), done func(Result)) (CollectStats, error) {
	var cs CollectStats
	states, err := b.saved()
	if err != nil {
		return cs, err
	}
	poll := b.Poll
	if poll <= 0 {
		poll = time.Minute
	}
	for _, st := range states {
		var batch *Batch
		last := ""
		for {
			batch, err = b.Client.Get(ctx, st.ID)
			if err != nil {
				return cs, fmt.Errorf("batch %s: %w", st.ID, err)
			}
			if batch.Finished() || !wait {
				break
			}
			if p := fmt.Sprintf("%d/%d", batch.Completed+batch.Failed, batch.Total); p != last {
				last = p
				b.logf("⏳ batch %s: %s requests done (%s)\n", st.ID, p, batch.Status)
			}
			select {
			case <-time.After(poll):
			case <-ctx.Done():
				return cs, ctx.Err()
			}
		}
		if !batch.Finished() {
			cs.Running++
			continue
		}
		cs.Finished++
		cs.Cost += batch.Cost
		if batch.Status != "completed" {
			msg := batch.Status
			if batch.Error != "" {
				msg += ": " + batch.Error
			}
			b.logf("💔 batch %s %s; its %d pages will be sent again\n", st.ID, msg, len(st.Jobs))
			for _, j := range st.Jobs {
				cs.Failed++
				done(Result{Job: j, Err: fmt.Errorf("batch %s", msg)})
			}
		} else {
			b.finish(ctx, st, batch, text, done, &cs)
			if ctx.Err() != nil {
				return cs, ctx.Err()
			}
		}
		if err := os.Remove(b.statePath(st.ID)); err != nil {
			return cs, err
		}
	}
	return cs, nil
}

func (b *Batcher) finish(ctx context.Context, st *batchState, batch *Batch, text func(SavedJob) (string, bool),
	done func(Result), cs *CollectStats) {
	opts := Options{ChunkSize: st.ChunkSize, ExtraInstructions: st.ExtraInstructions}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(b.Jobs, 1))
	for ji, j := range st.Jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			t, ok := text(j)
			var parts []Part
			if ok {
				parts = Plan(j.Title, t, opts)
			}
			if !ok || len(parts) != j.Parts {
				mu.Lock()
				cs.Changed++
				mu.Unlock()
				return
			}
			outs := make([]string, len(parts))
			var err error
			for pi, p := range parts {
				r := batch.Results[fmt.Sprintf("%d-%d", ji, pi)]
				switch {
				case r.Err == nil && r.Reply != "":
					outs[pi], err = FinishPart(ctx, b.Live, p, r.Reply)
				case b.Live != nil:
					outs[pi], err = ConvertPart(ctx, b.Live, p)
				case r.Err != nil:
					err = r.Err
				default:
					err = errors.New("the batch had no reply for it")
				}
				if err != nil {
					if len(parts) > 1 {
						err = fmt.Errorf("part %d/%d: %w", pi+1, len(parts), err)
					}
					break
				}
			}
			res := Result{Job: j, Model: st.Model, Err: err}
			if err == nil {
				res.Markup = Join(outs)
			}
			mu.Lock()
			if err != nil {
				cs.Failed++
			} else {
				cs.Pages++
			}
			mu.Unlock()
			done(res)
		}()
	}
	wg.Wait()
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
