// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package bulk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// batchMeta is saved with each page sent in a batch: the files it's for.
type batchMeta struct {
	Pages []manpage.Page `json:"pages"`
	Title string         `json:"title,omitempty"` // the page's own, like TRUE(1)
}

// BatchDir is where a cache keeps track of batches it has sent.
func BatchDir(dir cache.Dir) string { return filepath.Join(string(dir), "batches") }

// collect gathers pages from finished batches into the cache, waiting for
// unfinished ones if wait is set.
func collect(ctx context.Context, o Options, dir cache.Dir, wait bool, st *Stats, logf func(string, ...any), log io.Writer) error {
	var mu sync.Mutex
	got := 0
	cs, err := o.Batch.Collect(ctx, wait, func(j llm.SavedJob) (string, bool) {
		var m batchMeta
		if json.Unmarshal(j.Meta, &m) != nil {
			return "", false
		}
		// The page may have changed since; only a page with the same text
		// as was sent matches the reply.
		for _, p := range m.Pages {
			if text, err := manpage.Text(ctx, p.Path, o.Man); err == nil && cache.Hash(text) == j.Key {
				return text, true
			}
		}
		return "", false
	}, func(r llm.Result) {
		mu.Lock()
		defer mu.Unlock()
		if r.Err != nil {
			st.Failed++
			fmt.Fprintf(log, "💔 %s: %v (it'll go in the next batch)\n", r.Job.Title, r.Err)
			return
		}
		var m batchMeta
		_ = json.Unmarshal(r.Job.Meta, &m)
		if err := dir.PutObject(r.Job.Key, r.Markup); err != nil {
			st.Failed++
			fmt.Fprintf(log, "💔 %s: %v\n", r.Job.Title, err)
			return
		}
		for _, p := range m.Pages {
			if err := putEntry(dir, p, r.Job.Key, m.Title, maker{Backend: "openai", Model: r.Model, PromptVersion: r.PromptVersion}); err != nil {
				continue // gone since; the next run sorts it out
			}
			st.Converted++
		}
		got++
		logf("💖 %s (from a batch)\n", r.Job.Title)
	})
	if cs.Finished > 0 {
		logf("📬 %d finished batches: %d pages girlified", cs.Finished, got)
		if cs.Cost > 0 {
			logf(", costing $%.4f", cs.Cost)
		}
		logf("\n")
	}
	if cs.Changed > 0 {
		logf("🔄 %d pages changed since they were sent; they'll go in the next batch\n", cs.Changed)
	}
	if cs.Running > 0 && !wait {
		logf("⏳ %d batches still running\n", cs.Running)
	}
	return err
}

// runBatch takes what the repository has, sends the rest off in batches,
// and (unless NoWait) waits for them.
func runBatch(ctx context.Context, o Options, dir cache.Dir, byHash map[string]*job, order []string, st *Stats,
	logf func(string, ...any), log io.Writer) error {
	if len(order) == 0 {
		return nil
	}
	todo := order
	if o.Fetch != nil {
		todo = nil
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, max(o.Jobs, 1))
		for _, h := range order {
			j := byHash[h]
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				markup, gen, err := o.Fetch(ctx, j.pages[0])
				if err == nil && o.Reprompt && gen.PromptVersion < llm.PromptVersion {
					err = fmt.Errorf("its conversion there is from an older prompt: %w", repo.ErrNotFound)
				}
				if err == nil {
					err = dir.PutObject(j.hash, markup)
				}
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if !errors.Is(err, repo.ErrNotFound) {
						fmt.Fprintf(log, "girl: couldn't fetch %s from the repo: %v\n", j.name(), err)
					}
					todo = append(todo, h)
					return
				}
				for _, p := range j.pages {
					if putEntry(dir, p, j.hash, j.title, maker{Backend: "repo", Model: gen.Model, PromptVersion: gen.PromptVersion, Generated: gen.Generated}) == nil {
						st.Fetched++
					}
				}
				logf("📦 %s (from the repo)\n", j.name())
			}()
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}
		// Keep man's section order.
		keep := map[string]bool{}
		for _, h := range todo {
			keep[h] = true
		}
		todo = todo[:0]
		for _, h := range order {
			if keep[h] {
				todo = append(todo, h)
			}
		}
	}
	if len(todo) == 0 {
		return nil
	}
	jobs := make([]llm.BatchJob, 0, len(todo))
	pages := 0
	for _, h := range todo {
		j := byHash[h]
		meta, err := json.Marshal(batchMeta{Pages: j.pages, Title: j.title})
		if err != nil {
			return err
		}
		jobs = append(jobs, llm.BatchJob{Key: h, Title: j.name(), Text: j.text, Meta: meta})
		pages += len(j.pages)
	}
	logf("💌 sending %d pages (%d unique) to %s in batches: about half price, back within a day\n",
		pages, len(jobs), o.Batch.Client.ModelName())
	if _, err := o.Batch.Submit(ctx, jobs); err != nil {
		return err
	}
	if o.NoWait {
		st.Batched += pages
		logf("💤 not waiting; the next girl --bulk or --update collects them\n")
		return nil
	}
	logf("⏳ waiting for them (ctrl-c is fine: they keep going, and the next girl --bulk collects them)\n")
	if err := collect(ctx, o, dir, true, st, logf, log); err != nil {
		return err
	}
	return nil
}
