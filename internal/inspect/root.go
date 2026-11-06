// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package inspect

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// Root is manifest.json.
func (r *Repo) Root() *Node {
	return &Node{
		r: r, Kind: "manifest", Addr: "/", File: repo.ManifestPath(),
		Summary: "the repository",
		children: func(ctx context.Context, yield func(*Node) error) error {
			m, err := r.Manifest(ctx)
			if err != nil {
				return err
			}
			for _, g := range []struct {
				name string
				n    int
				what string
			}{{"chapters", len(m.Chapters), "chapter"}, {"sections", len(m.Sections), "section"},
				{"pages", m.Pages, "page"}, {"sources", len(m.Sources), "source"}, {"packs", len(m.Packs), "pack"}} {
				if g.n == 0 {
					continue
				}
				if err := yield(r.group(g.name, plural(g.n, g.what))); err != nil {
					return err
				}
			}
			if m.Index.Words != "" {
				return yield(r.group("index", "the search, variant and package indexes"))
			}
			return nil
		},
		describe: func(ctx context.Context, w io.Writer) error {
			m, err := r.Manifest(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "published: %s\npages: %d\n", m.Generated.Format(time.RFC3339), m.Pages)
			if len(m.Chapters) == 0 && len(m.Packs) > 0 {
				fmt.Fprintln(w, "pack-only: it has nothing but its packs (see packs)")
			}
			counts(w, "variants", m.Variants)
			if len(m.Chapters) > 0 {
				fmt.Fprintf(w, "chapters: %d\nsections: %d\n", len(m.Chapters), len(m.Sections))
			}
			fmt.Fprintf(w, "sources: %d\npacks: %d\n", len(m.Sources), len(m.Packs))
			return nil
		},
		check: func(ctx context.Context, data []byte, report Report) {
			var m repo.Manifest
			if !decode(data, &m, report) {
				return
			}
			sum := 0
			for i, c := range m.Chapters {
				sum += c.Pages
				if !repo.ValidHash(c.SHA256) {
					report(false, "chapter %s has a bad hash %q", c.Chapter, c.SHA256)
				}
				if i > 0 && c.Chapter <= m.Chapters[i-1].Chapter {
					report(false, "chapter %s isn't in order", c.Chapter)
				}
			}
			if len(m.Chapters) > 0 && sum != m.Pages {
				report(false, "its chapters have %d pages, but it says %d", sum, m.Pages)
			}
			sum = 0
			for i, s := range m.Sections {
				sum += s.Pages
				if i > 0 && s.Section <= m.Sections[i-1].Section {
					report(false, "section %s isn't in order", s.Section)
				}
			}
			if len(m.Sections) > 0 && sum != m.Pages {
				report(false, "its sections have %d pages, but it says %d", sum, m.Pages)
			}
			for i, p := range m.Packs {
				if p.Path != repo.PackPath(p.SHA256) {
					report(true, "pack %s is at %s", p.SHA256, p.Path)
				}
				if i > 0 && p.Seq <= m.Packs[i-1].Seq {
					report(false, "pack %d isn't in order", p.Seq)
				}
			}
		},
	}
}

