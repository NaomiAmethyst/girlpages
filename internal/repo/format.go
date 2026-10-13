// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package repo reads and writes girlpages repositories: trees of JSON files
// that can be served by any static web host, holding girlified (and
// original) man pages for many packages and distributions.
//
// Everything is keyed by the SHA-256 of a page's roff source (see
// manpage.SourceHash), so a client that has a page installed can fetch its
// conversion with a single request without knowing anything else:
//
//	manifest.json                          sections and index manifests
//	contents/ab/<source>/variants.json     Contents: a version's variants
//	contents/ab/<source>/<variant>-<sha>.json  one variant, as Variant
//	page/name-<section>/<name>.json        Pointer to the page's Listing
//	page/page-ab/<sha>.json                Listing: every version of a page
//	section/<section>.json                 Pointer to the section's index
//	section/<section>-<sha>.json           SectionIndex
//	index/words/manifest-<sha>.json        ShardManifest: word ranges
//	index/words/shard-ab/<sha>.json        WordShard
//	index/grams/manifest-<sha>.json        ShardManifest: trigram ranges
//	index/grams/shard-ab/<sha>.json        GramShard
//	index/docs/manifest-<sha>.json         DocsManifest
//	index/docs/shard-ab/<sha>.json         DocShard: the pages postings name
//	index/variants/<variant>/manifest-<sha>.json  ShardManifest: source ranges
//	index/variants/<variant>/shard-ab/<sha>.json  SourceShard: versions with it
//
// Files whose names end in -<sha> or are named <sha>.json are named after
// the SHA-256 of their own bytes, so they never change and can be cached
// forever. manifest.json, the pointers and variants.json files are the only
// ones that change in place.
package repo

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/version"
)

// Format is the repository format version written into every document.
const Format = 1

// Variants of a page. There can be others, made with other prompts; their
// names are short lower-case words (see ValidVariant).
const (
	// VariantGirl is the girlified page, in girlpages markup.
	VariantGirl = "girl"
	// VariantMan is the original, as plain text from man(1).
	VariantMan = "man"
)

// Section body syntaxes.
const (
	SyntaxMarkup = "girlpages-markup"
	SyntaxText   = "text"
)

// Manifest is manifest.json, the root of a repository.
type Manifest struct {
	Format    int       `json:"format"`
	Generated time.Time `json:"generated"`
	Pages     int       `json:"pages"`
	// Variants counts, for each variant there is, the pages whose latest
	// version has it.
	Variants map[string]int `json:"variants"`
	Sections []SectionEntry `json:"sections"`
	// Chapters are the chapters' indexes, each listing all of a chapter's
	// sections' pages.
	Chapters []ChapterEntry `json:"chapters"`
	Index    IndexEntry     `json:"index"`
	// Sources are the distributions (and releases) pages come from.
	Sources []SourceEntry `json:"sources,omitempty"`
}

// SectionEntry is a section in the manifest.
type SectionEntry struct {
	Section string `json:"section"`
	Pages   int    `json:"pages"`
	// Variants counts, for each variant, the section's pages whose latest
	// version has it.
	Variants map[string]int `json:"variants,omitempty"`
}

// IndexEntry names the index manifests.
type IndexEntry struct {
	Words string `json:"words"`
	Grams string `json:"grams"`
	// Names and Docs are the page table: what each page found is called,
	// and what it says about itself.
	Names string `json:"names"`
	Docs  string `json:"docs"`
	// Variants names, for each variant, the manifest of its index (a
	// ShardManifest of SourceShards), which says which page versions have
	// that variant without asking for each.
	Variants map[string]string `json:"variants,omitempty"`
}

// Pointer is a small mutable document naming an immutable one.
type Pointer struct {
	Format  int    `json:"format"`
	Name    string `json:"name,omitempty"`
	Section string `json:"section"`
	SHA256  string `json:"sha256"`
}

// Listing is every known version of one page.
type Listing struct {
	Format  int    `json:"format"`
	Name    string `json:"name"`
	Section string `json:"section"`
	// Current is the source hash of the latest version by version number,
	// the one shown by default. It's worked out whenever the listing
	// changes.
	Current  string    `json:"current"`
	Versions []Version `json:"versions"`
	// Sources maps each source's key ("debian/trixie") to the source hash
	// of the version it has, for clients. Also worked out on every change.
	Sources map[string]string `json:"sources,omitempty"`
}

// Version is one version of a page: one distinct roff source.
type Version struct {
	Source      string                 `json:"source"`
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description,omitempty"`
	Added       time.Time              `json:"added"`
	Variants    map[string]VariantInfo `json:"variants"`
	Packages    []Package              `json:"packages,omitempty"`
	// Version is the upstream version number (see Upstream), for clients.
	// It's worked out whenever the listing changes.
	Version string `json:"version,omitempty"`
}

