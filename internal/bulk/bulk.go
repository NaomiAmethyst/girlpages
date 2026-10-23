// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package bulk converts every man page on the system, incrementally.
package bulk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/flock"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// Options controls a bulk run.
type Options struct {
	Sections []string
	// Names limits the run to pages with these names.
	Names []string
	// Update only refreshes pages that are already cached, unless a full
	// bulk run has completed before, in which case new pages are converted
	// too. It is what the package-manager hooks and timers run.
	Update bool
	Force  bool
	DryRun bool
	Prune  bool
	Quiet  bool
	Jobs   int
	Limit  int
	Man    manpage.Options
	LLM    llm.Options
	// Fetch, if set, is tried before the model: it returns a page's
	// conversion from a girlpages repository, with what made it.
	Fetch func(ctx context.Context, p manpage.Page) (markup string, made *repo.Generator, err error)
	// Batch, if set, converts pages in batches instead of one at a time.
	// Batches submitted by earlier runs are collected first.
	Batch *llm.Batcher
	// NoWait leaves submitted batches running instead of waiting for them;
	// a later run collects them.
	NoWait bool
	// FetchOnly is for runs that only take what Fetch has (girl --import):
	// pages it doesn't have are counted, not failures.
	FetchOnly bool
	// Reprompt converts again pages made with an older prompt
	// (llm.PromptVersion), and won't take such conversions from the cache
	// or Fetch.
	Reprompt bool
}

// Stats summarises a run.
type Stats struct {
	Pages, Fresh, Linked, Converted, Failed, Pruned, Removed int
	// Fetched pages came ready-made from a repository; NotInRepo pages
	// weren't there, in a FetchOnly run.
	Fetched, NotInRepo int
	// Batched pages were sent off in a batch; Pending ones were already in
	// a batch that hasn't finished.
	Batched, Pending int
	// Skipped pages couldn't be rendered by man at all (usually a broken
	// .so link in a package); they aren't counted as failures.
	Skipped int
}

type job struct {
	hash  string
	text  string
	title string
	pages []manpage.Page
}

// name is how the job's page is described to the model and in the log.
func (j *job) name() string {
	if j.title != "" {
		return j.title
	}
	return j.pages[0].Title()
}

