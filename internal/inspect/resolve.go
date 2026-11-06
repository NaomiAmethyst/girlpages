// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package inspect

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/NaomiAmethyst/girlpages/internal/remote"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// Resolve finds what an address names. Addresses are the paths ls shows:
//
//	/                                    the repository: manifest.json
//	chapters/3                           a chapter's index
//	chapters/3/pointer                   its pointer
//	chapters/3/shards/2                  a chunk of its pages
//	chapters/3/sections/3ssl             a section of it
//	sections/3ssl                        the same
//	pages                                every page
//	pages/ls(1)                          a page's listing (or just ls(1), or ls)
//	pages/ls(1)/pointer                  its pointer
//	pages/ls(1)/9.12                     a version (or ls(1)@9.12)
//	pages/ls(1)/debian/trixie            what a source has
//	pages/ls(1)/latest                   the current version
//	pages/ls(1)/9.12/contents            its variants.json
//	pages/ls(1)/9.12/girl                a variant (or ls(1)/girl, of the latest)
//	pages/ls(1)/9.12/girl/generations/1  a generation of it
//	pages/ls(1)/9.12/girl/headings/2     a heading in it, by number or name
//	sources/debian/trixie                a source
//	index/words                          an index's manifest; so grams, names,
//	                                     docs, packages, variants/girl
//	index/words/shards/3                 one of its shards
//	index/words/grep                     an entry: a word, a trigram, a doc id
//	                                     (index/names/12), or a package
//	packs/2                              a pack, by its place in the log
//	packs/log                            packs/index.json
//
// or a file's path in the repository: page/page-ab/<sha>.json.
func (r *Repo) Resolve(ctx context.Context, addr string) (*Node, error) {
	n, err := r.resolve(ctx, strings.TrimSpace(addr))
	if errors.Is(err, repo.ErrNotFound) {
		if m, merr := r.Manifest(ctx); merr == nil && len(m.Chapters) == 0 && len(m.Packs) > 0 {
			return nil, fmt.Errorf("%w (this repository is pack-only: it has nothing but its packs)", err)
		}
	}
	return n, err
}

func (r *Repo) resolve(ctx context.Context, addr string) (*Node, error) {
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "./"), "/")
	addr = strings.TrimSuffix(addr, "/")
	if addr == "" {
		return r.Root(), nil
	}
	if isFile(addr) {
		return r.fromFile(ctx, addr)
	}
	first, rest, _ := strings.Cut(addr, "/")
	var segs []string
	if rest != "" {
		segs = strings.Split(rest, "/")
	}
	switch first {
	case "chapters":
		return r.resolveChapter(ctx, segs)
	case "sections":
		return r.resolveSection(ctx, segs)
	case "pages":
		if len(segs) == 0 {
			return r.group("pages", "every page"), nil
		}
		return r.resolvePage(ctx, segs)
	case "sources":
		return r.resolveSource(ctx, segs)
	case "index":
		return r.resolveIndex(ctx, segs)
	case "packs":
		return r.resolvePack(ctx, rest)
	}
	return r.resolvePage(ctx, strings.Split(addr, "/"))
}

func missing(format string, a ...any) error {
	return fmt.Errorf(format+": %w", append(a, repo.ErrNotFound)...)
}

func (r *Repo) resolveChapter(ctx context.Context, segs []string) (*Node, error) {
	if len(segs) == 0 {
		return r.group("chapters", "every chapter"), nil
	}
	m, err := r.Manifest(ctx)
	if err != nil {
		return nil, err
	}
	var e *repo.ChapterEntry
	for i := range m.Chapters {
		if m.Chapters[i].Chapter == segs[0] {
			e = &m.Chapters[i]
		}
	}
	if e == nil {
		return nil, missing("no chapter %s", segs[0])
	}
	n := r.chapter(*e)
	switch {
	case len(segs) == 1:
		return n, nil
	case len(segs) == 2 && segs[1] == "pointer":
		return r.chapterPointer(e.Chapter), nil
	case len(segs) == 3 && segs[1] == "sections":
		s, err := r.resolveSection(ctx, segs[2:])
		if err != nil {
			return nil, err
		}
		s.Addr = "chapters/" + e.Chapter + "/sections/" + segs[2]
		return s, nil
	case len(segs) == 3 && segs[1] == "shards":
		num, err := strconv.Atoi(segs[2])
		if err != nil {
			return nil, missing("no shard %q of chapter %s", segs[2], e.Chapter)
		}
		var ci repo.ChapterIndex
		if err := r.get(ctx, n.File, &ci); err != nil {
			return nil, err
		}
		if num == 1 {
			return r.chapterChunk(e.Chapter, 1, &ci), nil
		}
		if num < 1 || num-2 >= len(ci.Shards) {
			return nil, missing("no shard %d of chapter %s, which has %d", num, e.Chapter, len(ci.Shards)+1)
		}
		return r.chapterShard(e.Chapter, num, ci.Shards[num-2]), nil
	}
	return nil, missing("nothing at chapters/%s", strings.Join(segs, "/"))
}

