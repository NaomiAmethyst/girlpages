// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package ingest adds man pages to a girlpages repository: from man
// directories, from package files, or from the running system, and then
// girlifies them.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// Options controls an ingest.
type Options struct {
	// Package is provenance recorded for every page. Fields left empty are
	// filled in per package file from its name.
	Package repo.Package
	// KeepCurrent leaves pages' current versions alone, for adding older
	// releases after newer ones.
	KeepCurrent bool
	Sections    []string
	// Cache is searched for existing conversions of the same text, so
	// pages already girlified locally don't cost another LLM call.
	Cache  cache.Set
	Man    manpage.Options
	Jobs   int
	DryRun bool
	Quiet  bool
	Log    io.Writer
}

// Stats summarises an ingest.
type Stats struct {
	// Pages found; New versions added; Known versions already there.
	Pages, New, Known int
	// FromCache new versions came with a girl variant from the local cache.
	FromCache int
	// Skipped pages couldn't be read or rendered.
	Skipped int
}

type candidate struct {
	path          string // file to read (symlinks resolved)
	name, section string
	pkg           repo.Package
	roots         []string
	// jails, if set, are where the page and anything it includes must be.
	jails []string
}

// Paths ingests directories and package files.
func Paths(ctx context.Context, s *repo.Store, paths []string, o Options) (Stats, error) {
	var cands []candidate
	var roots, jails []string
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return Stats{}, err
		}
		pkg := o.Package
		sysroot, jail := "", ""
		switch {
		case fi.IsDir() && isManRoot(p):
			// A manual root on this system: links point where they say.
			sysroot = "/"
		case fi.IsDir():
			// An unpacked package or system image.
			sysroot, jail = p, p
		default:
			if PackageKind(p) == "" {
				return Stats{}, fmt.Errorf("%s: not a directory or a package file", p)
			}
			guess := PackageFromFile(p)
			if pkg.Name == "" || len(paths) > 1 {
				pkg.Name, pkg.Version, pkg.Arch = guess.Name, guess.Version, guess.Arch
			}
			dir, err := os.MkdirTemp("", "girlrepo-pkg-")
			if err != nil {
				return Stats{}, err
			}
			defer os.RemoveAll(dir)
			if err := Extract(p, dir); err != nil {
				return Stats{}, err
			}
			sysroot, jail = dir, dir
		}
		top := p
		if sysroot != "/" {
			top = sysroot
		}
		cs, rs, err := scan(top, sysroot, pkg, o.Sections)
		if err != nil {
			return Stats{}, err
		}
		if len(cs) == 0 && !o.Quiet {
			fmt.Fprintf(o.Log, "🤷 no man pages in %s\n", p)
		}
		cands = append(cands, cs...)
		roots = append(roots, rs...)
		if jail != "" {
			jails = append(jails, jail)
		}
	}
	for i := range cands {
		cands[i].roots, cands[i].jails = roots, jails
	}
	return run(ctx, s, cands, o)
}

// System ingests the running system's man pages, recording which
// installed package each came from.
func System(ctx context.Context, s *repo.Store, o Options) (Stats, error) {
	dirs, err := manpage.ManPath(ctx, o.Man)
	if err != nil {
		return Stats{}, err
	}
	pages, err := manpage.Enumerate(dirs, o.Sections)
	if err != nil {
		return Stats{}, err
	}
	distro, release := OSRelease("/")
	if o.Package.Distro == "" {
		o.Package.Distro, o.Package.Release = distro, release
	}
	owners := Owners()
	cands := make([]candidate, 0, len(pages))
	for _, p := range pages {
		pkg := o.Package
		if own, ok := owners[p.Path]; ok {
			pkg.Name, pkg.Version, pkg.Arch = own.Name, own.Version, own.Arch
		} else if real, err := filepath.EvalSymlinks(p.Path); err == nil {
			if own, ok := owners[real]; ok {
				pkg.Name, pkg.Version, pkg.Arch = own.Name, own.Version, own.Arch
			}
		}
		cands = append(cands, candidate{path: p.Path, name: p.Name, section: p.Section, pkg: pkg, roots: dirs})
	}
	return run(ctx, s, cands, o)
}

var manDirRE = regexp.MustCompile(`^man[0-9nlo]`)

