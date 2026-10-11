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
//	contents/ab/<source>/<variant>.json    one version of a page, as Variant
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
//
// Files whose names end in -<sha> or are named <sha>.json are named after
// the SHA-256 of their own bytes, so they never change and can be cached
// forever. The pointers and manifest.json are the only files that change in
// place; contents files change only when a page is converted again.
package repo

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// Format is the repository format version written into every document.
const Format = 1

// Variants of a page.
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
	Format    int            `json:"format"`
	Generated time.Time      `json:"generated"`
	Pages     int            `json:"pages"`
	Girlified int            `json:"girlified"`
	Variants  []string       `json:"variants"`
	Sections  []SectionEntry `json:"sections"`
	Index     IndexEntry     `json:"index"`
	// Sources are the distributions (and releases) pages come from.
	Sources []SourceEntry `json:"sources,omitempty"`
}

// SectionEntry is a section in the manifest.
type SectionEntry struct {
	Section   string `json:"section"`
	SHA256    string `json:"sha256"`
	Pages     int    `json:"pages"`
	Girlified int    `json:"girlified"`
}

// IndexEntry names the search index manifests.
type IndexEntry struct {
	Words string `json:"words"`
	Grams string `json:"grams"`
	Docs  string `json:"docs"`
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
	// Current is the source hash of the version to show by default.
	Current  string    `json:"current"`
	Versions []Version `json:"versions"`
}

// Version is one version of a page: one distinct roff source.
type Version struct {
	Source      string                 `json:"source"`
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description,omitempty"`
	Added       time.Time              `json:"added"`
	Variants    map[string]VariantInfo `json:"variants"`
	Packages    []Package              `json:"packages,omitempty"`
}

// VariantInfo describes a stored variant.
type VariantInfo struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
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
func (l *Listing) CurrentVersion() *Version {
	if v := l.Find(l.Current); v != nil {
		return v
	}
	if len(l.Versions) > 0 {
		return &l.Versions[0]
	}
	return nil
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

// SectionIndex lists every page in one section.
type SectionIndex struct {
	Format  int         `json:"format"`
	Section string      `json:"section"`
	Pages   []IndexPage `json:"pages"`
}

// IndexPage is a page in a SectionIndex.
type IndexPage struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Page is the SHA-256 of the page's Listing.
	Page string `json:"page"`
	// Source is the current version's source hash.
	Source    string `json:"source"`
	Girlified bool   `json:"girlified,omitempty"`
}

// ShardManifest lists index shards by the range of keys each holds. Shards
// are sorted and don't overlap; keys compare as UTF-8 bytes.
type ShardManifest struct {
	Format int          `json:"format"`
	Shards []ShardRange `json:"shards"`
}

// ShardRange is one shard in a ShardManifest.
type ShardRange struct {
	First  string `json:"first"`
	Last   string `json:"last"`
	SHA256 string `json:"sha256"`
}

// WordShard maps words to the pages they appear in. Postings are
// [doc id, score], best first; doc ids index the DocsManifest.
type WordShard struct {
	Format int                 `json:"format"`
	Words  map[string][][2]int `json:"words"`
}

// DocsManifest lists the shards of the search index's page table. Doc id
// i is entry i%Size of shard i/Size. Ids are in name, then section, order.
type DocsManifest struct {
	Format int      `json:"format"`
	Count  int      `json:"count"`
	Size   int      `json:"size"`
	Shards []string `json:"shards"`
}

// DocShard is a slice of the page table: [name, section, description].
type DocShard struct {
	Format int         `json:"format"`
	Docs   [][3]string `json:"docs"`
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

func ContentPath(source, variant string) string {
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

// IndexManifestPath is index/<kind>/manifest-<sha>.json; kind is
// "words", "grams" or "docs".
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