// VariantInfo describes a stored variant: in a listing, its latest
// generation.
type VariantInfo struct {
	// SHA256 names the variant's file (see VariantPath).
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// Generation counts how many times the variant has been made for this
	// version: 1 at first, more if it was made again (with a new prompt,
	// say). The latest is the highest.
	Generation int `json:"generation,omitempty"`
	// PromptVersion is the prompt version its generator records, so
	// outdated conversions can be found without reading them; 0 if it
	// doesn't say.
	PromptVersion int `json:"prompt_version,omitempty"`
}

// Contents is contents/ab/<source>/variants.json: every generation of every
// variant of one page version.
type Contents struct {
	Format   int                      `json:"format"`
	Source   string                   `json:"source"`
	Variants map[string][]VariantInfo `json:"variants"`
}

// Latest is a variant's latest generation, or nil if there's none.
func (c *Contents) Latest(variant string) *VariantInfo {
	var best *VariantInfo
	for i, g := range c.Variants[variant] {
		if best == nil || g.Generation > best.Generation {
			best = &c.Variants[variant][i]
		}
	}
	return best
}

// InfoFor describes a variant stored as b.
func InfoFor(b []byte) VariantInfo {
	info := VariantInfo{SHA256: Hash(b), Size: int64(len(b))}
	var v struct {
		Generator *Generator `json:"generator"`
	}
	if json.Unmarshal(b, &v) == nil && v.Generator != nil {
		info.PromptVersion = v.Generator.PromptVersion
	}
	return info
}

