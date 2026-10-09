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
	"syscall"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
)

// Options controls a bulk run.
type Options struct {
	Sections []string
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
}

// Stats summarises a run.
type Stats struct {
	Pages, Fresh, Linked, Converted, Failed, Pruned, Removed int
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

	full := len(o.Sections) == 0 && o.Limit == 0
	pages, err := pagesFor(ctx, o, dir)
	if err != nil {
		return st, err
	}
	st.Pages = len(pages)

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
					if _, e, fresh := caches.Lookup(p.Path); e != nil && fresh {
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
				if !o.Force && caches.HasObject(hash) {
					if !o.DryRun {
						if err := putEntry(dir, p, hash, manpage.HeaderTitle(text), nil); err != nil {
							fmt.Fprintf(log, "💔 %s: %v\n", p.Title(), err)
						}
					}
					mu.Lock()
					st.Linked++
					mu.Unlock()
					continue
				}
				mu.Lock()
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
			logf("✨ would girlify %s", j.pages[0].Title())
			if len(j.pages) > 1 {
				logf(" (+%d aliases)", len(j.pages)-1)
			}
			logf("\n")
		}
		logf("%d pages: %d up to date, %d linked to existing conversions, %d unrenderable, %d to convert (%d unique)\n",
			st.Pages, st.Fresh, st.Linked, st.Skipped, countPages(byHash), len(order))
		return st, nil
	}

	if len(order) > 0 {
		logf("💅 girlifying %d pages (%d unique) with %s%s, %d at a time\n",
			countPages(byHash), len(order), b.Name(), modelSuffix(b), max(o.Jobs, 1))
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
				lo := o.LLM
				lo.Parallel = 1
				lo.Progress = nil
				markup, err := llm.Convert(ctx, b, j.name(), j.text, lo)
				if err == nil {
					err = dir.PutObject(j.hash, markup)
				}
				if err == nil {
					for _, p := range j.pages {
						if perr := putEntry(dir, p, j.hash, j.title, b); perr != nil && err == nil {
							err = perr
						}
					}
				}
				n := done.Add(1)
				mu.Lock()
				if err != nil {
					st.Failed++
					if ctx.Err() == nil {
						fmt.Fprintf(log, "[%*d/%d] 💔 %s: %v\n", width(total), n, total, j.pages[0].Title(), err)
					}
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
	if full && !o.Update {
		if f, err := os.Create(dir.MarkerPath()); err == nil {
			f.Close()
		}
	}
	logf("✨ done: %d pages, %d already cute, %d linked, %d girlified, %d skipped, %d failed",
		st.Pages, st.Fresh, st.Linked, st.Converted, st.Skipped, st.Failed)
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
	if o.Limit > 0 && len(pages) > o.Limit {
		pages = pages[:o.Limit]
	}
	return pages, nil
}

func putEntry(dir cache.Dir, p manpage.Page, hash, title string, b llm.Backend) error {
	e := &cache.Entry{
		Source:        p.Path,
		Name:          p.Name,
		Section:       p.Section,
		Title:         title,
		Hash:          hash,
		PromptVersion: llm.PromptVersion,
		Generated:     time.Now().UTC().Truncate(time.Second),
	}
	if b != nil {
		e.Backend, e.Model = b.Name(), b.Model()
	} else if old, _ := dir.Entry(p.Path); old != nil && old.Hash == hash {
		e.Backend, e.Model, e.PromptVersion, e.Generated = old.Backend, old.Model, old.PromptVersion, old.Generated
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
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
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