// group is one of the things directly under the root.
func (r *Repo) group(name, summary string) *Node {
	n := &Node{r: r, Kind: "group", Addr: name, Summary: summary}
	switch name {
	case "chapters":
		n.value = func(ctx context.Context) (any, error) {
			m, err := r.Manifest(ctx)
			if err != nil {
				return nil, err
			}
			return m.Chapters, nil
		}
		n.children = func(ctx context.Context, yield func(*Node) error) error {
			m, err := r.Manifest(ctx)
			if err != nil {
				return err
			}
			for _, c := range m.Chapters {
				if err := yield(r.chapter(c)); err != nil {
					return err
				}
			}
			return nil
		}
	case "sections":
		n.value = func(ctx context.Context) (any, error) {
			m, err := r.Manifest(ctx)
			if err != nil {
				return nil, err
			}
			return m.Sections, nil
		}
		n.children = func(ctx context.Context, yield func(*Node) error) error {
			m, err := r.Manifest(ctx)
			if err != nil {
				return err
			}
			for _, s := range m.Sections {
				if err := yield(r.section(s)); err != nil {
					return err
				}
			}
			return nil
		}
	case "pages":
		// All the chapters' pages: the way to them, for reading, not checking.
		n.View = true
		n.children = func(ctx context.Context, yield func(*Node) error) error {
			m, err := r.Manifest(ctx)
			if err != nil {
				return err
			}
			for _, c := range m.Chapters {
				if err := r.chapterPages(ctx, c, "", yield); err != nil {
					return err
				}
			}
			return nil
		}
	case "sources":
		n.value = func(ctx context.Context) (any, error) {
			m, err := r.Manifest(ctx)
			if err != nil {
				return nil, err
			}
			return m.Sources, nil
		}
		n.children = func(ctx context.Context, yield func(*Node) error) error {
			m, err := r.Manifest(ctx)
			if err != nil {
				return err
			}
			for _, s := range m.Sources {
				if err := yield(r.source(s)); err != nil {
					return err
				}
			}
			return nil
		}
	case "packs":
		r.packsGroup(n)
	case "index":
		r.indexGroup(n)
	}
	return n
}

func (r *Repo) source(s repo.SourceEntry) *Node {
	return &Node{
		r: r, Kind: "source", Addr: "sources/" + s.Key(), Summary: plural(s.Pages, "page"),
		value: func(context.Context) (any, error) { return s, nil },
		describe: func(_ context.Context, w io.Writer) error {
			fmt.Fprintf(w, "pages: %d\n", s.Pages)
			counts(w, "by chapter", s.Sections)
			return nil
		},
	}
}

// counts writes a map of counts, in order.
func counts(w io.Writer, what string, m map[string]int) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(w, "%s:\n", what)
	for _, k := range keys {
		fmt.Fprintf(w, "  %-24s %d\n", k, m[k])
	}
}

// chapter is a chapter's index.
func (r *Repo) chapter(e repo.ChapterEntry) *Node {
	read := func(ctx context.Context) (*repo.ChapterIndex, error) {
		var ci repo.ChapterIndex
		return &ci, r.get(ctx, repo.SectionIndexPath(e.Chapter, valid(e.SHA256)), &ci)
	}
	return &Node{
		r: r, Kind: "chapter", Addr: "chapters/" + e.Chapter, File: repo.SectionIndexPath(e.Chapter, valid(e.SHA256)), Hash: e.SHA256,
		Summary: plural(e.Pages, "page"),
		children: func(ctx context.Context, yield func(*Node) error) error {
			ci, err := read(ctx)
			if err != nil {
				return err
			}
			if err := yield(r.chapterPointer(e.Chapter)); err != nil {
				return err
			}
			m, err := r.Manifest(ctx)
			if err != nil {
				return err
			}
			for _, s := range m.Sections {
				if manpage.Chapter(s.Section) == e.Chapter {
					sec := r.section(s)
					sec.Addr = "chapters/" + e.Chapter + "/sections/" + s.Section
					sec.View = true
					if err := yield(sec); err != nil {
						return err
					}
				}
			}
			if err := yield(r.chapterChunk(e.Chapter, 1, ci)); err != nil {
				return err
			}
			for i, ref := range ci.Shards {
				if err := yield(r.chapterShard(e.Chapter, i+2, ref)); err != nil {
					return err
				}
			}
			return nil
		},
		describe: func(ctx context.Context, w io.Writer) error {
			ci, err := read(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "pages: %d, of which %d here and the rest in %s\n", ci.Count, len(ci.Pages), plural(len(ci.Shards), "shard"))
			if len(ci.Letters) > 0 {
				fmt.Fprint(w, "letters (where each starts, by chunk):")
				for _, l := range ci.Letters {
					fmt.Fprintf(w, " %s:%d", l.Letter, l.Chunk)
				}
				fmt.Fprintln(w)
			}
			return nil
		},
		cat: func(ctx context.Context, w io.Writer) error {
			return r.chapterPages(ctx, e, "", func(p *Node) error {
				_, err := fmt.Fprintln(w, p.Summary)
				return err
			})
		},
		check: func(ctx context.Context, data []byte, report Report) {
			var ci repo.ChapterIndex
			if !decode(data, &ci, report) {
				return
			}
			if ci.Chapter != e.Chapter {
				report(false, "it's chapter %q's", ci.Chapter)
			}
			total := len(ci.Pages)
			for _, s := range ci.Shards {
				total += s.Count
			}
			if total != ci.Count {
				report(false, "its pages and shards come to %d, but it says %d", total, ci.Count)
			}
			if e.Pages != 0 && ci.Count != e.Pages {
				report(false, "it has %d pages, but the manifest says %d", ci.Count, e.Pages)
			}
			checkOrder(ci.Pages, report)
			for _, l := range ci.Letters {
				if l.Chunk < 1 || l.Chunk > len(ci.Shards)+1 {
					report(false, "letter %s is in chunk %d, of %d", l.Letter, l.Chunk, len(ci.Shards)+1)
				}
			}
			var pointer repo.Pointer
			if err := r.get(ctx, repo.SectionPointerPath(e.Chapter), &pointer); err != nil {
				report(false, "its pointer: %v", err)
			} else if e.SHA256 != "" && pointer.SHA256 != e.SHA256 {
				report(false, "its pointer says %s, but the manifest says %s", pointer.SHA256, e.SHA256)
			}
		},
	}
}