// Run converts pages into the writable cache of caches.
func Run(ctx context.Context, o Options, caches cache.Set, b llm.Backend, log io.Writer) (Stats, error) {
	var st Stats
	dir := caches.Writable()
	if err := os.MkdirAll(string(dir), 0o755); err != nil {
		return st, err
	}
	unlock, err := lock(dir.LockPath())
	if err != nil {
		return st, err
	}
	defer unlock()

	logf := func(format string, a ...any) {
		if !o.Quiet {
			fmt.Fprintf(log, format, a...)
		}
	}

	full := len(o.Sections) == 0 && len(o.Names) == 0 && o.Limit == 0
	pending := map[string]bool{}
	if o.Batch != nil && !o.DryRun {
		// Whatever earlier runs sent off may be back by now.
		if err := collect(ctx, o, dir, false, &st, logf, log); err != nil {
			return st, err
		}
		if pending, err = o.Batch.Pending(); err != nil {
			return st, err
		}
	}
	pages, err := pagesFor(ctx, o, dir)
	if err != nil {
		return st, err
	}
	st.Pages = len(pages)
	// What made each conversion in the caches, so pages linked to one
	// record the right thing, and reprompting can tell the old ones.
	made := makers(caches)
	current := func(hash string) bool {
		m, ok := made[hash]
		return !o.Reprompt || ok && m.PromptVersion >= llm.PromptVersion
	}

	// Pass 1: work out what needs converting. Rendering is local and cheap
	// next to an LLM call, so use every CPU.
	var (
		mu     sync.Mutex
		byHash = map[string]*job{}
		order  []string
		wg     sync.WaitGroup
		next   atomic.Int64
	)
	workers := runtime.NumCPU()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(pages) || ctx.Err() != nil {
					return
				}
				p := pages[i]
				if !o.Force {
					if _, e, fresh := caches.Lookup(p.Path); e != nil && fresh && (!o.Reprompt || e.PromptVersion >= llm.PromptVersion) {
						mu.Lock()
						st.Fresh++
						mu.Unlock()
						continue
					}
				}
				text, err := manpage.Text(ctx, p.Path, o.Man)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					mu.Lock()
					st.Skipped++
					mu.Unlock()
					logf("🙈 skipping %s, man can't render it: %v\n", p.Title(), err)
					continue
				}
				hash := cache.Hash(text)
				if !o.Force && caches.HasObject(hash) && current(hash) {
					if !o.DryRun {
						if err := putEntry(dir, p, hash, manpage.HeaderTitle(text), made[hash]); err != nil {
							fmt.Fprintf(log, "💔 %s: %v\n", p.Title(), err)
						}
					}
					mu.Lock()
					st.Linked++
					mu.Unlock()
					continue
				}
				mu.Lock()
				if pending[hash] && !o.Force {
					st.Pending++
					mu.Unlock()
					continue
				}
				if j, ok := byHash[hash]; ok {
					j.pages = append(j.pages, p)
				} else {
					byHash[hash] = &job{hash: hash, text: text, title: manpage.HeaderTitle(text), pages: []manpage.Page{p}}
					order = append(order, hash)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return st, err
	}
	// Keep man's section order for the expensive part.
	rank := map[string]int{}
	for i, p := range pages {
		rank[p.Path] = i
	}
	sort.Slice(order, func(i, j int) bool {
		return rank[byHash[order[i]].pages[0].Path] < rank[byHash[order[j]].pages[0].Path]
	})

	if o.DryRun {
		for _, h := range order {
			j := byHash[h]
			if o.FetchOnly {
				logf("🔎 would look for %s in the repo", j.pages[0].Title())
			} else {
				logf("✨ would girlify %s", j.pages[0].Title())
			}
			if len(j.pages) > 1 {
				logf(" (+%d aliases)", len(j.pages)-1)
			}
			logf("\n")
		}
		verb := "to convert"
		if o.FetchOnly {
			verb = "to look for"
		}
		logf("%d pages: %d up to date, %d linked to existing conversions, %d unrenderable, %d %s (%d unique)",
			st.Pages, st.Fresh, st.Linked, st.Skipped, countPages(byHash), verb, len(order))
		if o.Batch != nil {
			logf(", in a batch")
		}
		logf("\n")
		return st, nil
	}

	if o.Batch != nil {
		if err := runBatch(ctx, o, dir, byHash, order, &st, logf, log); err != nil {
			return st, err
		}
		order = nil
	}

	if len(order) > 0 {
		with := "the repo"
		if b != nil {
			with = b.Name() + modelSuffix(b)
			if o.Fetch != nil {
				with = "the repo and " + with
			}
		}
		if o.FetchOnly {
			logf("📥 looking for %d pages (%d unique) in the repo, %d at a time\n", countPages(byHash), len(order), max(o.Jobs, 1))
		} else {
			logf("💅 girlifying %d pages (%d unique) with %s, %d at a time\n",
				countPages(byHash), len(order), with, max(o.Jobs, 1))
		}
	}

	// Pass 2: convert.
	queue := make(chan *job)
	var done atomic.Int64
	total := len(order)
	jobs := max(o.Jobs, 1)
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				start := time.Now()
				var markup string
				var err error
				var mk maker
				fetched, missing := false, false
				if o.Fetch != nil {
					var gen *repo.Generator
					if markup, gen, err = o.Fetch(ctx, j.pages[0]); err == nil && o.Reprompt && gen.PromptVersion < llm.PromptVersion {
						err = fmt.Errorf("its conversion there is from an older prompt: %w", repo.ErrNotFound)
					}
					if err == nil {
						fetched, mk = true, maker{Backend: "repo", Model: gen.Model, PromptVersion: gen.PromptVersion, Generated: gen.Generated}
					} else if errors.Is(err, repo.ErrNotFound) {
						missing = true
					} else {
						fmt.Fprintf(log, "girl: couldn't fetch %s from the repo: %v\n", j.name(), err)
					}
				}
				switch {
				case fetched:
					err = nil
				case b == nil:
					err = errors.New("it isn't in the repo and there's no model to ask")
				default:
					lo := o.LLM
					lo.Parallel = 1
					lo.Progress = nil
					markup, err = llm.Convert(ctx, b, j.name(), j.text, lo)
					mk = maker{Backend: b.Name(), Model: b.Model(), PromptVersion: llm.PromptVersion, Generated: now()}
				}
				if err == nil {
					err = dir.PutObject(j.hash, markup)
				}
				if err == nil {
					for _, p := range j.pages {
						if perr := putEntry(dir, p, j.hash, j.title, mk); perr != nil && err == nil {
							err = perr
						}
					}
				}
				n := done.Add(1)
				mu.Lock()
				if missing && b == nil && o.FetchOnly {
					st.NotInRepo += len(j.pages)
				} else if err != nil {
					st.Failed++
					if ctx.Err() == nil {
						fmt.Fprintf(log, "[%*d/%d] 💔 %s: %v\n", width(total), n, total, j.pages[0].Title(), err)
					}
				} else if fetched {
					st.Fetched += len(j.pages)
					logf("[%*d/%d] 📦 %s (from the repo)\n", width(total), n, total, j.pages[0].Title())
				} else {
					st.Converted += len(j.pages)
					logf("[%*d/%d] 💖 %s (%s)\n", width(total), n, total, j.pages[0].Title(), time.Since(start).Round(time.Second))
				}
				mu.Unlock()
			}
		}()
	}
	for _, h := range order {
		select {
		case queue <- byHash[h]:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(queue)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return st, err
	}

	if o.Prune && full {
		pruned, removed, err := prune(dir)
		st.Pruned, st.Removed = pruned, removed
		if err != nil {
			return st, err
		}
	}
	// An import isn't a full run: update runs mustn't start converting
	// everything new because of one.
	if full && !o.Update && !o.FetchOnly {
		if f, err := os.Create(dir.MarkerPath()); err == nil {
			f.Close()
		}
	}
	logf("✨ done: %d pages, %d already cute, %d linked, ", st.Pages, st.Fresh, st.Linked)
	if !o.FetchOnly {
		logf("%d girlified, ", st.Converted)
	}
	if o.Fetch != nil {
		logf("%d from the repo, ", st.Fetched)
	}
	if o.FetchOnly {
		logf("%d not in it, ", st.NotInRepo)
	}
	if o.Batch != nil {
		logf("%d in batches still running, ", st.Pending+st.Batched)
	}
	logf("%d skipped, %d failed", st.Skipped, st.Failed)
	if o.Prune && full {
		logf(", %d stale entries and %d orphaned conversions pruned", st.Pruned, st.Removed)
	}
	logf("\n")
	if st.Failed > 0 {
		return st, fmt.Errorf("%d pages failed", st.Failed)
	}
	return st, nil
}

