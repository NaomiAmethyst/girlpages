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
	Package  repo.Package
	Sections []string
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

// Source is something to ingest: a directory or a package file, and
// where it came from.
type Source struct {
	Path    string
	Package repo.Package
	// Loose looks for roff pages anywhere under Path, not just in manual
	// sections: for source trees such as git checkouts.
	Loose bool
}

// Paths ingests directories and package files. Package files' names,
// versions and architectures are read from their file names unless o.Package
// gives them (for a single file).
func Paths(ctx context.Context, s *repo.Store, paths []string, o Options) (Stats, error) {
	var srcs []Source
	for _, p := range paths {
		srcs = append(srcs, LocalSource(p, o.Package, len(paths) > 1))
	}
	return Sources(ctx, s, srcs, o)
}

// LocalSource makes a Source for a directory or package file. A package
// file's name, version and architecture come from its file name, unless
// pkg names it and it's the only one (one of many can't share a name).
func LocalSource(path string, pkg repo.Package, many bool) Source {
	if PackageKind(path) != "" {
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			guess := PackageFromFile(path)
			if pkg.Name == "" || many {
				pkg.Name, pkg.Version, pkg.Arch = guess.Name, guess.Version, guess.Arch
			}
		}
	}
	return Source{Path: path, Package: pkg}
}

