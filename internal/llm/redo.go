// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// What's done with a batch's parts that failed, or whose replies dropped
// options (Batcher.Repair).
const (
	// RepairBatch asks them again in the next batch, at batch price: a page
	// whose reply dropped options is kept as it is meanwhile, and replaced
	// if the next reply is better; one with a part that failed waits.
	RepairBatch = "batch"
	// RepairLive asks them again of the live model, at full price, there
	// and then.
	RepairLive = "live"
	// RepairNone keeps replies that dropped options as they are; a page
	// with a part that failed goes in the next batch whole.
	RepairNone = "none"
)

// redoTries is how many times a part that failed is asked again before its
// page is sent whole.
const redoTries = 3

// SavedRedo is a page with parts to ask again in the next batch.
type SavedRedo struct {
	Job           SavedJob `json:"job"`
	Model         string   `json:"model"`
	PromptVersion int      `json:"prompt_version"`
	// Outs are its parts as they are: "" for one that failed.
	Outs  []string   `json:"outs"`
	Parts []RedoPart `json:"parts"`
	// Tries is how many times its failed parts have been asked again.
	Tries int `json:"tries,omitempty"`
}

// RedoPart is a part to ask again: as it was asked, or with the options its
// reply dropped (Missing).
type RedoPart struct {
	Part    int      `json:"part"`
	Prompt  Part     `json:"prompt"`
	Missing []string `json:"missing,omitempty"`
}

func (b *Batcher) repair() string {
	if b.Repair == "" {
		return RepairBatch
	}
	return b.Repair
}

func (b *Batcher) redoDir() string { return filepath.Join(b.Dir, "redo") }

func hashKey(k string) string {
	h := sha256.Sum256([]byte(k))
	return hex.EncodeToString(h[:16])
}

func (b *Batcher) redoPath(r *SavedRedo) string {
	return filepath.Join(b.redoDir(), hashKey(PendingKey(r.Job.Key, r.Job.Variant))+".json")
}

// queueRedo saves a page's parts to ask again in the next batch.
func (b *Batcher) queueRedo(r *SavedRedo) error {
	if err := os.MkdirAll(b.redoDir(), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeFile(b.redoPath(r), raw)
}

// redos are the pages with parts to ask again.
func (b *Batcher) redos() ([]*SavedRedo, []string, error) {
	files, err := filepath.Glob(filepath.Join(b.redoDir(), "*.json"))
	if err != nil {
		return nil, nil, err
	}
	var out []*SavedRedo
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, nil, err
		}
		var r SavedRedo
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, &r)
	}
	return out, files, nil
}

// HasRedos reports whether there are parts waiting for the next batch.
func (b *Batcher) HasRedos() bool {
	files, _ := filepath.Glob(filepath.Join(b.redoDir(), "*.json"))
	return len(files) > 0
}

// submitRedos sends the parts waiting to be asked again, in batches of
// their own (ids r<page>-<n>), returning how many requests went.
func (b *Batcher) submitRedos(ctx context.Context, maxBytes, maxReqs int) (int, error) {
	redos, files, err := b.redos()
	if err != nil || len(redos) == 0 {
		return 0, err
	}
	sent := 0
	var reqs []BatchRequest
	var st *batchState
	var done []string // the files of the redos in st
	size := 0
	flush := func() error {
		if len(reqs) == 0 {
			return nil
		}
		batch, err := b.Client.Submit(ctx, reqs)
		if err != nil {
			return err
		}
		st.ID, st.Submitted = batch.ID, time.Now().UTC()
		raw, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFile(b.statePath(batch.ID), raw); err != nil {
			return fmt.Errorf("batch %s was submitted but couldn't be saved, so it can't be collected: %w", batch.ID, err)
		}
		for _, f := range done {
			os.Remove(f)
		}
		b.logf("📦 sent batch %s: %d parts of %d pages asked again\n", batch.ID, len(reqs), len(st.Redos))
		sent += len(reqs)
		reqs, st, size, done = nil, nil, 0, nil
		return nil
	}
	for i, r := range redos {
		if st == nil {
			st = b.newState()
		}
		var mine []BatchRequest
		n := 0
		for k, p := range r.Parts {
			req := BatchRequest{CustomID: fmt.Sprintf("r%d-%d", len(st.Redos), k), Part: p.Prompt}
			sz, err := b.Client.RequestBytes(req)
			if err != nil {
				return sent, err
			}
			mine, n = append(mine, req), n+sz
		}
		if len(reqs) > 0 && (size+n > maxBytes || len(reqs)+len(mine) > maxReqs) {
			if err := flush(); err != nil {
				return sent, err
			}
			st = b.newState()
			for k := range mine {
				mine[k].CustomID = fmt.Sprintf("r%d-%d", 0, k)
			}
		}
		st.Redos = append(st.Redos, *r)
		done = append(done, files[i])
		reqs, size = append(reqs, mine...), size+n
	}
	return sent, flush()
}

// finishRedos takes what came back for a batch's parts asked again: a page
// with a part that failed is done once it's had them all, and one whose
// reply dropped options is done again if a reply's better.
func (b *Batcher) finishRedos(st *batchState, batch *Batch, done func(Result), cs *CollectStats) {
	for ri := range st.Redos {
		r := st.Redos[ri]
		better := false
		var still []RedoPart // failed parts that failed again
		for k, p := range r.Parts {
			res := batch.Results[fmt.Sprintf("r%d-%d", ri, k)]
			out := ""
			if res.Err == nil {
				out = Clean(res.Reply)
			}
			switch {
			case r.Outs[p.Part] == "" && out != "":
				r.Outs[p.Part] = out
				better = true
			case r.Outs[p.Part] == "":
				still = append(still, p)
			case out != "" && len(Missing(p.Prompt.Kind, p.Prompt.Source, out)) < len(p.Missing):
				r.Outs[p.Part] = out
				better = true
			}
		}
		res := Result{Job: r.Job, Model: r.Model, PromptVersion: r.PromptVersion}
		switch {
		case len(still) > 0 && r.Tries+1 < redoTries:
			r.Parts, r.Tries = still, r.Tries+1
			if err := b.queueRedo(&r); err != nil {
				res.Err = err
				cs.Failed++
				done(res)
			}
		case len(still) > 0:
			res.Err = fmt.Errorf("part %d failed %d times", still[0].Part+1, redoTries)
			cs.Failed++
			done(res)
		case better:
			res.Markup = Join(r.Outs)
			cs.Pages++
			done(res)
		}
	}
}

// redoKeys are the pending keys of pages with parts waiting to be asked
// again, so they aren't sent whole meanwhile.
func (b *Batcher) redoKeys(keys map[string]bool) error {
	redos, _, err := b.redos()
	if err != nil {
		return err
	}
	for _, r := range redos {
		keys[PendingKey(r.Job.Key, r.Job.Variant)] = true
	}
	return nil
}

func joinMissing(m []string) string { return strings.Join(short(m, 5), ", ") }