// scan finds English man pages under top. A directory counts as a manual
// section if it's named man<section> and its parent is named man (so
// translations in man/de/man1 are left out), or if its parent is top
// itself. Symlinks are followed inside sysroot, so an absolute link in an
// unpacked package points into the package, not the host.
func scan(top, sysroot string, pkg repo.Package, sections []string) ([]candidate, []string, error) {
	var out []candidate
	var roots []string
	seenRoot := map[string]bool{}
	err := filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return err
		}
		if !d.IsDir() || !manDirRE.MatchString(d.Name()) {
			return nil
		}
		parent := filepath.Dir(path)
		if filepath.Base(parent) != "man" && parent != filepath.Clean(top) {
			return nil
		}
		if !seenRoot[parent] {
			seenRoot[parent] = true
			roots = append(roots, parent)
		}
		ents, err := os.ReadDir(path)
		if err != nil {
			return nil
		}
		for _, e := range ents {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			file := filepath.Join(path, e.Name())
			real, err := resolveIn(sysroot, file)
			if err != nil {
				continue
			}
			name, sec := manpage.ParseFilename(file)
			if sec == "" {
				sec = strings.TrimPrefix(d.Name(), "man")
			}
			if !manpage.SectionMatches(sec, sections) || name == "" {
				continue
			}
			out = append(out, candidate{path: real, name: name, section: sec, pkg: pkg})
		}
		return fs.SkipDir
	})
	return out, roots, err
}

// isManRoot reports whether dir is a manual root itself (has man1 and so on).
func isManRoot(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.IsDir() && manDirRE.MatchString(e.Name()) {
			return true
		}
	}
	return false
}

// resolveIn follows symlinks, treating absolute targets as relative to
// sysroot when it isn't "/". It returns the regular file reached.
func resolveIn(sysroot, path string) (string, error) {
	for hops := 0; hops < 40; hops++ {
		fi, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			if !fi.Mode().IsRegular() {
				return "", fmt.Errorf("%s: not a regular file", path)
			}
			return path, nil
		}
		t, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(t) {
			if sysroot != "/" {
				t = filepath.Join(sysroot, t)
			}
		} else {
			t = filepath.Join(filepath.Dir(path), t)
		}
		path = t
	}
	return "", fmt.Errorf("%s: too many symlinks", path)
}

// makers records what made each conversion in the local cache, so girl
// variants taken from it say which model wrote them.
type makers map[string]*cache.Entry

func cacheMakers(caches cache.Set) makers {
	m := makers{}
	for _, d := range caches {
		_ = d.Entries(func(e *cache.Entry) error {
			m[e.Hash] = e
			return nil
		})
	}
	return m
}

func (m makers) generator(hash string, fallback time.Time) *repo.Generator {
	g := &repo.Generator{Generated: fallback}
	if e := m[hash]; e != nil {
		g.Backend, g.Model, g.PromptVersion = e.Backend, e.Model, e.PromptVersion
		if !e.Generated.IsZero() {
			g.Generated = e.Generated
		}
	}
	return g
}

