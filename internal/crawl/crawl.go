// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package crawl ingests whole package repositories (apt, rpm-md, pacman).
//
// Man pages only exist inside packages, and packages are compressed as
// single streams, so there's no fetching one file out of them. What the
// repositories' indexes do say is which files each package holds, so a
// crawl reads those, downloads just the packages with English man pages in,
// and remembers what it has ingested so later crawls only fetch what's new.
package crawl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/ingest"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// Package is a package file in a repository.
type Package struct {
	Name, Version, Arch string
	// URL is where to download it.
	URL string
	// SHA256 is its checksum from the index, if the index has one.
	SHA256 string
	Size   int64
}

func (p Package) key() string {
	if p.SHA256 != "" {
		return "sha256:" + p.SHA256
	}
	return fmt.Sprintf("url:%s|%d", p.URL, p.Size)
}

// Index is what a repository's indexes say.
type Index struct {
	// Compare orders two versions of a package the repository's way.
	Compare func(a, b string) int
	// Distro and Release, if the repository says.
	Distro, Release string
	// Total is how many packages the repository has.
	Total int
	// Packages are the ones with man pages.
	Packages []Package
}

// Repository is a kind of package repository.
type Repository interface {
	// Index reads the repository's indexes.
	Index(ctx context.Context, f *Fetcher) (*Index, error)
}

// manPathRE matches English man pages in an index's file lists.
var manPathRE = regexp.MustCompile(`(^|/)man/man[0-9nlo][^/]*/[^/]+$`)

// IsManPath reports whether a path from a file list is an English man page.
func IsManPath(p string) bool { return manPathRE.MatchString(p) }

// Options controls a crawl.
type Options struct {
	// Distro and Release override what the repository says.
	Distro, Release string
	// Match, if set, limits the crawl to packages whose names match.
	Match *regexp.Regexp
	// Limit stops after this many packages.
	Limit int
	// Jobs is how many packages download at once.
	Jobs int
	// Group is how many packages are ingested together; pages can only
	// include (.so) pages from packages in the same group.
	Group int
	// Again ingests packages even if an earlier crawl did.
	Again bool
	// AllVersions crawls every version a repository holds, not just the
	// newest of each package.
	AllVersions bool
	DryRun      bool
	// State is the file remembering which packages were ingested.
	State   string
	Ingest  ingest.Options
	Fetcher *Fetcher
	Log     io.Writer
	Quiet   bool
}

// Stats summarises a crawl.
type Stats struct {
	// Total packages; WithMan have man pages; Done were ingested before;
	// Fetched now; Failed couldn't be.
	Total, WithMan, Done, Fetched, Failed int
	// Bytes downloaded (or, for a dry run, that would be).
	Bytes int64
	// Pages is the sum of the ingests' stats.
	Pages ingest.Stats
}

// state remembers what's been ingested: package keys and when.
type state struct {
	Done map[string]string `json:"done"`
}

func loadState(path string) (*state, error) {
	st := &state{Done: map[string]string{}}
	if path == "" {
		return st, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if st.Done == nil {
		st.Done = map[string]string{}
	}
	return st, nil
}

func (st *state) save(path string) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	return repo.WriteFile(path, b)
}

// Run crawls a repository into the store.
func Run(ctx context.Context, s *repo.Store, r Repository, o Options) (Stats, error) {
	var st Stats
	logf := func(format string, a ...any) {
		if !o.Quiet && o.Log != nil {
			fmt.Fprintf(o.Log, format, a...)
		}
	}
	f := o.Fetcher
	if f == nil {
		f = &Fetcher{Retries: 3}
	}
	idx, err := r.Index(ctx, f)
	if err != nil {
		return st, err
	}
	st.Total = len(idx.Packages)
	if idx.Total > 0 {
		st.Total = idx.Total
	}
	pkg := repo.Package{Distro: idx.Distro, Release: idx.Release}
	if o.Distro != "" {
		pkg.Distro = o.Distro
	}
	if o.Release != "" {
		pkg.Release = o.Release
	}

	if !o.AllVersions && idx.Compare != nil {
		idx.Packages = newest(idx.Packages, idx.Compare)
	}
	known, err := loadState(o.State)
	if err != nil {
		return st, err
	}
	var todo []Package
	for _, p := range idx.Packages {
		if o.Match != nil && !o.Match.MatchString(p.Name) {
			continue
		}
		st.WithMan++
		if !o.Again && known.Done[p.key()] != "" {
			st.Done++
			continue
		}
		todo = append(todo, p)
	}
	sort.SliceStable(todo, func(i, j int) bool { return todo[i].Name < todo[j].Name })
	if o.Limit > 0 && len(todo) > o.Limit {
		todo = todo[:o.Limit]
	}
	for _, p := range todo {
		st.Bytes += p.Size
	}
	logf("📚 %d packages, %d with man pages, %d done before; %d to fetch (%s)\n",
		st.Total, st.WithMan, st.Done, len(todo), humanBytes(st.Bytes))
	if o.DryRun {
		for _, p := range todo {
			logf("  %s %s %s\n", p.Name, p.Version, p.Arch)
		}
		return st, nil
	}
	st.Bytes = 0

	tmp, err := os.MkdirTemp("", "girlrepo-crawl-")
	if err != nil {
		return st, err
	}
	defer os.RemoveAll(tmp)
	group := max(o.Group, 1)
	jobs := max(o.Jobs, 1)
	for start := 0; start < len(todo); start += group {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		chunk := todo[start:min(start+group, len(todo))]
		files := make([]string, len(chunk))
		errs := make([]error, len(chunk))
		sizes := make([]int64, len(chunk))
		var wg sync.WaitGroup
		sem := make(chan struct{}, jobs)
		for i, p := range chunk {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				files[i] = filepath.Join(tmp, fmt.Sprintf("%d-%s", start+i, filepath.Base(p.URL)))
				sizes[i], errs[i] = f.Download(ctx, p.URL, p.SHA256, files[i])
			}()
		}
		wg.Wait()
		var srcs []ingest.Source
		var got []Package
		for i, p := range chunk {
			st.Bytes += sizes[i]
			if errs[i] != nil {
				if ctx.Err() != nil {
					return st, ctx.Err()
				}
				st.Failed++
				fmt.Fprintf(o.Log, "💔 %s: %v\n", p.Name, errs[i])
				continue
			}
			pk := pkg
			pk.Name, pk.Version, pk.Arch = p.Name, p.Version, p.Arch
			srcs = append(srcs, ingest.Source{Path: files[i], Package: pk})
			got = append(got, p)
		}
		if len(srcs) > 0 {
			iopts := o.Ingest
			iopts.Quiet = true // a line per package is plenty
			is, err := ingest.Sources(ctx, s, srcs, iopts)
			if err != nil {
				return st, err
			}
			st.Pages.Pages += is.Pages
			st.Pages.New += is.New
			st.Pages.Known += is.Known
			st.Pages.FromCache += is.FromCache
			st.Pages.Skipped += is.Skipped
			st.Fetched += len(got)
			now := time.Now().UTC().Format(time.RFC3339)
			for _, p := range got {
				known.Done[p.key()] = p.Name + " " + p.Version + " " + now
			}
			if err := known.save(o.State); err != nil {
				return st, err
			}
			logf("[%d/%d] 📦 %d packages: %d pages, %d new versions\n",
				min(start+group, len(todo)), len(todo), len(got), is.Pages, is.New)
		}
		for _, file := range files {
			os.Remove(file)
		}
	}
	return st, nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
