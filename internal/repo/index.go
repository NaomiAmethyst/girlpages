// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"math"
	"runtime"
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
)

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
	Name, Section, Description string
	// Sources are the keys of the sources the page comes from, for
	// searching within one.
	Sources []string
	// Body is the page's text (the original, not the girly one: it's what
	// people search for).
	Body string
}

// BuiltIndex is a search index ready to write: shards with their manifests.
type BuiltIndex struct {
	Words      []WordShard
	WordRanges []ShardRange // SHA256 left for the writer
	Grams      []GramShard
	GramRanges []ShardRange
	Docs       []DocShard
	DocCount   int
}

// docShardSize is how many pages each DocShard holds. Small, because a
// search fetches the ones holding its top results.
const docShardSize = 128

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by for from has have if in into is it its
		no not of on or so such that the their then there these they this to was were will with you your
		can may must should which when where who also only than other any all each more most some`) {
		stopwords[w] = true
	}
}

// BuildIndex builds the search index for docs.
func BuildIndex(docs []IndexDoc) *BuiltIndex {
	// Doc ids go in name order, so ties in score come out alphabetical.
	docs = append([]IndexDoc(nil), docs...)
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].Name != docs[j].Name {
			return docs[i].Name < docs[j].Name
		}
		return docs[i].Section < docs[j].Section
	})
	type posting struct{ doc, score int }
	type docScores struct {
		scores map[string]int
		body   []string // distinct body words
	}
	per := make([]docScores, len(docs))
	parallel(len(docs), func(di int) {
		d := docs[di]
		scores := map[string]int{}
		name := strings.ToLower(d.Name)
		// Like man, prefer printf(1) to printf(3): each section step is
		// worth more than any amount of body text.
		scores[name] += scoreName + scoreSection*(manpage.SectionCount-manpage.SectionRank(d.Section))
		for _, t := range Tokens(d.Name) {
			if t != name {
				scores[t] += scoreNamePart
			}
		}
		seen := map[string]bool{}
		for _, t := range Tokens(d.Description) {
			if !seen[t] && !stopwords[t] {
				seen[t] = true
				scores[t] += scoreDesc
			}
		}
		tf := map[string]int{}
		for _, t := range Tokens(d.Body) {
			if !stopwords[t] {
				tf[t]++
			}
		}
		body := make([]string, 0, len(tf))
		for t, n := range tf {
			body = append(body, t)
			s := 1 + int(4*math.Log2(float64(n)))
			scores[t] += min(s, scoreBodyMax)
		}
		per[di] = docScores{scores, body}
	})

	postings := map[string][]posting{}
	bodyDF := map[string]int{}
	vocab := map[string]bool{} // words worth finding by trigram
	for di, d := range docs {
		vocab[strings.ToLower(d.Name)] = true
		for _, t := range Tokens(d.Name) {
			vocab[t] = true
		}
		for _, t := range Tokens(d.Description) {
			if !stopwords[t] {
				vocab[t] = true
			}
		}
		for _, t := range per[di].body {
			bodyDF[t]++
		}
		for t, s := range per[di].scores {
			postings[t] = append(postings[t], posting{di, s})
		}
		per[di] = docScores{}
	}

	// Drop body-only postings for words that are everywhere; they make
	// shards huge and searches no better.
	limit := int(commonBody * float64(len(docs)))
	if len(docs) >= 20 {
		for t, df := range bodyDF {
			if df <= limit {
				continue
			}
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

	words := make([]string, 0, len(postings))
	for t, ps := range postings {
		sort.Slice(ps, func(i, j int) bool {
			if ps[i].score != ps[j].score {
				return ps[i].score > ps[j].score
			}
			return ps[i].doc < ps[j].doc
		})
		if len(ps) > maxPostings {
			postings[t] = ps[:maxPostings]
		}
		words = append(words, t)
	}
	sort.Strings(words)

	bi := &BuiltIndex{DocCount: len(docs)}
	for i := 0; i < len(docs); i += docShardSize {
		ds := DocShard{Format: Format}
		for _, d := range docs[i:min(i+docShardSize, len(docs))] {
			ds.Docs = append(ds.Docs, [4]string{d.Name, d.Section, d.Description, strings.Join(d.Sources, " ")})
		}
		bi.Docs = append(bi.Docs, ds)
	}

	var cur *WordShard
	var size int
	var first, last string
	for _, w := range words {
		if cur == nil {
			cur = &WordShard{Format: Format, Words: map[string][][2]int{}}
			size, first = 0, w
		}
		ps := postings[w]
		list := make([][2]int, len(ps))
		size += len(w) + 6
		for i, p := range ps {
			list[i] = [2]int{p.doc, p.score}
			size += 4 + digits(p.doc) + digits(p.score)
		}
		cur.Words[w] = list
		last = w
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

	// Gram shards.
	df := map[string]int{}
	for t, ps := range postings {
		df[t] = len(ps)
	}
	for t, n := range bodyDF {
		df[t] = max(df[t], n)
	}
	grams := map[string][]string{}
	for w := range vocab {
		for _, g := range Grams(w) {
			grams[g] = append(grams[g], w)
		}
	}
	keys := make([]string, 0, len(grams))
	for g, ws := range grams {
		sort.Strings(ws)
		keys = append(keys, g)
	}
	sort.Strings(keys)
	var gcur *GramShard
	for _, g := range keys {
		if gcur == nil {
			gcur = &GramShard{Format: Format, Grams: map[string][]string{}, Pages: map[string][]int{}}
			size, first = 0, g
		}
		ws := grams[g]
		counts := make([]int, len(ws))
		size += len(g) + 12
		for i, w := range ws {
			counts[i] = df[w]
			size += len(w) + 4 + digits(df[w])
		}
		gcur.Grams[g], gcur.Pages[g] = ws, counts
		last = g
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

// parallel calls fn(0)…fn(n-1) on every CPU.
func parallel(n int, fn func(int)) {
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
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