func (r *Repo) resolveSection(ctx context.Context, segs []string) (*Node, error) {
	if len(segs) == 0 {
		return r.group("sections", "every section"), nil
	}
	m, err := r.Manifest(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range m.Sections {
		if s.Section == segs[0] && len(segs) == 1 {
			return r.section(s), nil
		}
	}
	return nil, missing("no section %s", strings.Join(segs, "/"))
}

func (r *Repo) resolveSource(ctx context.Context, segs []string) (*Node, error) {
	if len(segs) == 0 {
		return r.group("sources", "where pages come from"), nil
	}
	m, err := r.Manifest(ctx)
	if err != nil {
		return nil, err
	}
	key := strings.Join(segs, "/")
	for _, s := range m.Sources {
		if s.Key() == key {
			return r.source(s), nil
		}
	}
	return nil, missing("no source %s", key)
}

var pageForm = regexp.MustCompile(`^(.+)\(([^()/]+)\)(?:@(.+))?$`)

// man's order, for a name with no section.
var manOrder = []string{"1", "n", "l", "8", "3", "0", "2", "5", "4", "9", "6", "7", "rfc"}

// resolvePage resolves name(section)[@version] and what's under it.
func (r *Repo) resolvePage(ctx context.Context, segs []string) (*Node, error) {
	head := segs[0]
	rest := segs[1:]
	if unescaped, err := url.PathUnescape(head); err == nil {
		head = unescaped
	}
	var name, section string
	if m := pageForm.FindStringSubmatch(head); m != nil {
		name, section = m[1], m[2]
		if m[3] != "" {
			rest = append([]string{m[3]}, rest...)
		}
	} else {
		name, at, ok := strings.Cut(head, "@")
		for _, sec := range manOrder {
			var p repo.Pointer
			err := r.get(ctx, repo.NamePath(name, sec), &p)
			if err == nil {
				if ok {
					rest = append([]string{at}, rest...)
				}
				return r.underPage(ctx, name, sec, p.SHA256, rest)
			}
			if !errors.Is(err, repo.ErrNotFound) {
				return nil, err
			}
		}
		return nil, missing("nothing called %s (pages are name(section), and other things are under /)", head)
	}
	var p repo.Pointer
	if err := r.get(ctx, repo.NamePath(name, section), &p); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, missing("no page %s(%s)", name, section)
		}
		return nil, err
	}
	return r.underPage(ctx, name, section, p.SHA256, rest)
}

func (r *Repo) underPage(ctx context.Context, name, section, sha string, rest []string) (*Node, error) {
	page := r.page(name, section, sha)
	if len(rest) == 0 {
		return page, nil
	}
	if rest[0] == "pointer" && len(rest) == 1 {
		return r.pagePointer(name, section), nil
	}
	var l repo.Listing
	if err := r.get(ctx, page.File, &l); err != nil {
		return nil, err
	}
	var v *repo.Version
	used := 1
	switch {
	case rest[0] == "latest" || rest[0] == "current":
		v = l.CurrentVersion()
	case len(rest) >= 2 && l.VersionFrom(rest[0], rest[1]) != nil:
		v, used = l.VersionFrom(rest[0], rest[1]), 2
	default:
		if got, err := remote.PickVersion(&l, rest[0]); err == nil {
			v = got
		} else if cur := l.CurrentVersion(); cur != nil {
			if _, ok := cur.Variants[rest[0]]; ok {
				v, used = cur, 0
			}
		}
	}
	if v == nil {
		slugs := make([]string, 0, len(l.Versions))
		for _, x := range versionsOf(&l) {
			slugs = append(slugs, slug(&l, x))
		}
		return nil, missing("%s(%s) has no version or variant %s (its versions: %s)", name, section, rest[0], strings.Join(slugs, ", "))
	}
	return r.underVersion(ctx, page.Addr, &l, v, rest[used:])
}