func run(ctx context.Context, s *repo.Store, cands []candidate, o Options) (Stats, error) {
	var st Stats
	st.Pages = len(cands)
	if o.Log == nil {
		o.Log = io.Discard
	}
	logf := func(format string, a ...any) {
		if !o.Quiet {
			fmt.Fprintf(o.Log, format, a...)
		}
	}
	unlock, err := s.Lock()
	if err != nil {
		return st, err
	}
	defer unlock()

	made := cacheMakers(o.Cache)

	stamp := time.Now().UTC().Truncate(time.Second)
	var (
		mu      sync.Mutex
		next    atomic.Int64
		wg      sync.WaitGroup
		renders sync.Map // source hash → *sync.Once-guarded result, so aliases render once
	)
	type rendered struct {
		once sync.Once
		man  *repo.Variant
		girl *repo.Variant
		err  error
	}
	jobs := o.Jobs
	if jobs < 1 {
		jobs = runtime.NumCPU()
	}
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(cands) || ctx.Err() != nil {
					return
				}
				c := cands[i]
				title := c.name + "(" + c.section + ")"
				src, err := manpage.SourceIn(c.path, c.roots, c.jails)
				if err != nil {
					mu.Lock()
					st.Skipped++
					mu.Unlock()
					logf("🙈 skipping %s: %v\n", title, err)
					continue
				}
				source := repo.Hash(src)

				l, _, err := s.Listing(c.name, c.section)
				if err != nil {
					logf("💔 %s: %v\n", title, err)
					mu.Lock()
					st.Skipped++
					mu.Unlock()
					continue
				}
				var have *repo.Version
				if l != nil {
					have = l.Find(source)
				}
				if have != nil && have.Variants[repo.VariantMan].SHA256 != "" && s.HasVariant(source, repo.VariantMan) {
					mu.Lock()
					st.Known++
					mu.Unlock()
					if !o.DryRun {
						err = s.UpdateListing(c.name, c.section, func(l *repo.Listing) error {
							v := l.Find(source)
							if v == nil {
								return nil
							}
							addPackage(v, c.pkg)
							if !o.KeepCurrent {
								l.Current = source
							}
							return nil
						})
						if err != nil {
							logf("💔 %s: %v\n", title, err)
						}
					}
					continue
				}
				if o.DryRun {
					mu.Lock()
					st.New++
					mu.Unlock()
					logf("✨ would add %s (%s)\n", title, source[:12])
					continue
				}

				ri, _ := renders.LoadOrStore(source, &rendered{})
				r := ri.(*rendered)
				r.once.Do(func() {
					text, err := manpage.SourceText(ctx, src, filepath.Base(c.name+"."+c.section), o.Man)
					if err != nil {
						r.err = err
						return
					}
					r.man = repo.ManVariant(source, text)
					if _, err := s.PutVariant(r.man); err != nil {
						r.err = err
						return
					}
					if markup, ok := o.Cache.Object(cache.Hash(text)); ok && !s.HasVariant(source, repo.VariantGirl) {
						g := made.generator(cache.Hash(text), stamp)
						r.girl = repo.GirlVariant(source, r.man.Title, markup, g)
						if _, err := s.PutVariant(r.girl); err != nil {
							r.err = err
						}
					}
				})
				if r.err != nil {
					mu.Lock()
					st.Skipped++
					mu.Unlock()
					logf("🙈 skipping %s: %v\n", title, r.err)
					continue
				}
				err = s.UpdateListing(c.name, c.section, func(l *repo.Listing) error {
					v := l.Find(source)
					if v == nil {
						l.Versions = append(l.Versions, repo.Version{Source: source, Added: stamp,
							Variants: map[string]repo.VariantInfo{}})
						v = &l.Versions[len(l.Versions)-1]
					}
					v.Title = r.man.Title
					v.Description = repo.Description(r.man)
					for _, variant := range []string{repo.VariantMan, repo.VariantGirl} {
						if info, err := variantInfo(s, source, variant); err == nil {
							v.Variants[variant] = info
						}
					}
					addPackage(v, c.pkg)
					if !o.KeepCurrent || l.Current == "" {
						l.Current = source
					}
					return nil
				})
				mu.Lock()
				if err != nil {
					st.Skipped++
					logf("💔 %s: %v\n", title, err)
				} else {
					st.New++
					if r.girl != nil {
						st.FromCache++
					}
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return st, ctx.Err()
}

// variantInfo hashes a stored variant.
func variantInfo(s *repo.Store, source, variant string) (repo.VariantInfo, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, filepath.FromSlash(repo.ContentPath(source, variant))))
	if err != nil {
		return repo.VariantInfo{}, err
	}
	return repo.VariantInfo{SHA256: repo.Hash(b), Size: int64(len(b))}, nil
}

func addPackage(v *repo.Version, p repo.Package) {
	if p == (repo.Package{}) {
		return
	}
	for _, q := range v.Packages {
		if q == p {
			return
		}
	}
	v.Packages = append(v.Packages, p)
	sort.Slice(v.Packages, func(i, j int) bool { return v.Packages[i].String() < v.Packages[j].String() })
}

// GirlifyOptions controls Girlify.
type GirlifyOptions struct {
	Backend llm.Backend
	LLM     llm.Options
	Jobs    int
	// Limit stops after converting this many pages.
	Limit    int
	Sections []string
	// Names restricts the run to these page names.
	Names []string
	// AllVersions converts every version, not just current ones.
	AllVersions bool
	// Force converts again even if there's a girl variant already.
	Force  bool
	Cache  cache.Set
	DryRun bool
	Quiet  bool
	Log    io.Writer
	// Batch, if set, sends pages off in batches instead of one at a time,
	// first collecting any that earlier runs sent.
	Batch *llm.Batcher
	// NoWait leaves batches running for a later run to collect.
	NoWait bool
}

// GirlifyStats summarises a Girlify run.
type GirlifyStats struct {
	Converted, FromCache, Failed, Remaining int
	// Batched pages were sent off in batches still running; Pending ones
	// were already in one sent earlier.
	Batched, Pending int
}

