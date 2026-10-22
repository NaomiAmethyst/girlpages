// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"hash/maphash"
	"math"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
)

// The search index is two sharded maps. words maps every word to the pages
// it appears in, with a score; grams maps trigrams to the words containing
// them, for finding words from a misspelling. Each has a manifest listing
// the range of keys in each shard, so a client fetches just the shards it
// needs. web/search.js and Search here must agree with Tokens.

// Scores for where a word appears in a page.
const (
	scoreName     = 1000 // the whole page name, plus scoreSection
	scoreSection  = 25   // per place earlier in man's section order
	scoreNamePart = 100  // a word in the name
	scoreDesc     = 60   // a word in the description
	scoreBodyMax  = 20   // a word in the page, by how often
	// scoreVersioned is a name that's a word and a version number
	// (icecast2, python3), as that word: it's that program, so it's
	// worth more than any mention, but less than a page called it.
	scoreVersioned = scoreName / 2
)

// versioned matches a word with a version number stuck on (icecast2,
// python3): the word, of two letters or more, ending in a letter (so not
// x86_64 or x11), then digits.
var versioned = regexp.MustCompile(`^(\pL[\pL\pN_]*\pL)[0-9]+$`)

// versionStem is the word in a name with a version number stuck on, or "".
func versionStem(w string) string {
	if m := versioned.FindStringSubmatch(w); m != nil {
		return m[1]
	}
	return ""
}

const (
	minTokenRunes = 2
	maxTokenRunes = 40
	// maxPostings is how many pages are kept per word, best first.
	maxPostings = 400
	// commonBody: body words in more than this fraction of pages are left
	// out of the index (they still count in names and descriptions).
	commonBody = 0.3
	// shardTarget is roughly how big a shard grows before a new one starts.
	shardTarget = 64 << 10
)

// Tokens splits text into lower-case words: runs of letters, digits and
// underscores between 2 and 40 characters long.
func Tokens(s string) []string {
	var out []string
	start := -1
	emit := func(end int) {
		if start < 0 {
			return
		}
		t := s[start:end]
		start = -1
		if n := utf8.RuneCountInString(t); n < minTokenRunes || n > maxTokenRunes {
			return
		}
		out = append(out, strings.ToLower(t))
	}
	for i, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			if start < 0 {
				start = i
			}
			continue
		}
		emit(i)
	}
	emit(len(s))
	return out
}