func (r *Repo) underVersion(ctx context.Context, page string, l *repo.Listing, v *repo.Version, rest []string) (*Node, error) {
	node := r.version(page, l, v)
	if len(rest) == 0 {
		return node, nil
	}
	info, isVariant := v.Variants[rest[0]]
	if !isVariant {
		if rest[0] == "contents" && len(rest) == 1 {
			return r.contents(node.Addr, v.Source), nil
		}
		return nil, missing("%s has no variant %s (it has: %s)", node.Addr, rest[0], strings.Join(variantsOf(v), ", "))
	}
	name, addr := rest[0], node.Addr+"/"+rest[0]
	rest = rest[1:]
	if len(rest) >= 2 && rest[0] == "generations" {
		num, err := strconv.Atoi(rest[1])
		if err != nil {
			return nil, missing("no generation %q", rest[1])
		}
		var cs repo.Contents
		if err := r.get(ctx, repo.ContentsPath(valid(v.Source)), &cs); err != nil {
			return nil, err
		}
		found := false
		for _, g := range cs.Variants[name] {
			if g.Generation == num {
				info, found = g, true
			}
		}
		if !found {
			return nil, missing("%s has no generation %d", addr, num)
		}
		addr = fmt.Sprintf("%s/generations/%d", addr, num)
		rest = rest[2:]
	}
	gen := r.variant(addr, v.Source, name, info)
	if len(rest) == 0 {
		return gen, nil
	}
	if rest[0] != "headings" || len(rest) != 2 {
		return nil, missing("nothing at %s/%s", addr, strings.Join(rest, "/"))
	}
	var doc repo.Variant
	if err := r.get(ctx, gen.File, &doc); err != nil {
		return nil, err
	}
	want, _ := url.PathUnescape(rest[1])
	if i, err := strconv.Atoi(want); err == nil && i >= 0 && i < len(doc.Sections) {
		return r.heading(addr, &doc, i), nil
	}
	for i, s := range doc.Sections {
		if strings.EqualFold(s.Heading, want) || strings.EqualFold(cleanHeading(s.Heading), cleanHeading(want)) {
			return r.heading(addr, &doc, i), nil
		}
	}
	return nil, missing("%s has no heading %s", addr, want)
}

var notWords = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// cleanHeading is a heading without its flourishes.
func cleanHeading(s string) string {
	return strings.ToLower(strings.TrimSpace(notWords.ReplaceAllString(s, " ")))
}

func (r *Repo) resolveIndex(ctx context.Context, segs []string) (*Node, error) {
	if len(segs) == 0 {
		return r.group("index", "the search, variant and package indexes"), nil
	}
	m, err := r.Manifest(ctx)
	if err != nil {
		return nil, err
	}
	switch segs[0] {
	case "words", "grams":
		sha := m.Index.Words
		if segs[0] == "grams" {
			sha = m.Index.Grams
		}
		if sha == "" {
			return nil, missing("it has no %s index", segs[0])
		}
		return r.underSharded(ctx, segs[0], "index/"+segs[0], sha, segs[1:])
	case "variants":
		if len(segs) == 1 {
			return r.variantIndexes(), nil
		}
		sha := m.Index.Variants[segs[1]]
		if sha == "" {
			return nil, missing("no index of variant %s", segs[1])
		}
		return r.underSharded(ctx, repo.VariantIndexKind(segs[1]), "index/variants/"+segs[1], sha, segs[2:])
	case "names", "docs":
		sha := m.Index.Names
		if segs[0] == "docs" {
			sha = m.Index.Docs
		}
		if sha == "" {
			return nil, missing("it has no %s", segs[0])
		}
		return r.underTable(ctx, segs[0], sha, segs[1:])
	case "packages":
		if m.Index.Packages == "" {
			return nil, missing("it has no package index")
		}
		return r.underPackages(ctx, m.Index.Packages, segs[1:])
	}
	return nil, missing("no index %s", segs[0])
}