// chapterPointer is section/<chapter>.json.
func (r *Repo) chapterPointer(chapter string) *Node {
	return &Node{
		r: r, Kind: "pointer", Addr: "chapters/" + chapter + "/pointer", File: repo.SectionPointerPath(chapter),
		Summary: "to its current index",
		check: func(_ context.Context, data []byte, report Report) {
			var p repo.Pointer
			if !decode(data, &p, report) {
				return
			}
			if p.Section != chapter {
				report(false, "it's for section %q", p.Section)
			}
			if !repo.ValidHash(p.SHA256) {
				report(false, "it has a bad hash %q", p.SHA256)
			}
		},
	}
}

// pagesIn yields a list of a chapter's pages.
func (r *Repo) pagesIn(pages []repo.IndexPage, section string, yield func(*Node) error) error {
	for _, p := range pages {
		if section != "" && p.Section != section {
			continue
		}
		if err := yield(r.pageRow(p)); err != nil {
			return err
		}
	}
	return nil
}

// chapterPages yields a chapter's pages, or a section's of it.
func (r *Repo) chapterPages(ctx context.Context, e repo.ChapterEntry, section string, yield func(*Node) error) error {
	var ci repo.ChapterIndex
	if err := r.get(ctx, repo.SectionIndexPath(e.Chapter, valid(e.SHA256)), &ci); err != nil {
		return err
	}
	if err := r.pagesIn(ci.Pages, section, yield); err != nil {
		return err
	}
	for _, ref := range ci.Shards {
		var sh repo.ChapterShard
		if err := r.get(ctx, repo.SectionShardPath(valid(ref.SHA256)), &sh); err != nil {
			return err
		}
		if err := r.pagesIn(sh.Pages, section, yield); err != nil {
			return err
		}
	}
	return nil
}

// chapterChunk is the first chunk of a chapter's pages: in its index.
func (r *Repo) chapterChunk(chapter string, n int, ci *repo.ChapterIndex) *Node {
	first, last := "", ""
	if len(ci.Pages) > 0 {
		first, last = ci.Pages[0].Name, ci.Pages[len(ci.Pages)-1].Name
	}
	return &Node{
		r: r, Kind: "chapter-shard", Addr: fmt.Sprintf("chapters/%s/shards/%d", chapter, n),
		Summary: fmt.Sprintf("%s, %s to %s (in the chapter's index)", plural(len(ci.Pages), "page"), first, last),
		value:   func(context.Context) (any, error) { return ci.Pages, nil },
		children: func(_ context.Context, yield func(*Node) error) error {
			return r.pagesIn(ci.Pages, "", yield)
		},
		cat: func(_ context.Context, w io.Writer) error {
			return r.pagesIn(ci.Pages, "", func(p *Node) error {
				_, err := fmt.Fprintln(w, p.Summary)
				return err
			})
		},
	}
}

