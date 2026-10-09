// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package core fetches a girlified page: from the cache if it's there,
// otherwise by converting it and caching the result. The command line, the
// HTML output and the web server all go through here.
package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/config"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
)

// Girl holds what's needed to fetch pages.
type Girl struct {
	Cfg    *config.Config
	Caches cache.Set
	Man    manpage.Options
	// Limit, if positive, caps LLM requests in flight across every
	// conversion (the server sets it so visitors can't stampede the model).
	Limit int
	// NewBackend builds the backend; it defaults to NewBackend.
	NewBackend func(*config.Config) (llm.Backend, error)

	mu      sync.Mutex
	backend llm.Backend
}

// GetOptions controls a fetch.
type GetOptions struct {
	// Refresh converts again even if the page is cached.
	Refresh bool
	// CachedOnly never calls the model.
	CachedOnly bool
	// Started, if set, is called just before a conversion begins and
	// returns a function called when it ends.
	Started func(b llm.Backend) (done func())
	// Progress is passed to llm.Convert.
	Progress func(done, total int)
}

// Result is a fetched page.
type Result struct {
	Markup string
	// Title is the page's own title, such as "LS(1)", if known.
	Title string
	// Stale means the source changed after this was converted.
	Stale bool
	// Entry is the cache record, when there is one.
	Entry *cache.Entry
}

// ErrNotCached is returned for uncached pages with CachedOnly set.
var ErrNotCached = errors.New("it isn't girlified yet and converting is switched off")

// Backend returns the (shared) LLM backend.
func (g *Girl) Backend() (llm.Backend, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.backend != nil {
		return g.backend, nil
	}
	nb := g.NewBackend
	if nb == nil {
		nb = NewBackend
	}
	b, err := nb(g.Cfg)
	if err != nil {
		return nil, err
	}
	if g.Limit > 0 {
		b = llm.Limit(b, g.Limit)
	}
	g.backend = b
	return b, nil
}

// Cached returns the page from the cache without converting anything.
func (g *Girl) Cached(p manpage.Page) (Result, bool) {
	markup, e, fresh := g.Caches.Lookup(p.Path)
	if markup == "" {
		return Result{}, false
	}
	return Result{Markup: markup, Title: e.Title, Stale: !fresh, Entry: e}, true
}

// Get fetches a page, converting it if necessary.
func (g *Girl) Get(ctx context.Context, p manpage.Page, o GetOptions) (Result, error) {
	if !o.Refresh {
		if r, ok := g.Cached(p); ok {
			return r, nil
		}
	}
	text, err := manpage.Text(ctx, p.Path, g.Man)
	if err != nil {
		return Result{}, err
	}
	hash := cache.Hash(text)
	title := manpage.HeaderTitle(text)
	dir := g.Caches.Writable()
	entry := &cache.Entry{Source: p.Path, Name: p.Name, Section: p.Section, Title: title, Hash: hash,
		PromptVersion: llm.PromptVersion, Generated: time.Now().UTC().Truncate(time.Second)}
	if !o.Refresh {
		if markup, ok := g.Caches.Object(hash); ok {
			// Same text as a page that's already done (an alias, usually).
			if entry.Stamp() == nil {
				_ = dir.PutEntry(entry)
			}
			return Result{Markup: markup, Title: title, Entry: entry}, nil
		}
	}
	if o.CachedOnly {
		return Result{}, ErrNotCached
	}
	b, err := g.Backend()
	if err != nil {
		return Result{}, err
	}
	if o.Started != nil {
		done := o.Started(b)
		defer done()
	}
	markup, err := llm.Convert(ctx, b, DisplayTitle(p, title), text, llm.Options{
		ChunkSize:         g.Cfg.ChunkSize,
		Parallel:          g.Cfg.Jobs,
		ExtraInstructions: g.Cfg.ExtraInstructions,
		Progress:          o.Progress,
	})
	if err != nil {
		return Result{}, err
	}
	entry.Backend, entry.Model = b.Name(), b.Model()
	if err := dir.PutObject(hash, markup); err != nil {
		fmt.Fprintf(os.Stderr, "girl: couldn't save to cache: %v\n", err)
	} else if err := entry.Stamp(); err == nil {
		if err := dir.PutEntry(entry); err != nil {
			fmt.Fprintf(os.Stderr, "girl: couldn't save to cache: %v\n", err)
		}
	}
	return Result{Markup: markup, Title: title, Entry: entry}, nil
}

// DisplayTitle is the page's own title if known, else NAME(SECTION).
func DisplayTitle(p manpage.Page, title string) string {
	if title != "" {
		return title
	}
	if p.Section == "" {
		return strings.ToUpper(p.Name)
	}
	return strings.ToUpper(p.Name) + "(" + p.Section + ")"
}

// NewBackend builds the configured LLM backend.
func NewBackend(cfg *config.Config) (llm.Backend, error) {
	name, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	switch name {
	case config.BackendClaude:
		return &llm.Claude{Command: cfg.Claude.Command, ModelName: cfg.Claude.Model, Args: cfg.Claude.Args,
			Timeout: cfg.Claude.Timeout.Duration}, nil
	default:
		key, err := cfg.OpenAIKey()
		if err != nil {
			return nil, err
		}
		return &llm.OpenAI{
			BaseURL:     cfg.OpenAI.BaseURL,
			APIKey:      key,
			ModelName:   cfg.OpenAI.Model,
			Temperature: cfg.OpenAI.Temperature,
			MaxTokens:   cfg.OpenAI.MaxTokens,
			Headers:     cfg.OpenAI.Headers,
			Client:      &http.Client{Timeout: cfg.OpenAI.Timeout.Duration},
			Retries:     3,
		}, nil
	}
}

// CacheSet builds the cache list from the config.
func CacheSet(cfg *config.Config) cache.Set {
	var s cache.Set
	for _, d := range cfg.ReadCacheDirs() {
		s = append(s, cache.Dir(d))
	}
	return s
}