func (r *Repo) underSharded(ctx context.Context, kind, addr, sha string, rest []string) (*Node, error) {
	n := r.sharded(kind, addr, sha)
	if len(rest) == 0 {
		return n, nil
	}
	var sm repo.ShardManifest
	if err := r.get(ctx, n.File, &sm); err != nil {
		return nil, err
	}
	if rest[0] == "shards" && len(rest) == 2 {
		num, err := strconv.Atoi(rest[1])
		if err != nil || num < 1 || num > len(sm.Shards) {
			return nil, missing("%s has no shard %s, of %d", addr, rest[1], len(sm.Shards))
		}
		return r.keyShard(kind, fmt.Sprintf("%s/shards/%d", addr, num), sm.Shards[num-1]), nil
	}
	key := strings.Join(rest, "/")
	if unescaped, err := url.PathUnescape(key); err == nil {
		key = unescaped
	}
	for i, s := range sm.Shards {
		if key < s.First || key > s.Last {
			continue
		}
		shard := r.keyShard(kind, fmt.Sprintf("%s/shards/%d", addr, i+1), s)
		var found *Node
		err := shard.Children(ctx, func(c *Node) error {
			if strings.HasSuffix(c.Addr, "/"+key) {
				found = c
				return errFound
			}
			return nil
		})
		if err != nil && !errors.Is(err, errFound) {
			return nil, err
		}
		if found != nil {
			return found, nil
		}
		if shard.Kind == "source-shard" {
			return nil, missing("%s isn't in %s", key, addr)
		}
	}
	return nil, missing("%s has no %q", addr, key)
}

func (r *Repo) underTable(ctx context.Context, kind, sha string, rest []string) (*Node, error) {
	n := r.table(kind, sha)
	if len(rest) == 0 {
		return n, nil
	}
	if rest[0] == "shards" && len(rest) == 2 {
		var tm repo.TableManifest
		if err := r.get(ctx, n.File, &tm); err != nil {
			return nil, err
		}
		num, err := strconv.Atoi(rest[1])
		if err != nil || num < 1 || num > len(tm.Shards) {
			return nil, missing("index/%s has no shard %s, of %d", kind, rest[1], len(tm.Shards))
		}
		return r.tableShard(kind, fmt.Sprintf("index/%s/shards/%d", kind, num), tm.Shards[num-1]), nil
	}
	id, err := strconv.Atoi(rest[0])
	if err != nil || len(rest) != 1 {
		return nil, missing("index/%s/%s isn't a doc id", kind, strings.Join(rest, "/"))
	}
	s, err := r.tableRange(ctx, kind, sha, id)
	if err != nil {
		return nil, err
	}
	var found *Node
	err = r.tableShard(kind, "index/"+kind+"/shards/0", s).Children(ctx, func(c *Node) error {
		if strings.HasSuffix(c.Addr, "/"+rest[0]) {
			found = c
			return errFound
		}
		return nil
	})
	if err != nil && !errors.Is(err, errFound) {
		return nil, err
	}
	if found == nil {
		return nil, missing("no doc id %d", id)
	}
	return found, nil
}

func (r *Repo) underPackages(ctx context.Context, sha string, rest []string) (*Node, error) {
	n := r.packageIndex(sha)
	if len(rest) == 0 {
		return n, nil
	}
	if rest[0] == "shards" && len(rest) == 2 {
		var pi repo.PackageIndex
		if err := r.get(ctx, n.File, &pi); err != nil {
			return nil, err
		}
		num, err := strconv.Atoi(rest[1])
		if err != nil || num < 1 || num > len(pi.Chunks) {
			return nil, missing("index/packages has no chunk %s, of %d", rest[1], len(pi.Chunks))
		}
		return r.packageChunk("index/packages/shards/"+rest[1], pi.Chunks[num-1]), nil
	}
	pkg, err := r.findPackage(ctx, rest[0])
	if err != nil {
		return nil, err
	}
	if len(rest) == 1 {
		return pkg, nil
	}
	if rest[1] == "pages" && len(rest) == 2 {
		var found *Node
		if err := pkg.Children(ctx, func(c *Node) error { found = c; return nil }); err != nil {
			return nil, err
		}
		return found, nil
	}
	return nil, missing("nothing at index/packages/%s", strings.Join(rest, "/"))
}