// Package is somewhere a version was found.
type Package struct {
	Distro  string `json:"distro,omitempty"`
	Release string `json:"release,omitempty"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Arch    string `json:"arch,omitempty"`
	// URL is where it came from, for sources such as git repositories.
	URL string `json:"url,omitempty"`
}

func (p Package) String() string {
	var parts []string
	if p.Distro != "" {
		d := p.Distro
		if p.Release != "" {
			d += " " + p.Release
		}
		parts = append(parts, d)
	}
	if p.Name != "" {
		n := p.Name
		if p.Version != "" {
			n += " " + p.Version
		}
		parts = append(parts, n)
	}
	out := strings.Join(parts, ": ")
	if p.URL != "" {
		out += " from " + p.URL
	}
	return out
}

// Find returns the version with the given source hash, or nil.
func (l *Listing) Find(source string) *Version {
	for i := range l.Versions {
		if l.Versions[i].Source == source {
			return &l.Versions[i]
		}
	}
	return nil
}

// CurrentVersion returns the version to show by default.
// It's always the latest by version number (see Latest); Current records
// which that is for clients.
func (l *Listing) CurrentVersion() *Version { return l.Latest() }

// Upstream is the highest upstream version (see version.Upstream) of the
// packages a version was found in, or "" if none says.
func (v *Version) Upstream() string {
	best := ""
	for _, p := range v.Packages {
		if u := version.Upstream(p.Version); u != "" && (best == "" || version.Compare(u, best) > 0) {
			best = u
		}
	}
	return best
}

// Newer reports whether v is a later version of a page than w: by upstream
// version number, then (for the same upstream version, as with a distro's
// patched copy) by how many places have it, then by when it was added.
func (v *Version) Newer(w *Version) bool {
	uv, uw := v.Upstream(), w.Upstream()
	switch {
	case uv != "" && uw == "":
		return true
	case uv == "" && uw != "":
		return false
	case uv != uw:
		if c := version.Compare(uv, uw); c != 0 {
			return c > 0
		}
	}
	if len(v.Packages) != len(w.Packages) {
		return len(v.Packages) > len(w.Packages)
	}
	if !v.Added.Equal(w.Added) {
		return v.Added.After(w.Added)
	}
	return v.Source > w.Source
}

// Latest is the page's latest version by version number.
func (l *Listing) Latest() *Version {
	var best *Version
	for i := range l.Versions {
		if v := &l.Versions[i]; best == nil || v.Newer(best) {
			best = v
		}
	}
	return best
}

// Variant is one rendering of one version of a page.
type Variant struct {
	Format  int    `json:"format"`
	Variant string `json:"variant"`
	Source  string `json:"source"`
	Title   string `json:"title,omitempty"`
	// Syntax is how section bodies are written: SyntaxMarkup or SyntaxText.
	Syntax string `json:"syntax"`
	// Header and Footer are man's first and last lines (text only).
	Header   string    `json:"header,omitempty"`
	Footer   string    `json:"footer,omitempty"`
	Sections []Section `json:"sections"`
	// Generator says how a girl variant was made.
	Generator *Generator `json:"generator,omitempty"`
}

// Section is one top-level section of a page. Heading is "" for anything
// before the first heading. Body is the section's lines, each ending in a
// newline, exactly as they appear in the page.
type Section struct {
	Heading string `json:"heading"`
	Body    string `json:"body"`
}

// Generator records how a conversion was made.
type Generator struct {
	Backend       string    `json:"backend,omitempty"`
	Model         string    `json:"model,omitempty"`
	PromptVersion int       `json:"prompt_version,omitempty"`
	Generated     time.Time `json:"generated"`
}

// ShardManifest lists index shards by the range of keys each holds. Shards
// are sorted and don't overlap; keys compare as UTF-8 bytes.
type ShardManifest struct {
	Format int    `json:"format"`
	Shards Shards `json:"shards"`
}

// ShardRange is one shard in a ShardManifest.
type ShardRange struct {
	First  string
	Last   string
	SHA256 string
	// Bytes is the shard's size, so clients can say how much fetching
	// every shard would be.
	Bytes int64
	// Count, for section index shards, is how many pages the shard has.
	Count int
}

// Shards are a list of shards, as a Table: first, last, sha256 (in
// basE91), bytes, and for section index shards, count.
type Shards []ShardRange

func (s Shards) MarshalJSON() ([]byte, error) {
	counts := false
	for _, r := range s {
		counts = counts || r.Count > 0
	}
	keys := []string{"first", "last", "sha256", "bytes"}
	if counts {
		keys = append(keys, "count")
	}
	t := newTable(keys...)
	for _, r := range s {
		if counts {
			t.add(r.First, r.Last, hash91(r.SHA256), r.Bytes, r.Count)
		} else {
			t.add(r.First, r.Last, hash91(r.SHA256), r.Bytes)
		}
	}
	return t.MarshalJSON()
}

func (s *Shards) UnmarshalJSON(b []byte) error {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	*s = nil
	return t.rows(func(get func(string) json.RawMessage) error {
		var r ShardRange
		var sha string
		for _, f := range []struct {
			key string
			v   any
		}{{"first", &r.First}, {"last", &r.Last}, {"sha256", &sha}, {"bytes", &r.Bytes}, {"count", &r.Count}} {
			if err := field(get(f.key), f.v); err != nil {
				return fmt.Errorf("shards %s: %w", f.key, err)
			}
		}
		var err error
		if r.SHA256, err = unhash91(sha); err != nil {
			return err
		}
		*s = append(*s, r)
		return nil
	})
}

// WordShard maps words to the pages they appear in. Postings are doc id
// and score, in turn, best first: [id, score, id, score, …]; doc ids index
// the page table (names and docs).
type WordShard struct {
	Format int              `json:"format"`
	Words  map[string][]int `json:"words"`
}

// SourceShard is part of a variant's index: the first Length hex digits of
// the source hash of every version with that variant, sorted. A match means
// the variant is (almost certainly) there.
type SourceShard struct {
	Format  int      `json:"format"`
	Length  int      `json:"length"`
	Sources []string `json:"sources"`
}

// SourceKeyLength is how many hex digits of a source hash variant indexes
// keep: 64 bits, so a false match (which only
// costs a request that finds nothing) is vanishingly unlikely.
const SourceKeyLength = 16

// TableManifest lists the shards of the page table's names or docs, by the
// range of doc ids each holds. Count is one more than the highest id.
type TableManifest struct {
	Format int      `json:"format"`
	Count  int      `json:"count"`
	Shards IDShards `json:"shards"`
}

// IDRange is a shard of the page table: ids First to Last.
type IDRange struct {
	First, Last int
	SHA256      string
	Bytes       int64
}

// IDShards are a list of the page table's shards, as a Table: first,
// last, sha256 (in basE91), bytes.
type IDShards []IDRange

func (s IDShards) MarshalJSON() ([]byte, error) {
	t := newTable("first", "last", "sha256", "bytes")
	for _, r := range s {
		t.add(r.First, r.Last, hash91(r.SHA256), r.Bytes)
	}
	return t.MarshalJSON()
}

func (s *IDShards) UnmarshalJSON(b []byte) error {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	*s = nil
	return t.rows(func(get func(string) json.RawMessage) error {
		var r IDRange
		var sha string
		for _, f := range []struct {
			key string
			v   any
		}{{"first", &r.First}, {"last", &r.Last}, {"sha256", &sha}, {"bytes", &r.Bytes}} {
			if err := field(get(f.key), f.v); err != nil {
				return fmt.Errorf("shards %s: %w", f.key, err)
			}
		}
		var err error
		if r.SHA256, err = unhash91(sha); err != nil {
			return err
		}
		*s = append(*s, r)
		return nil
	})
}

// Find is the shard holding id, if any.
func (s IDShards) Find(id int) (IDRange, bool) {
	i := sort.Search(len(s), func(i int) bool { return s[i].Last >= id })
	if i < len(s) && s[i].First <= id {
		return s[i], true
	}
	return IDRange{}, false
}

// NameShard is a run of the page table's names, from id First: each page's
// name and section ("" for an id no page has any more).
type NameShard struct {
	Format int       `json:"format"`
	First  int       `json:"first"`
	Names  NameTable `json:"names"`
}

// PageName is a page's name and section.
type PageName struct{ Name, Section string }

// NameTable is names, as a Table: name, section.
type NameTable []PageName

func (n NameTable) MarshalJSON() ([]byte, error) {
	t := newTable("name", "section")
	for _, p := range n {
		t.add(p.Name, p.Section)
	}
	return t.MarshalJSON()
}

func (n *NameTable) UnmarshalJSON(b []byte) error {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	*n = nil
	return t.rows(func(get func(string) json.RawMessage) error {
		var p PageName
		if err := field(get("name"), &p.Name); err != nil {
			return err
		}
		if err := field(get("section"), &p.Section); err != nil {
			return err
		}
		*n = append(*n, p)
		return nil
	})
}

// DocShard is a run of the page table's docs, from id First: each page's
// description, and the keys of the sources it's from, separated by spaces.
type DocShard struct {
	Format int      `json:"format"`
	First  int      `json:"first"`
	Docs   DocTable `json:"docs"`
}

// PageDoc is what the page table says about a page, besides its name.
type PageDoc struct{ Description, Sources string }

// DocTable is docs, as a Table: description, sources.
type DocTable []PageDoc

func (d DocTable) MarshalJSON() ([]byte, error) {
	t := newTable("description", "sources")
	for _, p := range d {
		t.add(p.Description, p.Sources)
	}
	return t.MarshalJSON()
}

func (d *DocTable) UnmarshalJSON(b []byte) error {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	*d = nil
	return t.rows(func(get func(string) json.RawMessage) error {
		var p PageDoc
		if err := field(get("description"), &p.Description); err != nil {
			return err
		}
		if err := field(get("sources"), &p.Sources); err != nil {
			return err
		}
		*d = append(*d, p)
		return nil
	})
}

// GramShard maps trigrams to the words containing them. Pages holds, in
// step with each list of words, how many pages each word is in, so the
// commonest of several equally close guesses wins.
type GramShard struct {
	Format int                 `json:"format"`
	Grams  map[string][]string `json:"grams"`
	Pages  map[string][]int    `json:"pages"`
}

// Paths, relative to the repository root.

func ManifestPath() string { return "manifest.json" }

// ContentsPath is the Contents file of a page version.
func ContentsPath(source string) string {
	return path.Join("contents", source[:2], source, "variants.json")
}

// VariantPath is a generation of a variant, named after its own hash.
func VariantPath(source, variant, sha string) string {
	return path.Join("contents", source[:2], source, variant+"-"+sha+".json")
}

// legacyVariantPath is where variants were kept before generations:
// named after the variant alone, so changed in place.
func legacyVariantPath(source, variant string) string {
	return path.Join("contents", source[:2], source, variant+".json")
}

func NamePath(name, section string) string {
	return path.Join("page", "name-"+EscapeName(section), EscapeName(name)+".json")
}

func ListingPath(sha string) string {
	return path.Join("page", "page-"+sha[:2], sha+".json")
}

func SectionPointerPath(section string) string {
	return path.Join("section", EscapeName(section)+".json")
}

func SectionIndexPath(section, sha string) string {
	return path.Join("section", EscapeName(section)+"-"+sha+".json")
}

// SectionShardPath is section/shard-ab/<sha>.json, a shard of a long
// section's index.
func SectionShardPath(sha string) string {
	return path.Join("section", "shard-"+sha[:2], sha+".json")
}

// IndexManifestPath is index/<kind>/manifest-<sha>.json; kind is
// "words", "grams", "docs" or "variants/<variant>".
func IndexManifestPath(kind, sha string) string {
	return path.Join("index", kind, "manifest-"+sha+".json")
}

func ShardPath(kind, sha string) string {
	return path.Join("index", kind, "shard-"+sha[:2], sha+".json")
}

// EscapeName makes a page name or section safe as a file name: bytes
// other than letters, digits and "_+-.,:@" become %XX, as does a leading
// dot. Clients must escape the same way, then percent-encode the result
// again when building a URL (so "%" travels as "%25").
func EscapeName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("_+-.,:@", c) >= 0
		if !ok || i == 0 && c == '.' {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

var variantRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidVariant reports whether s can be a variant's name: a short
// lower-case word, safe in file names and URLs.
func ValidVariant(s string) bool { return variantRE.MatchString(s) }

// VariantIndexKind is the index kind of a variant's index.
func VariantIndexKind(variant string) string { return "variants/" + variant }

// ValidHash reports whether s looks like a hex SHA-256.
func ValidHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