// pagesFor picks the pages to look at. In update mode without a previous
// full run, that's just what's already in the cache.
func pagesFor(ctx context.Context, o Options, dir cache.Dir) ([]manpage.Page, error) {
	var pages []manpage.Page
	_, markerErr := os.Stat(dir.MarkerPath())
	if o.Update && markerErr != nil {
		err := dir.Entries(func(e *cache.Entry) error {
			if _, err := os.Stat(e.Source); err != nil {
				return nil
			}
			if manpage.SectionMatches(e.Section, o.Sections) {
				pages = append(pages, manpage.Page{Path: e.Source, Name: e.Name, Section: e.Section})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		dirs, err := manpage.ManPath(ctx, o.Man)
		if err != nil {
			return nil, err
		}
		pages, err = manpage.Enumerate(dirs, o.Sections)
		if err != nil {
			return nil, err
		}
	}
	if len(o.Names) > 0 {
		want := map[string]bool{}
		for _, n := range o.Names {
			want[n] = true
		}
		kept := pages[:0]
		for _, p := range pages {
			if want[p.Name] {
				kept = append(kept, p)
			}
		}
		pages = kept
	}
	if o.Limit > 0 && len(pages) > o.Limit {
		pages = pages[:o.Limit]
	}
	return pages, nil
}

// maker is what made a conversion.
type maker struct {
	Backend, Model string
	PromptVersion  int
	Generated      time.Time
}

func now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// makers reads what made each conversion in the caches, by hash.
func makers(caches cache.Set) map[string]maker {
	out := map[string]maker{}
	for _, d := range caches {
		_ = d.Entries(func(e *cache.Entry) error {
			if m, ok := out[e.Hash]; !ok || e.PromptVersion > m.PromptVersion {
				out[e.Hash] = maker{e.Backend, e.Model, e.PromptVersion, e.Generated}
			}
			return nil
		})
	}
	return out
}

// putEntry records that p converts to hash, which m made.
func putEntry(dir cache.Dir, p manpage.Page, hash, title string, m maker) error {
	if m.Generated.IsZero() {
		m.Generated = now()
	}
	e := &cache.Entry{
		Source:        p.Path,
		Name:          p.Name,
		Section:       p.Section,
		Title:         title,
		Hash:          hash,
		Backend:       m.Backend,
		Model:         m.Model,
		PromptVersion: m.PromptVersion,
		Generated:     m.Generated,
	}
	if err := e.Stamp(); err != nil {
		return err
	}
	return dir.PutEntry(e)
}

// prune drops index entries whose source has gone (a package was removed)
// and then any conversions nothing refers to.
func prune(dir cache.Dir) (int, int, error) {
	var gone []string
	err := dir.Entries(func(e *cache.Entry) error {
		if _, err := os.Stat(e.Source); errors.Is(err, os.ErrNotExist) {
			gone = append(gone, e.Source)
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	for _, s := range gone {
		if err := dir.RemoveEntry(s); err != nil {
			return 0, 0, err
		}
	}
	n, err := dir.GC()
	return len(gone), n, err
}

func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := flock.TryLock(f); err != nil {
		f.Close()
		if errors.Is(err, flock.ErrLocked) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() {
		flock.Unlock(f)
		f.Close()
	}, nil
}

// ErrLocked means another bulk run is using the cache.
var ErrLocked = errors.New("another bulk run is already in progress")

func countPages(m map[string]*job) int {
	n := 0
	for _, j := range m {
		n += len(j.pages)
	}
	return n
}

func modelSuffix(b llm.Backend) string {
	if m := b.Model(); m != "" {
		return " (" + m + ")"
	}
	return ""
}

func width(n int) int { return len(fmt.Sprint(n)) }