// Girlify adds girl variants to versions that don't have one.
func Girlify(ctx context.Context, s *repo.Store, o GirlifyOptions) (GirlifyStats, error) {
	var st GirlifyStats
	if o.Log == nil {
		o.Log = io.Discard
	}
	logf := func(format string, a ...any) {
		if !o.Quiet {
			fmt.Fprintf(o.Log, format, a...)
		}
	}
	pending := map[string]bool{}
	if o.Batch != nil && !o.DryRun {
		if err := collectGirl(ctx, s, o, false, &st, logf); err != nil {
			return st, err
		}
		var err error
		if pending, err = o.Batch.Pending(); err != nil {
			return st, err
		}
	}
	pages, err := s.AllPages()
	if err != nil {
		return st, err
	}
	counted := map[string]bool{}
	names := map[string]bool{}
	for _, n := range o.Names {
		names[n] = true
	}
	type work struct {
		source, title string
		pages         []repo.Pointer
	}
	bySource := map[string]*work{}
	var order []string
	for _, p := range pages {
		if len(names) > 0 && !names[p.Pointer.Name] {
			continue
		}
		if !manpage.SectionMatches(p.Pointer.Section, o.Sections) {
			continue
		}
		vs := p.Listing.Versions
		if !o.AllVersions {
			vs = []repo.Version{*p.Current()}
		}
		for _, v := range vs {
			if _, ok := v.Variants[repo.VariantGirl]; ok && !o.Force {
				continue
			}
			if pending[v.Source] && !o.Force {
				if !counted[v.Source] {
					counted[v.Source] = true
					st.Pending++
				}
				continue
			}
			w := bySource[v.Source]
			if w == nil {
				title := v.Title
				if title == "" {
					title = strings.ToUpper(p.Pointer.Name) + "(" + p.Pointer.Section + ")"
				}
				w = &work{source: v.Source, title: title}
				bySource[v.Source] = w
				order = append(order, v.Source)
			}
			w.pages = append(w.pages, p.Pointer)
		}
	}
	// Same order as man searches: commands first.
	sort.SliceStable(order, func(i, j int) bool {
		return sectionRank(bySource[order[i]].pages[0].Section) < sectionRank(bySource[order[j]].pages[0].Section)
	})
	if o.Limit > 0 && len(order) > o.Limit {
		st.Remaining = len(order) - o.Limit
		order = order[:o.Limit]
	}
	if o.DryRun {
		for _, src := range order {
			logf("✨ would girlify %s\n", bySource[src].title)
		}
		st.Remaining += len(order)
		return st, nil
	}
	if len(order) == 0 {
		return st, nil
	}
	if o.Batch != nil {
		jobs := make([]llm.BatchJob, 0, len(order))
		made := cacheMakers(o.Cache)
		for _, src := range order {
			w := bySource[src]
			man, err := s.Variant(src, repo.VariantMan)
			if err != nil {
				return st, err
			}
			text := man.Text()
			if markup, ok := o.Cache.Object(cache.Hash(text)); ok && !o.Force {
				g := made.generator(cache.Hash(text), time.Now().UTC().Truncate(time.Second))
				if err := putGirl(s, src, man.Title, markup, g, w.pages); err != nil {
					return st, err
				}
				st.FromCache++
				continue
			}
			meta, err := json.Marshal(w.pages)
			if err != nil {
				return st, err
			}
			jobs = append(jobs, llm.BatchJob{Key: src, Title: w.title, Text: text, Meta: meta})
		}
		if len(jobs) == 0 {
			return st, nil
		}
		logf("💌 sending %d pages to %s in batches: about half price, back within a day\n", len(jobs), o.Batch.Client.Model)
		if _, err := o.Batch.Submit(ctx, jobs); err != nil {
			return st, err
		}
		if o.NoWait {
			st.Batched = len(jobs)
			logf("💤 not waiting; the next girlrepo girlify collects them\n")
			return st, nil
		}
		logf("⏳ waiting for them (ctrl-c is fine: they keep going, and the next girlify collects them)\n")
		return st, collectGirl(ctx, s, o, true, &st, logf)
	}
	if o.Backend == nil {
		return st, errors.New("no model to girlify with")
	}
	logf("💅 girlifying %d pages with %s, %d at a time\n", len(order), o.Backend.Name(), max(o.Jobs, 1))
	made := cacheMakers(o.Cache)

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		done atomic.Int64
	)
	queue := make(chan *work)
	for i := 0; i < max(o.Jobs, 1); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w := range queue {
				start := time.Now()
				fromCache, err := girlifyOne(ctx, s, o, made, w.source, w.title, func(info repo.VariantInfo) error {
					return recordGirl(s, w.source, w.pages, info)
				})
				n := done.Add(1)
				mu.Lock()
				switch {
				case err != nil:
					st.Failed++
					if ctx.Err() == nil {
						fmt.Fprintf(o.Log, "[%d/%d] 💔 %s: %v\n", n, len(order), w.title, err)
					}
				case fromCache:
					st.FromCache++
					logf("[%d/%d] 💾 %s (from the local cache)\n", n, len(order), w.title)
				default:
					st.Converted++
					logf("[%d/%d] 💖 %s (%s)\n", n, len(order), w.title, time.Since(start).Round(time.Second))
				}
				mu.Unlock()
			}
		}()
	}
	for _, src := range order {
		select {
		case queue <- bySource[src]:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(queue)
	wg.Wait()
	return st, ctx.Err()
}

func girlifyOne(ctx context.Context, s *repo.Store, o GirlifyOptions, made makers, source, title string, record func(repo.VariantInfo) error) (bool, error) {
	man, err := s.Variant(source, repo.VariantMan)
	if err != nil {
		return false, err
	}
	text := man.Text()
	g := made.generator(cache.Hash(text), time.Now().UTC().Truncate(time.Second))
	markup, fromCache := "", false
	if !o.Force {
		markup, fromCache = o.Cache.Object(cache.Hash(text))
	}
	if !fromCache {
		markup, err = llm.Convert(ctx, o.Backend, title, text, llm.Options{
			ChunkSize: o.LLM.ChunkSize, Parallel: 1, ExtraInstructions: o.LLM.ExtraInstructions,
		})
		if err != nil {
			return false, err
		}
		g = &repo.Generator{Backend: o.Backend.Name(), Model: o.Backend.Model(), PromptVersion: llm.PromptVersion,
			Generated: time.Now().UTC().Truncate(time.Second)}
	}
	info, err := s.PutVariant(repo.GirlVariant(source, man.Title, markup, g))
	if err != nil {
		return fromCache, err
	}
	return fromCache, record(info)
}

// recordGirl notes a version's new girl variant in the listings of every
// page that has it.
func recordGirl(s *repo.Store, source string, pages []repo.Pointer, info repo.VariantInfo) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	for _, p := range pages {
		err := s.UpdateListing(p.Name, p.Section, func(l *repo.Listing) error {
			if v := l.Find(source); v != nil {
				v.Variants[repo.VariantGirl] = info
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// putGirl stores a girl variant and records it.
func putGirl(s *repo.Store, source, title, markup string, g *repo.Generator, pages []repo.Pointer) error {
	info, err := s.PutVariant(repo.GirlVariant(source, title, markup, g))
	if err != nil {
		return err
	}
	return recordGirl(s, source, pages, info)
}

// collectGirl stores what finished batches sent back.
func collectGirl(ctx context.Context, s *repo.Store, o GirlifyOptions, wait bool, st *GirlifyStats, logf func(string, ...any)) error {
	var mu sync.Mutex
	cs, err := o.Batch.Collect(ctx, wait, func(j llm.SavedJob) (string, bool) {
		man, err := s.Variant(j.Key, repo.VariantMan)
		if err != nil {
			return "", false
		}
		return man.Text(), true
	}, func(r llm.Result) {
		err := r.Err
		if err == nil {
			var pages []repo.Pointer
			_ = json.Unmarshal(r.Job.Meta, &pages)
			var man *repo.Variant
			if man, err = s.Variant(r.Job.Key, repo.VariantMan); err == nil {
				g := &repo.Generator{Backend: "openai", Model: r.Model, PromptVersion: llm.PromptVersion,
					Generated: time.Now().UTC().Truncate(time.Second)}
				err = putGirl(s, r.Job.Key, man.Title, r.Markup, g, pages)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			st.Failed++
			fmt.Fprintf(o.Log, "💔 %s: %v (it'll go in the next batch)\n", r.Job.Title, err)
			return
		}
		st.Converted++
		logf("💖 %s (from a batch)\n", r.Job.Title)
	})
	if cs.Finished > 0 {
		logf("📬 %d finished batches: %d pages girlified", cs.Finished, cs.Pages)
		if cs.Cost > 0 {
			logf(", costing $%.4f", cs.Cost)
		}
		logf("\n")
	}
	if cs.Running > 0 && !wait {
		logf("⏳ %d batches still running\n", cs.Running)
	}
	return err
}

// sectionRank orders sections the way man searches them.
func sectionRank(sec string) int {
	for i, s := range []string{"1", "8", "6", "7", "5", "4", "3", "2", "n", "l", "9"} {
		if strings.HasPrefix(sec, s) {
			return i
		}
	}
	return 99
}
