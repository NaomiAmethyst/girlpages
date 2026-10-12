// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package rfc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/crawl"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// DefaultURL is the RFC Editor's site.
const DefaultURL = "https://www.rfc-editor.org/"

// Options controls a Crawl.
type Options struct {
	// URL is where rfc-index.xml and rfc/rfcN.txt are (DefaultURL if "").
	URL     string
	Fetcher *crawl.Fetcher
	// Numbers, if any, are the only RFCs crawled.
	Numbers map[int]bool
	Limit   int
	Jobs    int
	// Again crawls RFCs an earlier crawl did.
	Again  bool
	DryRun bool
	// State is the file that remembers what's been crawled.
	State string
	Log   io.Writer
	Quiet bool
}

// Stats summarises a Crawl.
type Stats struct {
	Listed, Done, Fetched, Failed, New int
	Bytes                              int64
}

// Crawl adds RFCs from the RFC Editor to the store: each one's text,
// cleaned and cut into sections, as its original, with its title as its
// description.
func Crawl(ctx context.Context, s *repo.Store, o Options) (Stats, error) {
	var st Stats
	if o.URL == "" {
		o.URL = DefaultURL
	}
	if !strings.HasSuffix(o.URL, "/") {
		o.URL += "/"
	}
	if o.Fetcher == nil {
		o.Fetcher = &crawl.Fetcher{Retries: 2}
	}
	if o.Jobs < 1 {
		o.Jobs = 4
	}
	logf := func(format string, a ...any) {
		if !o.Quiet && o.Log != nil {
			fmt.Fprintf(o.Log, format, a...)
		}
	}
	r, err := o.Fetcher.Open(ctx, o.URL+"rfc-index.xml")
	if err != nil {
		return st, err
	}
	entries, err := ParseIndex(r)
	r.Close()
	if err != nil {
		return st, err
	}
	done, err := loadState(o.State)
	if err != nil {
		return st, err
	}
	var todo []Entry
	for _, e := range entries {
		if !e.HasText() || (len(o.Numbers) > 0 && !o.Numbers[e.Number]) {
			continue
		}
		st.Listed++
		if done[e.Number] && !o.Again {
			st.Done++
			continue
		}
		todo = append(todo, e)
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].Number < todo[j].Number })
	if o.Limit > 0 && len(todo) > o.Limit {
		todo = todo[:o.Limit]
	}
	logf("📚 %d RFCs with text, %d done before; %d to fetch\n", st.Listed, st.Done, len(todo))
	if o.DryRun {
		for _, e := range todo {
			logf("✨ would fetch RFC %d: %s\n", e.Number, e.Title)
		}
		return st, nil
	}

	var mu sync.Mutex
	var next int
	save := func() error { return saveState(o.State, done) }
	worker := func() error {
		for {
			mu.Lock()
			if next >= len(todo) || ctx.Err() != nil {
				mu.Unlock()
				return ctx.Err()
			}
			e := todo[next]
			next++
			mu.Unlock()
			isNew, n, err := one(ctx, s, o, e)
			mu.Lock()
			st.Bytes += n
			if err != nil {
				st.Failed++
				mu.Unlock()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				logf("💔 RFC %d: %v\n", e.Number, err)
				continue
			}
			st.Fetched++
			if isNew {
				st.New++
			}
			done[e.Number] = true
			if st.Fetched%50 == 0 {
				logf("[%d/%d] 📄 up to RFC %d\n", st.Fetched+st.Failed, len(todo), e.Number)
				if err := save(); err != nil {
					mu.Unlock()
					return err
				}
			}
			mu.Unlock()
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, o.Jobs)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = worker()
		}(i)
	}
	wg.Wait()
	if err := save(); err != nil {
		return st, err
	}
	return st, errors.Join(errs...)
}

// one fetches an RFC and adds it, reporting whether it was new to the
// store and how big it was.
func one(ctx context.Context, s *repo.Store, o Options, e Entry) (bool, int64, error) {
	r, err := o.Fetcher.Open(ctx, fmt.Sprintf("%srfc/rfc%d.txt", o.URL, e.Number))
	if err != nil {
		return false, 0, err
	}
	b, err := io.ReadAll(io.LimitReader(r, 32<<20))
	r.Close()
	if err != nil {
		return false, 0, err
	}
	source := repo.Hash(b)
	text := Clean(string(b))
	if strings.TrimSpace(text) == "" {
		return false, int64(len(b)), errors.New("it's empty")
	}
	v := Variant(source, e.Number, text)
	info, err := s.PutVariant(v)
	if err != nil {
		return false, int64(len(b)), err
	}
	unlock, err := s.Lock()
	if err != nil {
		return false, int64(len(b)), err
	}
	defer unlock()
	isNew := false
	err = s.UpdateListing(e.Name(), Section, func(l *repo.Listing) error {
		ver := l.Find(source)
		if ver == nil {
			isNew = true
			l.Versions = append(l.Versions, repo.Version{Source: source, Added: time.Now().UTC().Truncate(time.Second),
				Variants: map[string]repo.VariantInfo{}})
			ver = &l.Versions[len(l.Versions)-1]
		}
		ver.Title = v.Title
		ver.Description = e.Title
		ver.Variants[repo.VariantMan] = info
		pkg := e.Package()
		for _, p := range ver.Packages {
			if p == pkg {
				return nil
			}
		}
		ver.Packages = append(ver.Packages, pkg)
		return nil
	})
	return isNew, int64(len(b)), err
}

func loadState(path string) (map[int]bool, error) {
	done := map[int]bool{}
	if path == "" {
		return done, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return done, nil
	}
	if err != nil {
		return nil, err
	}
	var ns []int
	if err := json.Unmarshal(b, &ns); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, n := range ns {
		done[n] = true
	}
	return done, nil
}

func saveState(path string, done map[int]bool) error {
	if path == "" {
		return nil
	}
	var ns []int
	for n := range done {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	b, err := json.Marshal(ns)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return repo.WriteFile(path, b)
}