// Sources ingests sources, each with its own provenance. Pages may include
// (.so) pages from any of them.
func Sources(ctx context.Context, s *repo.Store, srcs []Source, o Options) (Stats, error) {
	var cands []candidate
	var roots, jails []string
	for _, src := range srcs {
		p, pkg := src.Path, src.Package
		fi, err := os.Stat(p)
		if err != nil {
			return Stats{}, err
		}
		sysroot, jail := "", ""
		switch {
		case fi.IsDir() && !src.Loose && isManRoot(p):
			// A manual root on this system: links point where they say.
			sysroot = "/"
		case fi.IsDir():
			// An unpacked package, system image or source tree.
			sysroot, jail = p, p
		default:
			if PackageKind(p) == "" {
				return Stats{}, fmt.Errorf("%s: not a directory or a package file", p)
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
		var cs []candidate
		var rs []string
		if src.Loose {
			cs, rs, err = scanLoose(top, pkg, o.Sections)
		} else {
			cs, rs, err = scan(top, sysroot, pkg, o.Sections)
		}
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
	return InstalledPages(ctx, s, pages, dirs, o)
}

// InstalledPages ingests some of the system's man pages, recording which
// installed package each came from; roots are the manual roots to find
// .so includes in.
func InstalledPages(ctx context.Context, s *repo.Store, pages []manpage.Page, roots []string, o Options) (Stats, error) {
	distro, release := OSRelease("/")
	if o.Package.Distro == "" {
		o.Package.Distro, o.Package.Release = distro, release
	}
	owners := Owners()
	cands := make([]candidate, 0, len(pages))
	for _, p := range pages {
		// The page is whatever file a link leads to, so that file's package
		// is where it's from (ls.1.gz may be a link another package owns,
		// such as Ubuntu's coreutils-from-uutils, version 0.0.0, to
		// rust-ls.1.gz in rust-coreutils).
		pkg := o.Package
		var own repo.Package
		ok := false
		if real, err := filepath.EvalSymlinks(p.Path); err == nil {
			own, ok = owners[real]
		}
		if !ok {
			own, ok = owners[p.Path]
		}
		if ok {
			pkg.Name, pkg.Version, pkg.Arch = own.Name, own.Version, own.Arch
		}
		cands = append(cands, candidate{path: p.Path, name: p.Name, section: p.Section, pkg: pkg, roots: roots})
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
			dirSec := strings.TrimPrefix(d.Name(), "man")
			if sec == "" {
				sec = dirSec
			}
			// A page's section starts as its directory's does (man3 has
			// 3, 3p and 3ssl); anything else there (PMIx_Abort.3.html,
			// context.man1.pdf) is another kind of file.
			if !strings.HasPrefix(sec, dirSec[:1]) {
				continue
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

// current reports whether the cached conversion for hash was made with the
// current prompt.
func (m makers) current(hash string) bool {
	e := m[hash]
	return e != nil && e.PromptVersion >= llm.PromptVersion
}

// outdated reports whether a variant was made with an older prompt,
// reading it if its listing doesn't say (one written before listings
// recorded prompt versions, or a variant whose maker didn't say).
func outdated(s *repo.Store, variant, source, section string, info repo.VariantInfo) bool {
	if info.PromptVersion == 0 {
		if v, err := s.Variant(source, variant); err == nil && v.Generator != nil {
			info.PromptVersion = v.Generator.PromptVersion
		}
	}
	return info.PromptVersion < llm.PromptVersionVariant(llm.KindOf(section), variant)
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
	if err := ctx.Err(); err != nil {
		return st, err
	}
	return st, nil
}

// variantInfo describes a stored variant's latest generation.
func variantInfo(s *repo.Store, source, variant string) (repo.VariantInfo, error) {
	cs, err := s.Contents(source)
	if err != nil {
		return repo.VariantInfo{}, err
	}
	g := cs.Latest(variant)
	if g == nil {
		return repo.VariantInfo{}, repo.ErrNotFound
	}
	return *g, nil
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
	// Names restricts the run to these pages: a name for any section, or
	// name(section) for one.
	Names []string
	// AllVersions converts every version, not just current ones.
	AllVersions bool
	// Force converts again even if there's a variant already.
	Force bool
	// Reprompt converts again versions whose variant was made with an
	// older prompt (llm.PromptVersionVariant), and won't take such
	// conversions from the cache.
	Reprompt bool
	// OutputRatio, for dry runs, is how long output is reckoned to be next
	// to the original; zero means the package's OutputRatio.
	OutputRatio float64
	// Cache is girl's conversions, by the original's text: only the girl
	// variant (LLM.Variant "") uses it.
	Cache  cache.Set
	DryRun bool
	Quiet  bool
	Log    io.Writer
	// Batch, if set, sends pages off in batches instead of one at a time,
	// first collecting any that earlier runs sent.
	Batch *llm.Batcher
	// NoWait leaves batches running for a later run to collect.
	NoWait bool
	// PartsDir, if set, keeps the parts of long pages as they're converted,
	// so a page that fails part way only needs its missing parts again.
	PartsDir string
}

// progressEvery is how often girlify reports on a long page's parts.
const progressEvery = 30 * time.Second

// GirlifyStats summarises a Girlify run.
type GirlifyStats struct {
	Converted, FromCache, Failed, Remaining int
	// Batched pages were sent off in batches still running; Pending ones
	// were already in one sent earlier.
	Batched, Pending int
	// Estimate is what a dry run reckons the conversions would take.
	Estimate Estimate
}

// Estimate is the work a girlify run would send to the model.
type Estimate struct {
	// Pages to convert, and Requests (parts) they take; Cached pages are
	// in girl's cache already and cost nothing.
	Pages, Requests, Cached int
	// InputTokens and OutputTokens are rough: about four bytes a token,
	// and output as long as the original times OutputRatio.
	InputTokens, OutputTokens int64
}

// OutputRatio is how much longer a girlified page is than the original,
// measured from real conversions (median 1.12, most between 1 and 1.4).
const OutputRatio = 1.2

// EstimateTokens guesses how many tokens text is: about four bytes each,
// once runs of whitespace (man's indentation) are counted as one.
func EstimateTokens(text string) int64 {
	n, space := 0, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == ' ' || c == '\t' || c == '\n' {
			if !space {
				n++
			}
			space = true
			continue
		}
		space = false
		n++
	}
	return int64((n + 3) / 4)
}

// variant is the variant being made.
func (o GirlifyOptions) variant() string {
	if o.LLM.Variant == "" {
		return repo.VariantGirl
	}
	return o.LLM.Variant
}

// cached is girl's cached conversion of text, for the girl variant.
func (o GirlifyOptions) cached(text string) (string, bool) {
	if o.variant() != repo.VariantGirl {
		return "", false
	}
	return o.Cache.Object(cache.Hash(text))
}

// Girlify adds variants (girl, or o.LLM.Variant) to versions that don't
// have one.
func Girlify(ctx context.Context, s *repo.Store, o GirlifyOptions) (GirlifyStats, error) {
	var st GirlifyStats
	if _, err := llm.LookupVariant(o.LLM.Variant); err != nil {
		return st, err
	}
	variant := o.variant()
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
	pages, err := s.AllPagesContext(ctx)
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
		if err := ctx.Err(); err != nil {
			return st, err
		}
		// A name matches in any section; name(section) in that one.
		if len(names) > 0 && !names[p.Pointer.Name] && !names[p.Pointer.Name+"("+p.Pointer.Section+")"] {
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
			if info, ok := v.Variants[variant]; ok && !o.Force && !(o.Reprompt && outdated(s, variant, v.Source, p.Pointer.Section, info)) {
				continue
			}
			if pending[llm.PendingKey(v.Source, variant)] && !o.Force {
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
	// Aliases share the version's variant files, so every page with a
	// version being converted needs its listing updated, named or not.
	for _, p := range pages {
		for _, v := range p.Listing.Versions {
			w := bySource[v.Source]
			if w == nil {
				continue
			}
			listed := false
			for _, q := range w.pages {
				listed = listed || q == p.Pointer
			}
			if !listed {
				w.pages = append(w.pages, p.Pointer)
			}
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
		ratio := o.OutputRatio
		if ratio <= 0 {
			ratio = OutputRatio
		}
		e := &st.Estimate
		for _, src := range order {
			if err := ctx.Err(); err != nil {
				return st, err
			}
			w := bySource[src]
			man, err := s.Variant(src, repo.VariantMan)
			if err != nil {
				return st, err
			}
			text := man.Text()
			if _, ok := o.cached(text); ok && !o.Force {
				e.Cached++
				logf("💾 %s (in the local cache)\n", w.title)
				continue
			}
			lo := o.LLM
			lo.Kind = llm.KindOf(w.pages[0].Section)
			parts := llm.Plan(w.title, text, lo)
			var in, out int64
			for _, p := range parts {
				in += EstimateTokens(p.System) + EstimateTokens(p.User)
				out += int64(float64(EstimateTokens(p.Source)) * ratio)
			}
			e.Pages++
			e.Requests += len(parts)
			e.InputTokens += in
			e.OutputTokens += out
			logf("✨ would %s %s: %d requests, ~%d tokens in, ~%d out\n", verb(variant), w.title, len(parts), in, out)
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
			if err := ctx.Err(); err != nil {
				return st, err
			}
			w := bySource[src]
			man, err := s.Variant(src, repo.VariantMan)
			if err != nil {
				return st, err
			}
			text := man.Text()
			if markup, ok := o.cached(text); ok && !o.Force && (!o.Reprompt || made.current(cache.Hash(text))) {
				g := made.generator(cache.Hash(text), time.Now().UTC().Truncate(time.Second))
				if err := putVariant(s, variant, src, man.Title, markup, g, w.pages); err != nil {
					return st, err
				}
				st.FromCache++
				continue
			}
			meta, err := json.Marshal(w.pages)
			if err != nil {
				return st, err
			}
			jobs = append(jobs, llm.BatchJob{Key: src, Title: w.title, Text: text, Meta: meta, Kind: llm.KindOf(w.pages[0].Section), Variant: o.LLM.Variant})
		}
		if len(jobs) == 0 {
			return st, nil
		}
		logf("💌 sending %d pages to %s in batches: about half price, back within a day\n", len(jobs), o.Batch.Client.ModelName())
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
	logf("💅 %s %d pages with %s, %d at a time\n", verbing(variant), len(order), o.Backend.Name(), max(o.Jobs, 1))
	made := cacheMakers(o.Cache)
	// Every request shares Jobs slots, so when only a few long pages are
	// left their parts use the rest.
	o.LLM.Slots = make(chan struct{}, max(o.Jobs, 1))
	o.LLM.Parallel = max(o.Jobs, 1)
	if o.PartsDir != "" {
		o.LLM.Parts = partDir(o.PartsDir)
	}

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
				var last time.Time
				lo := o
				lo.LLM.Progress = func(done, total int) {
					if total > 1 && done < total && time.Since(last) >= progressEvery {
						if !last.IsZero() || done > 0 {
							logf("⏳ %s: %d/%d parts\n", w.title, done, total)
						}
						last = time.Now()
					}
				}
				lo.LLM.Kind = llm.KindOf(w.pages[0].Section)
				fromCache, err := girlifyOne(ctx, s, lo, made, w.source, w.title, func(info repo.VariantInfo) error {
					return recordVariant(s, variant, w.source, w.pages, info)
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

func girlifyOne(ctx context.Context, s *repo.Store, o GirlifyOptions, made makers, source, title string, record func(repo.VariantInfo) error) (_ bool, err error) {
	man, err := s.Variant(source, repo.VariantMan)
	if err != nil {
		return false, err
	}
	text := man.Text()
	g := made.generator(cache.Hash(text), time.Now().UTC().Truncate(time.Second))
	markup, fromCache := "", false
	if !o.Force {
		if !o.Reprompt || made.current(cache.Hash(text)) {
			markup, fromCache = o.cached(text)
		}
	}
	if !fromCache {
		lo := o.LLM
		lo.Parallel = max(lo.Parallel, 1)
		markup, err = llm.Convert(ctx, o.Backend, title, text, lo)
		if err != nil {
			return false, err
		}
		if pd, ok := lo.Parts.(partDir); ok {
			// Once the page is safely stored, its parts aren't needed.
			defer func() {
				if err == nil {
					pd.forget(o.Backend, llm.Plan(title, text, lo))
				}
			}()
		}
		g = &repo.Generator{Backend: o.Backend.Name(), Model: o.Backend.Model(), PromptVersion: llm.PromptVersionVariant(o.LLM.Kind, o.variant()),
			Generated: time.Now().UTC().Truncate(time.Second)}
	}
	info, err := s.PutVariant(repo.MarkupVariant(o.variant(), source, man.Title, markup, g))
	if err != nil {
		return fromCache, err
	}
	return fromCache, record(info)
}

// recordVariant notes a version's new variant in the listings of every
// page that has it.
func recordVariant(s *repo.Store, variant, source string, pages []repo.Pointer, info repo.VariantInfo) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	for _, p := range pages {
		err := s.UpdateListing(p.Name, p.Section, func(l *repo.Listing) error {
			if v := l.Find(source); v != nil {
				v.Variants[variant] = info
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// putVariant stores a variant and records it.
func putVariant(s *repo.Store, variant, source, title, markup string, g *repo.Generator, pages []repo.Pointer) error {
	info, err := s.PutVariant(repo.MarkupVariant(variant, source, title, markup, g))
	if err != nil {
		return err
	}
	return recordVariant(s, variant, source, pages, info)
}

// verb and verbing say what making a variant is called: girlify for girl,
// "make the pirate variant of" for the rest.
func verb(variant string) string {
	if variant == repo.VariantGirl {
		return "girlify"
	}
	return "make the " + variant + " variant of"
}

func verbing(variant string) string {
	if variant == repo.VariantGirl {
		return "girlifying"
	}
	return "making the " + variant + " variant of"
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
				g := &repo.Generator{Backend: "openai", Model: r.Model, PromptVersion: r.PromptVersion,
					Generated: time.Now().UTC().Truncate(time.Second)}
				variant := r.Job.Variant
				if variant == "" {
					variant = repo.VariantGirl
				}
				err = putVariant(s, variant, r.Job.Key, man.Title, r.Markup, g, pages)
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