// Grams returns a word's distinct trigrams, including ones marking where
// it starts and ends ("^gr" and "ep$" for grep), so even short words and
// transposed letters share some with the word that was meant.
func Grams(w string) []string {
	rs := []rune("^" + w + "$")
	seen := map[string]bool{}
	var out []string
	for i := 0; i+3 <= len(rs); i++ {
		g := string(rs[i : i+3])
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

// EditDistance is the optimal string alignment distance between a and b:
// insertions, deletions, substitutions and swaps of neighbours each cost 1.
func EditDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	d := make([][]int, len(x)+1)
	for i := range d {
		d[i] = make([]int, len(y)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(x); i++ {
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(x)][len(y)]
}

// MaxTypos is how far a word may be from a query word to count as a
// misspelling of it.
func MaxTypos(word string) int {
	switch n := utf8.RuneCountInString(word); {
	case n <= 2:
		return 0
	case n <= 5:
		return 1
	default:
		return 2
	}
}

// IndexDoc is a page to index.
type IndexDoc struct {
	// ID is the page's doc id: its place in the page table.
	ID                         int
	Name, Section, Description string
	// Sources are the keys of the sources the page comes from, for
	// searching within one.
	Sources []string
	// Packages are its PackageKeys, for keeping to a package.
	Packages []string
	// Body is the page's text (the original, not the girly one: it's what
	// people search for).
	Body string
}

// BuiltIndex is a search index ready to write: shards with their
// manifests. (The page table is written from the docs' ids.)
type BuiltIndex struct {
	Words      []WordShard
	WordRanges []ShardRange // SHA256 left for the writer
	Grams      []GramShard
	GramRanges []ShardRange
	// Common are the words in so many pages' text that only pages with
	// them in their names or descriptions are listed: a phrase search
	// can't go by them.
	Common []string
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by for from has have if in into is it its
		no not of on or so such that the their then there these they this to was were will with you your
		can may must should which when where who also only than other any all each more most some`) {
		stopwords[w] = true
	}
}

// BuildIndex builds the search index for docs, by their ids.
func BuildIndex(docs []IndexDoc) *BuiltIndex {
	// Pages are scored in parallel, then words are shared out among the
	// CPUs by hash so each merges, sorts and trigrams its own; only cutting
	// the sorted result into shards is left to do in turn.
	parts := runtime.NumCPU()
	seed := maphash.MakeSeed()
	partOf := func(w string) int { return int(maphash.String(seed, w) % uint64(parts)) }

	const (
		inBody  = 1 << iota // a distinct body word, counted for bodyDF
		inVocab             // worth finding by trigram
	)
	type term struct {
		word  string
		score int
		flags uint8
	}
	per := make([][][]term, len(docs)) // doc → part → terms
	parallel(len(docs), func(di int) {
		d := docs[di]
		scores := map[string]int{}
		flags := map[string]uint8{}
		name := strings.ToLower(d.Name)
		// Like man, prefer printf(1) to printf(3): each section step is
		// worth more than any amount of body text.
		scores[name] += scoreName + scoreSection*(manpage.SectionCount-manpage.SectionRank(d.Section))
		flags[name] |= inVocab
		if manpage.IsSeries(d.Section) {
			// RFC 9110 is found as rfc9110 too (see SeriesTerms).
			scores[strings.ToLower(d.Section)+name] += scoreName
			flags[strings.ToLower(d.Section+d.Name)] |= inVocab
		}
		// A program with its version in its name (icecast2, python3) is
		// found by the program's name; so is a part of a name (libssl3
		// in libssl3-dev), as a word of it.
		if stem := versionStem(name); stem != "" {
			scores[stem] += scoreVersioned
			flags[stem] |= inVocab
		}
		for _, t := range Tokens(d.Name) {
			if stem := versionStem(t); stem != "" && t != name {
				scores[stem] += scoreNamePart
				flags[stem] |= inVocab
			}
			if t != name {
				scores[t] += scoreNamePart
			}
			flags[t] |= inVocab
		}
		seen := map[string]bool{}
		for _, t := range Tokens(d.Description) {
			if !seen[t] && !stopwords[t] {
				seen[t] = true
				scores[t] += scoreDesc
				flags[t] |= inVocab
			}
		}
		tf := map[string]int{}
		for _, t := range Tokens(d.Body) {
			if !stopwords[t] {
				tf[t]++
			}
		}
		for t, n := range tf {
			s := 1 + int(4*math.Log2(float64(n)))
			scores[t] += min(s, scoreBodyMax)
			flags[t] |= inBody
		}
		// Packages are filters, not scores: every page from one is listed,
		// whatever its score.
		for _, k := range d.Packages {
			scores[PackagePrefix+k] = 1
		}
		out := make([][]term, parts)
		for t, s := range scores {
			p := partOf(t)
			out[p] = append(out[p], term{t, s, flags[t]})
		}
		per[di] = out
	})

	type wordEntry struct {
		word string
		list []int // flat [doc, score, …]
		size int
	}
	type gramWord struct {
		gram, word string
		df         int
	}
	words := make([][]wordEntry, parts)
	common := make([][]string, parts)
	gramsOut := make([][][]gramWord, parts) // word part → gram part → …
	limit := int(commonBody * float64(len(docs)))
	parallel(parts, func(p int) {
		type posting struct{ doc, score int }
		postings := map[string][]posting{}
		bodyDF := map[string]int{}
		vocab := map[string]bool{}
		for di, d := range docs {
			for _, t := range per[di][p] {
				postings[t.word] = append(postings[t.word], posting{d.ID, t.score})
				if t.flags&inBody != 0 {
					bodyDF[t.word]++
				}
				if t.flags&inVocab != 0 {
					vocab[t.word] = true
				}
			}
			per[di][p] = nil
		}

		// Drop body-only postings for words that are everywhere; they make
		// shards huge and searches no better.
		if len(docs) >= 20 {
			for t, df := range bodyDF {
				if df <= limit {
					continue
				}
				common[p] = append(common[p], t)
				ps := postings[t][:0]
				for _, p := range postings[t] {
					if p.score > scoreBodyMax {
						ps = append(ps, p)
					}
				}
				if len(ps) == 0 {
					delete(postings, t)
				} else {
					postings[t] = ps
				}
			}
		}
		// Body words worth fuzzy-finding: ones in more than one page and not
		// mostly digits (option values, addresses and versions aren't typos).
		for t, df := range bodyDF {
			if df >= 2 && !mostlyDigits(t) && postings[t] != nil {
				vocab[t] = true
			}
		}

		out := make([]wordEntry, 0, len(postings))
		for t, ps := range postings {
			sort.Slice(ps, func(i, j int) bool {
				if ps[i].score != ps[j].score {
					return ps[i].score > ps[j].score
				}
				return ps[i].doc < ps[j].doc
			})
			if len(ps) > maxPostings && !strings.HasPrefix(t, PackagePrefix) {
				ps = ps[:maxPostings]
				postings[t] = ps
			}
			list := make([]int, 0, 2*len(ps))
			size := len(t) + 6
			for _, p := range ps {
				list = append(list, p.doc, p.score)
				size += 2 + digits(p.doc) + digits(p.score)
			}
			out = append(out, wordEntry{t, list, size})
		}
		words[p] = out

		gs := make([][]gramWord, parts)
		for w := range vocab {
			df := max(len(postings[w]), bodyDF[w])
			for _, g := range Grams(w) {
				q := partOf(g)
				gs[q] = append(gs[q], gramWord{g, w, df})
			}
		}
		gramsOut[p] = gs
	})

	type gramEntry struct {
		gram   string
		words  []string
		counts []int
		size   int
	}
	grams := make([][]gramEntry, parts)
	parallel(parts, func(q int) {
		byGram := map[string][]gramWord{}
		for p := range gramsOut {
			for _, gw := range gramsOut[p][q] {
				byGram[gw.gram] = append(byGram[gw.gram], gw)
			}
		}
		out := make([]gramEntry, 0, len(byGram))
		for g, gws := range byGram {
			sort.Slice(gws, func(i, j int) bool { return gws[i].word < gws[j].word })
			e := gramEntry{gram: g, words: make([]string, len(gws)), counts: make([]int, len(gws)), size: len(g) + 12}
			for i, gw := range gws {
				e.words[i], e.counts[i] = gw.word, gw.df
				e.size += len(gw.word) + 4 + digits(gw.df)
			}
			out = append(out, e)
		}
		grams[q] = out
	})
	gramsOut = nil

	bi := &BuiltIndex{Common: slices.Concat(common...)}
	slices.Sort(bi.Common)

	allWords := slices.Concat(words...)
	sort.Slice(allWords, func(i, j int) bool { return allWords[i].word < allWords[j].word })
	var cur *WordShard
	var size int
	var first, last string
	for _, e := range allWords {
		if cur == nil {
			cur = &WordShard{Format: Format, Words: map[string][]int{}}
			size, first = 0, e.word
		}
		cur.Words[e.word] = e.list
		size += e.size
		last = e.word
		if size >= shardTarget {
			bi.Words = append(bi.Words, *cur)
			bi.WordRanges = append(bi.WordRanges, ShardRange{First: first, Last: last})
			cur = nil
		}
	}
	if cur != nil {
		bi.Words = append(bi.Words, *cur)
		bi.WordRanges = append(bi.WordRanges, ShardRange{First: first, Last: last})
	}

	allGrams := slices.Concat(grams...)
	sort.Slice(allGrams, func(i, j int) bool { return allGrams[i].gram < allGrams[j].gram })
	var gcur *GramShard
	for _, e := range allGrams {
		if gcur == nil {
			gcur = &GramShard{Format: Format, Grams: map[string][]string{}, Pages: map[string][]int{}}
			size, first = 0, e.gram
		}
		gcur.Grams[e.gram], gcur.Pages[e.gram] = e.words, e.counts
		size += e.size
		last = e.gram
		if size >= shardTarget {
			bi.Grams = append(bi.Grams, *gcur)
			bi.GramRanges = append(bi.GramRanges, ShardRange{First: first, Last: last})
			gcur = nil
		}
	}
	if gcur != nil {
		bi.Grams = append(bi.Grams, *gcur)
		bi.GramRanges = append(bi.GramRanges, ShardRange{First: first, Last: last})
	}
	return bi
}

// Workers is how many files the repository reads or writes at once:
// one per CPU, unless it's set (more than that helps on a network file
// system, where each file is mostly a wait).
var Workers = 0

func workers() int {
	if Workers > 0 {
		return Workers
	}
	return runtime.NumCPU()
}

// parallel calls fn(0)…fn(n-1), Workers (or one per CPU) at a time.
func parallel(n int, fn func(int)) {
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

func digits(n int) int {
	d := 1
	for n >= 10 {
		n /= 10
		d++
	}
	return d
}

func mostlyDigits(s string) bool {
	d := 0
	for _, r := range s {
		if unicode.IsDigit(r) {
			d++
		}
	}
	return d*2 >= len(s)
}
