// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package inspect

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// indexGroup fills in the group of indexes.
func (r *Repo) indexGroup(n *Node) {
	n.value = func(ctx context.Context) (any, error) {
		m, err := r.Manifest(ctx)
		if err != nil {
			return nil, err
		}
		return m.Index, nil
	}
	n.children = func(ctx context.Context, yield func(*Node) error) error {
		m, err := r.Manifest(ctx)
		if err != nil {
			return err
		}
		ix := m.Index
		nodes := []*Node{r.sharded("words", "index/words", ix.Words), r.sharded("grams", "index/grams", ix.Grams),
			r.table("names", ix.Names), r.table("docs", ix.Docs)}
		if len(ix.Variants) > 0 {
			nodes = append(nodes, r.variantIndexes())
		}
		if ix.Packages != "" {
			nodes = append(nodes, r.packageIndex(ix.Packages))
		}
		for _, c := range nodes {
			if err := yield(c); err != nil {
				return err
			}
		}
		return nil
	}
}

// variantIndexes is the group of variant indexes.
func (r *Repo) variantIndexes() *Node {
	return &Node{
		r: r, Kind: "group", Addr: "index/variants", Summary: "which versions have each variant",
		value: func(ctx context.Context) (any, error) {
			m, err := r.Manifest(ctx)
			if err != nil {
				return nil, err
			}
			return m.Index.Variants, nil
		},
		children: func(ctx context.Context, yield func(*Node) error) error {
			m, err := r.Manifest(ctx)
			if err != nil {
				return err
			}
			for _, name := range slices.Sorted(maps.Keys(m.Index.Variants)) {
				if err := yield(r.sharded(repo.VariantIndexKind(name), "index/variants/"+name, m.Index.Variants[name])); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// sharded is an index of shards by key: words, grams or a variant's.
func (r *Repo) sharded(kind, addr, sha string) *Node {
	read := func(ctx context.Context) (*repo.ShardManifest, error) {
		var sm repo.ShardManifest
		return &sm, r.get(ctx, repo.IndexManifestPath(kind, valid(sha)), &sm)
	}
	return &Node{
		r: r, Kind: "shard-manifest", Addr: addr, File: repo.IndexManifestPath(kind, valid(sha)), Hash: sha,
		Summary: "shards by the range of keys each holds",
		children: func(ctx context.Context, yield func(*Node) error) error {
			sm, err := read(ctx)
			if err != nil {
				return err
			}
			for i, s := range sm.Shards {
				if err := yield(r.keyShard(kind, fmt.Sprintf("%s/shards/%d", addr, i+1), s)); err != nil {
					return err
				}
			}
			return nil
		},
		describe: func(ctx context.Context, w io.Writer) error {
			sm, err := read(ctx)
			if err != nil {
				return err
			}
			var total int64
			for _, s := range sm.Shards {
				total += s.Bytes
			}
			fmt.Fprintf(w, "shards: %d, %s\n", len(sm.Shards), size(total))
			if len(sm.Shards) > 0 {
				fmt.Fprintf(w, "keys: %q to %q\n", sm.Shards[0].First, sm.Shards[len(sm.Shards)-1].Last)
			}
			if len(sm.Common) > 0 {
				fmt.Fprintf(w, "common words: %s\n", strings.Join(sm.Common, " "))
			}
			return nil
		},
		check: func(_ context.Context, data []byte, report Report) {
			var sm repo.ShardManifest
			if !decode(data, &sm, report) {
				return
			}
			for i, s := range sm.Shards {
				if !repo.ValidHash(s.SHA256) {
					report(false, "shard %d has a bad hash", i+1)
				}
				if s.First > s.Last {
					report(false, "shard %d runs backwards: %q to %q", i+1, s.First, s.Last)
				}
				if i > 0 && s.First <= sm.Shards[i-1].Last {
					report(false, "shard %d overlaps the one before it: %q after %q", i+1, s.First, sm.Shards[i-1].Last)
				}
			}
		},
	}
}

// keyShard is a shard of an index by key.
func (r *Repo) keyShard(kind, addr string, s repo.ShardRange) *Node {
	n := &Node{r: r, Addr: addr, File: repo.ShardPath(kind, valid(s.SHA256)), Hash: s.SHA256, Bytes: s.Bytes}
	if s.First != "" {
		n.Summary = fmt.Sprintf("%q to %q, %s", s.First, s.Last, size(s.Bytes))
	}
	inRange := func(key string, report Report) {
		if s.First != "" && (key < s.First || key > s.Last) {
			report(false, "%q isn't between %q and %q", key, s.First, s.Last)
		}
	}
	base := strings.TrimSuffix(addr, addr[strings.LastIndex(addr, "/shards/"):])
	switch {
	case kind == "words":
		n.Kind = "word-shard"
		read := func(ctx context.Context) (*repo.WordShard, error) {
			var sh repo.WordShard
			return &sh, r.get(ctx, n.File, &sh)
		}
		n.children = func(ctx context.Context, yield func(*Node) error) error {
			sh, err := read(ctx)
			if err != nil {
				return err
			}
			for _, word := range slices.Sorted(maps.Keys(sh.Words)) {
				if err := yield(r.word(base, word, sh.Words[word])); err != nil {
					return err
				}
			}
			return nil
		}
		n.check = func(_ context.Context, data []byte, report Report) {
			var sh repo.WordShard
			if !decode(data, &sh, report) {
				return
			}
			for word, postings := range sh.Words {
				inRange(word, report)
				if len(postings)%2 != 0 {
					report(false, "%q has an odd number of doc ids and scores", word)
				}
			}
		}
	case kind == "grams":
		n.Kind = "gram-shard"
		read := func(ctx context.Context) (*repo.GramShard, error) {
			var sh repo.GramShard
			return &sh, r.get(ctx, n.File, &sh)
		}
		n.children = func(ctx context.Context, yield func(*Node) error) error {
			sh, err := read(ctx)
			if err != nil {
				return err
			}
			for _, gram := range slices.Sorted(maps.Keys(sh.Grams)) {
				if err := yield(r.gram(base, gram, sh.Grams[gram], sh.Pages[gram])); err != nil {
					return err
				}
			}
			return nil
		}
		n.check = func(_ context.Context, data []byte, report Report) {
			var sh repo.GramShard
			if !decode(data, &sh, report) {
				return
			}
			for gram, words := range sh.Grams {
				inRange(gram, report)
				if len(words) != len(sh.Pages[gram]) {
					report(false, "%q has %d words, but %d counts", gram, len(words), len(sh.Pages[gram]))
				}
			}
		}
	default:
		n.Kind = "source-shard"
		read := func(ctx context.Context) (*repo.SourceShard, error) {
			var sh repo.SourceShard
			return &sh, r.get(ctx, n.File, &sh)
		}
		n.cat = func(ctx context.Context, w io.Writer) error {
			sh, err := read(ctx)
			if err != nil {
				return err
			}
			for _, k := range sh.Sources {
				fmt.Fprintln(w, k)
			}
			return nil
		}
		n.describe = func(ctx context.Context, w io.Writer) error {
			sh, err := read(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "versions: %d, by the first %d digits of their source hashes\n", len(sh.Sources), sh.Length)
			return nil
		}
		n.check = func(_ context.Context, data []byte, report Report) {
			var sh repo.SourceShard
			if !decode(data, &sh, report) {
				return
			}
			if sh.Length != repo.SourceKeyLength {
				report(false, "its keys are %d digits, not %d", sh.Length, repo.SourceKeyLength)
			}
			for i, k := range sh.Sources {
				inRange(k, report)
				if len(k) != sh.Length {
					report(false, "%q isn't %d digits", k, sh.Length)
				}
				if i > 0 && k <= sh.Sources[i-1] {
					report(false, "%q isn't in order", k)
				}
			}
		}
	}
	return n
}

// word is a word in the search index: the pages it's in, by doc id and score.
func (r *Repo) word(base, word string, postings []int) *Node {
	return &Node{
		r: r, Kind: "word", Addr: base + "/" + word, Summary: plural(len(postings)/2, "page"),
		value: func(context.Context) (any, error) { return postings, nil },
		cat: func(ctx context.Context, w io.Writer) error {
			for i := 0; i+1 < len(postings); i += 2 {
				name, err := r.pageName(ctx, postings[i])
				if err != nil {
					return err
				}
				fmt.Fprintf(w, "%6d  %-8d %s\n", postings[i+1], postings[i], name)
			}
			return nil
		},
	}
}

// gram is a trigram in the search index: the words containing it.
func (r *Repo) gram(base, gram string, words []string, pages []int) *Node {
	return &Node{
		r: r, Kind: "gram", Addr: base + "/" + gram, Summary: plural(len(words), "word"),
		value: func(context.Context) (any, error) {
			return map[string]any{"words": words, "pages": pages}, nil
		},
		cat: func(_ context.Context, w io.Writer) error {
			for i, word := range words {
				count := 0
				if i < len(pages) {
					count = pages[i]
				}
				fmt.Fprintf(w, "%-40s %s\n", word, plural(count, "page"))
			}
			return nil
		},
	}
}

// table is one part of the page table: names or docs.
func (r *Repo) table(kind, sha string) *Node {
	read := func(ctx context.Context) (*repo.TableManifest, error) {
		var tm repo.TableManifest
		return &tm, r.get(ctx, repo.IndexManifestPath(kind, valid(sha)), &tm)
	}
	return &Node{
		r: r, Kind: "table-manifest", Addr: "index/" + kind, File: repo.IndexManifestPath(kind, valid(sha)), Hash: sha,
		Summary: "the page table's " + kind + ", by doc id",
		children: func(ctx context.Context, yield func(*Node) error) error {
			tm, err := read(ctx)
			if err != nil {
				return err
			}
			for i, s := range tm.Shards {
				if err := yield(r.tableShard(kind, fmt.Sprintf("index/%s/shards/%d", kind, i+1), s)); err != nil {
					return err
				}
			}
			return nil
		},
		describe: func(ctx context.Context, w io.Writer) error {
			tm, err := read(ctx)
			if err != nil {
				return err
			}
			var total int64
			for _, s := range tm.Shards {
				total += s.Bytes
			}
			fmt.Fprintf(w, "doc ids: %d\nshards: %d, %s\n", tm.Count, len(tm.Shards), size(total))
			return nil
		},
		check: func(_ context.Context, data []byte, report Report) {
			var tm repo.TableManifest
			if !decode(data, &tm, report) {
				return
			}
			next := 0
			for i, s := range tm.Shards {
				if s.First != next {
					report(false, "shard %d starts at id %d, not %d", i+1, s.First, next)
				}
				next = s.Last + 1
			}
			if next != tm.Count {
				report(false, "its shards go up to id %d, but it says %d", next-1, tm.Count-1)
			}
		},
	}
}

// tableShard is a run of the page table.
func (r *Repo) tableShard(kind, addr string, s repo.IDRange) *Node {
	n := &Node{r: r, Addr: addr, File: repo.ShardPath(kind, valid(s.SHA256)), Hash: s.SHA256, Bytes: s.Bytes}
	if s.Last > 0 {
		n.Summary = fmt.Sprintf("ids %d to %d, %s", s.First, s.Last, size(s.Bytes))
	}
	count := func(first, have int, report Report) {
		if s.Last > 0 && (first != s.First || have != s.Last-s.First+1) {
			report(false, "it has ids %d to %d, not %d to %d", first, first+have-1, s.First, s.Last)
		}
	}
	if kind == "names" {
		n.Kind = "name-shard"
		n.children = func(ctx context.Context, yield func(*Node) error) error {
			var sh repo.NameShard
			if err := r.get(ctx, n.File, &sh); err != nil {
				return err
			}
			for i, p := range sh.Names {
				if err := yield(r.nameRow(sh.First+i, p)); err != nil {
					return err
				}
			}
			return nil
		}
		n.check = func(_ context.Context, data []byte, report Report) {
			var sh repo.NameShard
			if decode(data, &sh, report) {
				count(sh.First, len(sh.Names), report)
			}
		}
		return n
	}
	n.Kind = "doc-shard"
	n.children = func(ctx context.Context, yield func(*Node) error) error {
		var sh repo.DocShard
		if err := r.get(ctx, n.File, &sh); err != nil {
			return err
		}
		for i, d := range sh.Docs {
			if err := yield(r.docRow(sh.First+i, d)); err != nil {
				return err
			}
		}
		return nil
	}
	n.check = func(_ context.Context, data []byte, report Report) {
		var sh repo.DocShard
		if decode(data, &sh, report) {
			count(sh.First, len(sh.Docs), report)
		}
	}
	return n
}

func (r *Repo) nameRow(id int, p repo.PageName) *Node {
	summary := p.Name + "(" + p.Section + ")"
	if p.Name == "" {
		summary = "(no page has this id any more)"
	}
	return &Node{
		r: r, Kind: "name", Addr: "index/names/" + strconv.Itoa(id), Summary: summary, NoWalk: true,
		value: func(context.Context) (any, error) { return p, nil },
	}
}

func (r *Repo) docRow(id int, d repo.PageDoc) *Node {
	return &Node{
		r: r, Kind: "doc", Addr: "index/docs/" + strconv.Itoa(id), Summary: d.Description, NoWalk: true,
		value: func(context.Context) (any, error) { return d, nil },
		describe: func(_ context.Context, w io.Writer) error {
			fmt.Fprintf(w, "sources: %s\n", d.Sources)
			return nil
		},
	}
}

// tableRange finds the shard of a part of the page table with an id.
func (r *Repo) tableRange(ctx context.Context, kind, sha string, id int) (repo.IDRange, error) {
	var tm repo.TableManifest
	if err := r.get(ctx, repo.IndexManifestPath(kind, valid(sha)), &tm); err != nil {
		return repo.IDRange{}, err
	}
	for _, s := range tm.Shards {
		if id >= s.First && id <= s.Last {
			return s, nil
		}
	}
	return repo.IDRange{}, fmt.Errorf("no doc id %d: %w", id, repo.ErrNotFound)
}

// pageName is what a doc id is called, as name(section).
func (r *Repo) pageName(ctx context.Context, id int) (string, error) {
	m, err := r.Manifest(ctx)
	if err != nil {
		return "", err
	}
	s, err := r.tableRange(ctx, "names", m.Index.Names, id)
	if err != nil {
		return "", err
	}
	var sh repo.NameShard
	if err := r.get(ctx, repo.ShardPath("names", valid(s.SHA256)), &sh); err != nil {
		return "", err
	}
	if id < sh.First || id-sh.First >= len(sh.Names) {
		return "", fmt.Errorf("no doc id %d in its shard", id)
	}
	p := sh.Names[id-sh.First]
	return p.Name + "(" + p.Section + ")", nil
}

// packageIndex is the index of every package.
func (r *Repo) packageIndex(sha string) *Node {
	file := repo.IndexManifestPath("packages", valid(sha))
	read := func(ctx context.Context) (*repo.PackageIndex, error) {
		var pi repo.PackageIndex
		return &pi, r.get(ctx, file, &pi)
	}
	return &Node{
		r: r, Kind: "package-index", Addr: "index/packages", File: file, Hash: sha, Summary: "every package, A to Z",
		children: func(ctx context.Context, yield func(*Node) error) error {
			pi, err := read(ctx)
			if err != nil {
				return err
			}
			for i, c := range pi.Chunks {
				if err := yield(r.packageChunk(fmt.Sprintf("index/packages/shards/%d", i+1), c)); err != nil {
					return err
				}
			}
			return nil
		},
		describe: func(ctx context.Context, w io.Writer) error {
			pi, err := read(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "packages: %d, in %s of %d\n", pi.Count, plural(len(pi.Chunks), "chunk"), repo.PackageChunk)
			counts(w, "by source", pi.Sources)
			return nil
		},
		check: func(_ context.Context, data []byte, report Report) {
			var pi repo.PackageIndex
			if !decode(data, &pi, report) {
				return
			}
			total := 0
			for _, c := range pi.Chunks {
				total += c.Count
			}
			if total != pi.Count {
				report(false, "its chunks come to %d packages, but it says %d", total, pi.Count)
			}
		},
	}
}

// packageChunk is a chunk of the package index.
func (r *Repo) packageChunk(addr string, c repo.ChapterShardRef) *Node {
	file := repo.ShardPath("packages", valid(c.SHA256))
	n := &Node{r: r, Kind: "package-chunk", Addr: addr, File: file, Hash: c.SHA256, Bytes: c.Bytes}
	if c.Count > 0 {
		n.Summary = fmt.Sprintf("%s, %s to %s", plural(c.Count, "package"), c.First, c.Last)
	}
	n.children = func(ctx context.Context, yield func(*Node) error) error {
		var sh repo.PackageShard
		if err := r.get(ctx, file, &sh); err != nil {
			return err
		}
		for _, row := range sh.Packages {
			if err := yield(r.packageRow(row)); err != nil {
				return err
			}
		}
		return nil
	}
	n.check = func(_ context.Context, data []byte, report Report) {
		var sh repo.PackageShard
		if !decode(data, &sh, report) {
			return
		}
		if c.Count > 0 && len(sh.Packages) != c.Count {
			report(false, "it has %d packages, but its index says %d", len(sh.Packages), c.Count)
		}
		for _, row := range sh.Packages {
			if !repo.ValidHash(row.List) {
				report(false, "package %s has a bad list hash", row.Name)
			}
		}
	}
	return n
}

// packageRow is a package, in the package index.
func (r *Repo) packageRow(row repo.PackageRow) *Node {
	addr := "index/packages/" + row.Name
	return &Node{
		r: r, Kind: "package", Addr: addr, Summary: fmt.Sprintf("%s: %s", plural(row.Pages, "page"), row.Sample),
		value: func(context.Context) (any, error) {
			return map[string]any{"name": row.Name, "pages": row.Pages, "sources": row.Sources, "sample": row.Sample, "list": row.List}, nil
		},
		children: func(_ context.Context, yield func(*Node) error) error {
			return yield(r.packageList(addr+"/pages", row.Name, row.Pages, row.List))
		},
		describe: func(_ context.Context, w io.Writer) error {
			counts(w, "by source", row.Sources)
			return nil
		},
	}
}

// packageList is a package's pages.
func (r *Repo) packageList(addr, name string, count int, sha string) *Node {
	file := repo.ShardPath("package", valid(sha))
	read := func(ctx context.Context) (*repo.PackagePages, error) {
		var pp repo.PackagePages
		return &pp, r.get(ctx, file, &pp)
	}
	return &Node{
		r: r, Kind: "package-pages", Addr: addr, File: file, Hash: sha, Summary: plural(count, "page"),
		children: func(ctx context.Context, yield func(*Node) error) error {
			pp, err := read(ctx)
			if err != nil {
				return err
			}
			return r.pagesIn(pp.Pages, "", func(p *Node) error {
				// (Checked by way of their chapters.)
				p.View = true
				return yield(p)
			})
		},
		cat: func(ctx context.Context, w io.Writer) error {
			pp, err := read(ctx)
			if err != nil {
				return err
			}
			for _, p := range pp.Pages {
				fmt.Fprintln(w, pageLine(p))
			}
			return nil
		},
		check: func(_ context.Context, data []byte, report Report) {
			var pp repo.PackagePages
			if !decode(data, &pp, report) {
				return
			}
			if name != "" && pp.Package != name {
				report(false, "it's package %q's", pp.Package)
			}
			if count != 0 && len(pp.Pages) != count {
				report(false, "it has %d pages, but its index says %d", len(pp.Pages), count)
			}
		},
	}
}

// findPackage finds a package in the package index.
func (r *Repo) findPackage(ctx context.Context, name string) (*Node, error) {
	m, err := r.Manifest(ctx)
	if err != nil {
		return nil, err
	}
	if m.Index.Packages == "" {
		return nil, fmt.Errorf("it has no package index: %w", repo.ErrNotFound)
	}
	var pi repo.PackageIndex
	if err := r.get(ctx, repo.IndexManifestPath("packages", valid(m.Index.Packages)), &pi); err != nil {
		return nil, err
	}
	name = strings.ToLower(name)
	for _, c := range pi.Chunks {
		if manpage.ListingLess(name, "", c.First, "") || manpage.ListingLess(c.Last, "", name, "") {
			continue
		}
		var sh repo.PackageShard
		if err := r.get(ctx, repo.ShardPath("packages", valid(c.SHA256)), &sh); err != nil {
			return nil, err
		}
		for _, row := range sh.Packages {
			if row.Name == name {
				return r.packageRow(row), nil
			}
		}
	}
	return nil, fmt.Errorf("no package %s: %w", name, repo.ErrNotFound)
}