func (r *Repo) resolvePack(ctx context.Context, rest string) (*Node, error) {
	if rest == "" {
		return r.group("packs", "the log of packs"), nil
	}
	if rest == "log" {
		return r.packLog(), nil
	}
	m, err := r.Manifest(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range m.Packs {
		if strconv.Itoa(e.Seq) == rest || (len(rest) >= 8 && strings.HasPrefix(e.SHA256, rest)) {
			return r.pack(e), nil
		}
	}
	return nil, missing("no pack %s", rest)
}

// fileForms are the repository's files, by their paths (see doc/repo-format.md).
var (
	hex64       = `[0-9a-f]{64}`
	formPointer = regexp.MustCompile(`^page/name-([^/]+)/([^/]+)\.json$`)
	formListing = regexp.MustCompile(`^page/page-[0-9a-f]{2}/(` + hex64 + `)\.json$`)
	formContent = regexp.MustCompile(`^contents/[0-9a-f]{2}/(` + hex64 + `)/variants\.json$`)
	formVariant = regexp.MustCompile(`^contents/[0-9a-f]{2}/(` + hex64 + `)/([a-z][a-z0-9-]*)-(` + hex64 + `)\.json$`)
	formShard   = regexp.MustCompile(`^section/shard-[0-9a-f]{2}/(` + hex64 + `)\.json$`)
	formChapter = regexp.MustCompile(`^section/([^/]+)-(` + hex64 + `)\.json$`)
	formChapPtr = regexp.MustCompile(`^section/([^/]+)\.json$`)
	formIndexM  = regexp.MustCompile(`^index/(words|grams|names|docs|packages|variants/[a-z][a-z0-9-]*)/manifest-(` + hex64 + `)\.json$`)
	formIndexS  = regexp.MustCompile(`^index/(words|grams|names|docs|packages|package|variants/[a-z][a-z0-9-]*)/shard-[0-9a-f]{2}/(` + hex64 + `)\.json$`)
	formPack    = regexp.MustCompile(`^packs/(` + hex64 + `)\.tar\.zst$`)
)

// isFile reports whether an address is a file's path in the repository.
func isFile(addr string) bool {
	if addr == repo.ManifestPath() || addr == repo.PackLogPath() {
		return true
	}
	for _, form := range []*regexp.Regexp{formPointer, formListing, formContent, formVariant, formShard, formChapter, formChapPtr, formIndexM, formIndexS, formPack} {
		if form.MatchString(addr) {
			return true
		}
	}
	return false
}

// fromFile is the Node for a repository file, by its path.
func (r *Repo) fromFile(ctx context.Context, rel string) (*Node, error) {
	unescape := func(s string) string {
		if u, err := url.PathUnescape(s); err == nil {
			return u
		}
		return s
	}
	var n *Node
	switch {
	case rel == repo.ManifestPath():
		return r.Root(), nil
	case rel == repo.PackLogPath():
		return r.packLog(), nil
	}
	if m := formPointer.FindStringSubmatch(rel); m != nil {
		n = r.pagePointer(unescape(m[2]), unescape(m[1]))
	} else if m := formListing.FindStringSubmatch(rel); m != nil {
		var l repo.Listing
		if err := r.get(ctx, rel, &l); err != nil {
			return nil, err
		}
		n = r.listing(l.Name, l.Section, m[1], true)
	} else if m := formContent.FindStringSubmatch(rel); m != nil {
		n = r.contents(rel, m[1])
	} else if m := formVariant.FindStringSubmatch(rel); m != nil {
		n = r.variant(rel, m[1], m[2], repo.VariantInfo{SHA256: m[3]})
		n.Bytes = 0
	} else if m := formShard.FindStringSubmatch(rel); m != nil {
		n = r.chapterShard("", 0, repo.ChapterShardRef{SHA256: m[1]})
		n.Summary = ""
	} else if m := formChapter.FindStringSubmatch(rel); m != nil {
		n = r.chapter(repo.ChapterEntry{Chapter: unescape(m[1]), SHA256: m[2]})
		n.Summary = ""
	} else if m := formChapPtr.FindStringSubmatch(rel); m != nil {
		n = r.chapterPointer(unescape(m[1]))
	} else if m := formIndexM.FindStringSubmatch(rel); m != nil {
		switch m[1] {
		case "names", "docs":
			n = r.table(m[1], m[2])
		case "packages":
			n = r.packageIndex(m[2])
		default:
			n = r.sharded(m[1], "index/"+m[1], m[2])
		}
	} else if m := formIndexS.FindStringSubmatch(rel); m != nil {
		switch m[1] {
		case "names", "docs":
			n = r.tableShard(m[1], rel+"/shards/0", repo.IDRange{SHA256: m[2]})
		case "packages":
			n = r.packageChunk(rel, repo.ChapterShardRef{SHA256: m[2]})
		case "package":
			n = r.packageList(rel, "", 0, m[2])
		default:
			n = r.keyShard(m[1], rel+"/shards/0", repo.ShardRange{SHA256: m[2]})
		}
	} else if m := formPack.FindStringSubmatch(rel); m != nil {
		man, err := r.Manifest(ctx)
		if err != nil {
			return nil, err
		}
		for _, e := range man.Packs {
			if e.SHA256 == m[1] {
				return r.pack(e), nil
			}
		}
		n = r.pack(repo.PackEntry{Path: rel, SHA256: m[1]})
	}
	if n == nil {
		return nil, missing("%s isn't a repository file", rel)
	}
	n.Addr = rel
	return n, nil
}

// Kinds says what can be given to AsFile, by what each file's kind is called.
var Kinds = []string{"manifest", "pointer", "page", "contents", "variant", "chapter", "chapter-shard", "shard-manifest",
	"table-manifest", "word-shard", "gram-shard", "source-shard", "name-shard", "doc-shard", "package-index", "package-chunk", "package-pages", "pack-log"}

// AsFile is a Node for bytes from outside any repository, as a kind of file,
// for checking.
func (r *Repo) AsFile(kind, name string, data []byte) (*Node, error) {
	var n *Node
	switch kind {
	case "manifest":
		n = r.Root()
	case "pointer":
		n = &Node{Kind: "pointer", check: func(_ context.Context, b []byte, report Report) {
			var p repo.Pointer
			if decode(b, &p, report) && !repo.ValidHash(p.SHA256) {
				report(false, "it has a bad hash %q", p.SHA256)
			}
		}}
	case "page":
		n = r.page("", "", "")
	case "contents":
		n = r.contents("", "")
	case "variant":
		n = r.variant("", "", "", repo.VariantInfo{})
	case "chapter":
		n = &Node{Kind: "chapter", check: func(_ context.Context, b []byte, report Report) {
			var ci repo.ChapterIndex
			decode(b, &ci, report)
		}}
	case "chapter-shard":
		n = r.chapterShard("", 0, repo.ChapterShardRef{})
	case "shard-manifest":
		n = r.sharded("words", "x", "")
	case "table-manifest":
		n = r.table("names", "")
		n.check = func(_ context.Context, b []byte, report Report) {
			var tm repo.TableManifest
			decode(b, &tm, report)
		}
	case "word-shard":
		n = r.keyShard("words", "x/shards/0", repo.ShardRange{})
	case "gram-shard":
		n = r.keyShard("grams", "x/shards/0", repo.ShardRange{})
	case "source-shard":
		n = r.keyShard("variants/x", "x/shards/0", repo.ShardRange{})
	case "name-shard":
		n = r.tableShard("names", "x/shards/0", repo.IDRange{})
	case "doc-shard":
		n = r.tableShard("docs", "x/shards/0", repo.IDRange{})
	case "package-index":
		n = r.packageIndex("")
	case "package-chunk":
		n = r.packageChunk("x", repo.ChapterShardRef{})
	case "package-pages":
		n = r.packageList("x", "", 0, "")
	case "pack-log":
		n = r.packLog()
	default:
		return nil, fmt.Errorf("no kind of file called %q (there's %s)", kind, strings.Join(Kinds, ", "))
	}
	n.r, n.Addr, n.File, n.Hash, n.Bytes, n.data = r, name, name, namedHash(name), 0, data
	n.children = nil
	return n, nil
}

// KindOfFile says what kind of file a path looks like, or "" if it can't tell.
func KindOfFile(path string) string {
	switch {
	case path == repo.ManifestPath():
		return "manifest"
	case formPointer.MatchString(path), formChapPtr.MatchString(path):
		return "pointer"
	case formListing.MatchString(path):
		return "page"
	case formContent.MatchString(path):
		return "contents"
	case formVariant.MatchString(path):
		return "variant"
	case formShard.MatchString(path):
		return "chapter-shard"
	case formChapter.MatchString(path):
		return "chapter"
	}
	if m := formIndexM.FindStringSubmatch(path); m != nil {
		switch m[1] {
		case "names", "docs":
			return "table-manifest"
		case "packages":
			return "package-index"
		}
		return "shard-manifest"
	}
	if m := formIndexS.FindStringSubmatch(path); m != nil {
		switch m[1] {
		case "words":
			return "word-shard"
		case "grams":
			return "gram-shard"
		case "names":
			return "name-shard"
		case "docs":
			return "doc-shard"
		case "packages":
			return "package-chunk"
		case "package":
			return "package-pages"
		}
		return "source-shard"
	}
	return ""
}