// chapterShard is the rest of a chapter's pages, chunk n of them.
func (r *Repo) chapterShard(chapter string, n int, ref repo.ChapterShardRef) *Node {
	read := func(ctx context.Context) (*repo.ChapterShard, error) {
		var sh repo.ChapterShard
		return &sh, r.get(ctx, repo.SectionShardPath(valid(ref.SHA256)), &sh)
	}
	return &Node{
		r: r, Kind: "chapter-shard", Addr: fmt.Sprintf("chapters/%s/shards/%d", chapter, n), File: repo.SectionShardPath(valid(ref.SHA256)),
		Hash: ref.SHA256, Bytes: ref.Bytes,
		Summary: fmt.Sprintf("%s, %s to %s", plural(ref.Count, "page"), ref.First, ref.Last),
		children: func(ctx context.Context, yield func(*Node) error) error {
			sh, err := read(ctx)
			if err != nil {
				return err
			}
			return r.pagesIn(sh.Pages, "", yield)
		},
		cat: func(ctx context.Context, w io.Writer) error {
			sh, err := read(ctx)
			if err != nil {
				return err
			}
			return r.pagesIn(sh.Pages, "", func(p *Node) error {
				_, err := fmt.Fprintln(w, p.Summary)
				return err
			})
		},
		check: func(_ context.Context, data []byte, report Report) {
			var sh repo.ChapterShard
			if !decode(data, &sh, report) {
				return
			}
			if chapter != "" && sh.Chapter != chapter {
				report(false, "it's chapter %q's", sh.Chapter)
			}
			if ref.Count != 0 && len(sh.Pages) != ref.Count {
				report(false, "it has %d pages, but its chapter says %d", len(sh.Pages), ref.Count)
			}
			checkOrder(sh.Pages, report)
		},
	}
}

// section is a view of a chapter's pages.
func (r *Repo) section(s repo.SectionEntry) *Node {
	chapter := manpage.Chapter(s.Section)
	return &Node{
		r: r, Kind: "section", Addr: "sections/" + s.Section, Summary: plural(s.Pages, "page"),
		value: func(context.Context) (any, error) { return s, nil },
		children: func(ctx context.Context, yield func(*Node) error) error {
			m, err := r.Manifest(ctx)
			if err != nil {
				return err
			}
			for _, c := range m.Chapters {
				if c.Chapter == chapter {
					return r.chapterPages(ctx, c, s.Section, yield)
				}
			}
			return fmt.Errorf("no chapter %s", chapter)
		},
		describe: func(_ context.Context, w io.Writer) error {
			fmt.Fprintf(w, "chapter: %s (a section has no file of its own: these are that chapter's pages)\npages: %d\n", chapter, s.Pages)
			counts(w, "variants", s.Variants)
			return nil
		},
		cat: func(ctx context.Context, w io.Writer) error {
			m, err := r.Manifest(ctx)
			if err != nil {
				return err
			}
			for _, c := range m.Chapters {
				if c.Chapter == chapter {
					return r.chapterPages(ctx, c, s.Section, func(p *Node) error {
						_, err := fmt.Fprintln(w, p.Summary)
						return err
					})
				}
			}
			return nil
		},
	}
}

// checkOrder checks pages are in the order listings show them. Names differing
// only in case (lacheck, LAcheck) count as the same, so either may be first.
func checkOrder(pages []repo.IndexPage, report Report) {
	for i := 1; i < len(pages); i++ {
		a, b := pages[i-1], pages[i]
		if manpage.ListingLess(b.Name, b.Section, a.Name, a.Section) {
			report(false, "%s(%s) comes before %s(%s), out of order", a.Name, a.Section, b.Name, b.Section)
		}
	}
}

// pageLine is a page, in a line.
func pageLine(p repo.IndexPage) string {
	bits := []string{p.Name + "(" + p.Section + ")"}
	if p.Version != "" {
		bits = append(bits, p.Version)
	}
	if p.Versions > 1 {
		bits = append(bits, plural(p.Versions, "version"))
	}
	if len(p.Variants) > 0 {
		bits = append(bits, "["+strings.Join(p.Variants, " ")+"]")
	}
	if p.Description != "" {
		bits = append(bits, "— "+p.Description)
	}
	return strings.Join(bits, " ")
}
