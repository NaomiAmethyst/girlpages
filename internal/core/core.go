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
	"github.com/NaomiAmethyst/girlpages/internal/repo"
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
	// Repo, if set, is asked for pages before the model is.
	Repo *repo.Client

	mu      sync.Mutex
	backend llm.Backend
}

// GetOptions controls a fetch.
type GetOptions struct {
	// Refresh converts again even if the page is cached.
	Refresh bool
	// CachedOnly never calls the model.
	CachedOnly bool
	// NoCache always calls the model, looking in neither the cache nor the
	// repository, and saves nothing.
	NoCache bool
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
	reuse := !o.Refresh && !o.NoCache
	if reuse {
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
	if reuse {
		if markup, ok := g.Caches.Object(hash); ok {
			// Same text as a page that's already done (an alias, usually).
			if entry.Stamp() == nil {
				_ = dir.PutEntry(entry)
			}
			return Result{Markup: markup, Title: title, Entry: entry}, nil
		}
	}
	if reuse {
		if markup, gen, err := g.FromRepo(ctx, p); err == nil {
			// Record the prompt that made it, not ours (0 if it doesn't say).
			entry.Backend, entry.Model, entry.PromptVersion = BackendRepo, gen.Model, gen.PromptVersion
			if err := dir.PutObject(hash, markup); err != nil {
				fmt.Fprintf(os.Stderr, "girl: couldn't save to cache: %v\n", err)
			} else if err := entry.Stamp(); err == nil {
				_ = dir.PutEntry(entry)
			}
			return Result{Markup: markup, Title: title, Entry: entry}, nil
		} else if !errors.Is(err, repo.ErrNotFound) && !errors.Is(err, errNoRepo) {
			fmt.Fprintf(os.Stderr, "girl: couldn't fetch from the repo: %v\n", err)
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
	if o.NoCache {
		return Result{Markup: markup, Title: title, Entry: entry}, nil
	}
	if err := dir.PutObject(hash, markup); err != nil {
		fmt.Fprintf(os.Stderr, "girl: couldn't save to cache: %v\n", err)
	} else if err := entry.Stamp(); err == nil {
		if err := dir.PutEntry(entry); err != nil {
			fmt.Fprintf(os.Stderr, "girl: couldn't save to cache: %v\n", err)
		}
	}
	return Result{Markup: markup, Title: title, Entry: entry}, nil
}

// BackendRepo is the backend recorded for pages fetched from a repository.
const BackendRepo = "repo"

var errNoRepo = errors.New("no repo configured")

// FromRepo fetches a page's conversion from the repository, if there is
// one. Pages are looked up by the hash of their roff source, so it only
// finds conversions of exactly the version installed here. Custom
// extra_instructions mean the repository's pages aren't wanted.
func (g *Girl) FromRepo(ctx context.Context, p manpage.Page) (string, *repo.Generator, error) {
	if g.Repo == nil || g.Cfg.ExtraInstructions != "" {
		return "", nil, errNoRepo
	}
	return FetchFrom(ctx, g.Repo, p)
}

// FetchFrom fetches a page's conversion from a repository, whatever the
// configuration says.
func FetchFrom(ctx context.Context, c *repo.Client, p manpage.Page) (string, *repo.Generator, error) {
	src, err := manpage.SourceHash(p.Path, nil)
	if err != nil {
		return "", nil, err
	}
	v, err := c.Variant(ctx, src, repo.VariantGirl)
	if err != nil {
		return "", nil, err
	}
	gen := v.Generator
	if gen == nil {
		gen = &repo.Generator{}
	}
	return v.Text(), gen, nil
}

// BulkFetch makes a fetch function for bulk runs (girl --bulk, --import):
// it checks the repository's index of girl variants before asking for each
// page, so pages the repository doesn't have cost no requests. Older
// repositories without the index are asked page by page.
func BulkFetch(c *repo.Client) func(ctx context.Context, p manpage.Page) (string, *repo.Generator, error) {
	idx := c.VariantIndex(repo.VariantGirl)
	return func(ctx context.Context, p manpage.Page) (string, *repo.Generator, error) {
		src, err := manpage.SourceHash(p.Path, nil)
		if err != nil {
			return "", nil, err
		}
		if has, err := idx.Has(ctx, src); err == nil && !has {
			return "", nil, fmt.Errorf("%s: %w", p.Title(), repo.ErrNotFound)
		}
		return FetchFrom(ctx, c, p)
	}
}

// NewRepo makes a client for the configured repository, or nil if there
// isn't one.
func NewRepo(cfg *config.Config, version string) *repo.Client {
	u := strings.TrimSpace(cfg.Repo.URL)
	if u == "" || u == "none" {
		return nil
	}
	c := repo.NewClient(u, cfg.Repo.Timeout.Duration)
	c.UserAgent = "girlpages/" + version
	return c
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

// NewBatcher makes a Batcher for bulk runs using the configured batch API,
// keeping its state in dir. It returns nil if batching isn't set up. live,
// if not nil, redoes parts a batch got wrong.
func NewBatcher(cfg *config.Config, dir string, live llm.Backend) (*llm.Batcher, error) {
	kind, err := cfg.ResolveBatch()
	if err != nil {
		return nil, err
	}
	var api llm.BatchAPI
	switch kind {
	case config.BatchNone:
		return nil, nil
	case config.BatchOpenRouter:
		key, err := cfg.OpenAIKey()
		if err != nil {
			return nil, err
		}
		u := strings.TrimSpace(cfg.OpenAI.BatchURL)
		if u == "" {
			u = config.DefaultOpenRouterBatchURL
		}
		api = &llm.OpenRouterBatch{
			URL: u, APIKey: key, Headers: cfg.OpenAI.Headers, Model: cfg.OpenAI.Model,
			Temperature: cfg.OpenAI.Temperature, MaxTokens: cfg.OpenAI.MaxTokens,
			Client: &http.Client{Timeout: cfg.OpenAI.Timeout.Duration}, Retries: 3,
		}
	case config.BatchAnthropic:
		key, err := cfg.AnthropicKey()
		if err != nil {
			return nil, err
		}
		u := strings.TrimSpace(cfg.Anthropic.BatchURL)
		if u == "" {
			u = config.DefaultAnthropicBatchURL
		}
		api = &llm.AnthropicBatch{
			URL: u, APIKey: key, Headers: cfg.Anthropic.Headers, Model: cfg.Anthropic.Model,
			Temperature: cfg.Anthropic.Temperature, MaxTokens: cfg.Anthropic.MaxTokens,
			Client: &http.Client{Timeout: cfg.Anthropic.Timeout.Duration}, Retries: 3,
		}
	}
	return &llm.Batcher{
		Client:  api,
		Dir:     dir,
		Options: llm.Options{ChunkSize: cfg.ChunkSize, ExtraInstructions: cfg.ExtraInstructions},
		Live:    live,
		Jobs:    cfg.Jobs,
		Poll:    cfg.OpenAI.BatchPoll.Duration,
	}, nil
}

// CacheSet builds the cache list from the config.
func CacheSet(cfg *config.Config) cache.Set {
	var s cache.Set
	for _, d := range cfg.ReadCacheDirs() {
		s = append(s, cache.Dir(d))
	}
	return s
}

// Document fetches a document in a series (an RFC) from the repository,
// by name: there's no local copy to go by. It's the latest version's girl
// variant, or the original if there isn't one yet.
func (g *Girl) Document(ctx context.Context, section, name string) (*repo.Variant, error) {
	if g.Repo == nil {
		return nil, errNoRepo
	}
	l, err := g.Repo.Page(ctx, name, section)
	if err != nil {
		return nil, err
	}
	v := l.CurrentVersion()
	if v == nil {
		return nil, repo.ErrNotFound
	}
	for _, variant := range []string{repo.VariantGirl, repo.VariantMan} {
		if info, ok := v.Variants[variant]; ok {
			return g.Repo.VariantAt(ctx, v.Source, variant, info.SHA256)
		}
	}
	return nil, repo.ErrNotFound
}
