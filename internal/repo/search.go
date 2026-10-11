// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// Hit is a search result.
type Hit struct {
	Name, Section, Description string
	Score                      float64
}

// Searcher queries a published index. web/search.js is the same algorithm
// for browsers; keep them in step.
type Searcher struct {
	Client *Client

	mu     sync.Mutex
	man    *Manifest
	words  *ShardManifest
	grams  *ShardManifest
	docs   *DocsManifest
	shards map[string]any
}

// Weights for words found other than exactly.
const (
	prefixWeight   = 0.5
	fuzzyWeight    = 0.3
	maxPrefixWords = 50
	maxFuzzyWords  = 5
)

// Search finds up to limit pages for a query. Pages matching every word
// come first; if none do, pages matching any word are returned.
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	terms := dedupe(Tokens(query))
	whole := strings.ToLower(strings.TrimSpace(query))
	if len(terms) == 0 && whole == "" {
		return nil, nil
	}

	perTerm := make([]map[int]float64, len(terms))
	for i, t := range terms {
		m, err := s.term(ctx, t)
		if err != nil {
			return nil, err
		}
		perTerm[i] = m
	}
	// The whole query as a page name ("git-commit", "[") is a bonus.
	bonus := map[int]float64{}
	if whole != "" && !strings.ContainsAny(whole, " \t") {
		ps, err := s.postings(ctx, whole)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			addBest(bonus, p[0], float64(p[1]))
		}
	}

	combine := func(all bool) map[int]float64 {
		out := map[int]float64{}
		for id, sc := range bonus {
			out[id] = sc
		}
		seen := map[int]bool{}
		for _, pt := range perTerm {
			for id := range pt {
				if seen[id] {
					continue
				}
				seen[id] = true
				total, n := 0.0, 0
				for _, other := range perTerm {
					if sc, ok := other[id]; ok {
						total += sc
						n++
					}
				}
				if all && n < len(perTerm) {
					continue
				}
				out[id] = total + bonus[id]
			}
		}
		return out
	}
	res := combine(true)
	if len(res) == len(bonus) {
		res = combine(false)
	}
	type idScore struct {
		id    int
		score float64
	}
	ranked := make([]idScore, 0, len(res))
	for id, sc := range res {
		ranked = append(ranked, idScore{id, sc})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].id < ranked[j].id
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	hits := make([]Hit, 0, len(ranked))
	for _, r := range ranked {
		d, err := s.doc(ctx, r.id)
		if err != nil {
			return nil, err
		}
		hits = append(hits, Hit{Name: d[0], Section: d[1], Description: d[2], Score: r.score})
	}
	return hits, nil
}

func dedupe(ts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range ts {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func addBest(m map[int]float64, id int, score float64) {
	if cur, ok := m[id]; !ok || score > cur {
		m[id] = score
	}
}

// term scores pages for one query word: exact matches, then words it's a
// prefix of, then (if nothing matched exactly) words that look like it.
func (s *Searcher) term(ctx context.Context, t string) (map[int]float64, error) {
	out := map[int]float64{}
	exact, err := s.postings(ctx, t)
	if err != nil {
		return nil, err
	}
	for _, p := range exact {
		addBest(out, p[0], float64(p[1]))
	}
	pre, err := s.prefixWords(ctx, t)
	if err != nil {
		return nil, err
	}
	for _, w := range pre {
		ps, err := s.postings(ctx, w)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			addBest(out, p[0], float64(p[1])*prefixWeight)
		}
	}
	if len(exact) == 0 {
		fz, err := s.fuzzyWords(ctx, t)
		if err != nil {
			return nil, err
		}
		for _, w := range fz {
			ps, err := s.postings(ctx, w)
			if err != nil {
				return nil, err
			}
			for _, p := range ps {
				addBest(out, p[0], float64(p[1])*fuzzyWeight)
			}
		}
	}
	return out, nil
}

func (s *Searcher) postings(ctx context.Context, w string) ([][2]int, error) {
	sh, err := s.wordShard(ctx, w)
	if err != nil || sh == nil {
		return nil, err
	}
	return sh.Words[w], nil
}

// doc looks up a page in the page table.
func (s *Searcher) doc(ctx context.Context, id int) ([3]string, error) {
	if s.docs.Size < 1 || id < 0 || id/s.docs.Size >= len(s.docs.Shards) {
		return [3]string{}, fmt.Errorf("search index has no page %d", id)
	}
	sh, err := s.shard(ctx, "docs", s.docs.Shards[id/s.docs.Size], func() any { return &DocShard{} })
	if err != nil {
		return [3]string{}, err
	}
	ds := sh.(*DocShard)
	if i := id % s.docs.Size; i < len(ds.Docs) {
		return ds.Docs[i], nil
	}
	return [3]string{}, fmt.Errorf("search index has no page %d", id)
}

// prefixWords lists indexed words that start with p (but aren't p).
func (s *Searcher) prefixWords(ctx context.Context, p string) ([]string, error) {
	if utf8.RuneCountInString(p) < 2 {
		return nil, nil
	}
	var out []string
	for _, r := range s.words.Shards {
		if r.Last < p || !strings.HasPrefix(r.First, p) && r.First > p {
			continue
		}
		sh, err := s.shard(ctx, "words", r.SHA256, func() any { return &WordShard{} })
		if err != nil {
			return nil, err
		}
		ws := sh.(*WordShard)
		var keys []string
		for w := range ws.Words {
			if w != p && strings.HasPrefix(w, p) {
				keys = append(keys, w)
			}
		}
		sort.Strings(keys)
		out = append(out, keys...)
		if len(out) >= maxPrefixWords {
			return out[:maxPrefixWords], nil
		}
	}
	return out, nil
}

// fuzzyWords finds words within a few typos of t, closest first. Words
// sharing a trigram with t are the candidates.
func (s *Searcher) fuzzyWords(ctx context.Context, t string) ([]string, error) {
	maxD := MaxTypos(t)
	if maxD == 0 {
		return nil, nil
	}
	pages := map[string]int{}
	for _, g := range queryGrams(t) {
		r := findRange(s.grams.Shards, g)
		if r == nil {
			continue
		}
		sh, err := s.shard(ctx, "grams", r.SHA256, func() any { return &GramShard{} })
		if err != nil {
			return nil, err
		}
		gs := sh.(*GramShard)
		for i, w := range gs.Grams[g] {
			if i < len(gs.Pages[g]) {
				pages[w] = gs.Pages[g][i]
			}
		}
	}
	type cand struct {
		w    string
		d, n int
	}
	var cs []cand
	for w, n := range pages {
		if w == t {
			continue
		}
		if d := EditDistance(t, w); d <= maxD {
			cs = append(cs, cand{w, d, n})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].d != cs[j].d {
			return cs[i].d < cs[j].d
		}
		if cs[i].n != cs[j].n {
			return cs[i].n > cs[j].n
		}
		return cs[i].w < cs[j].w
	})
	var out []string
	for i := 0; i < len(cs) && i < maxFuzzyWords; i++ {
		out = append(out, cs[i].w)
	}
	return out, nil
}

// queryGrams is Grams plus two start grams that catch a typo in the first
// letters: one skipping the second letter (lsit → ^li, for list) and one
// with the first two swapped (sl → ^ls).
func queryGrams(t string) []string {
	gs := Grams(t)
	if rs := []rune(t); len(rs) >= 3 {
		gs = append(gs, "^"+string(rs[0])+string(rs[2]), "^"+string(rs[1])+string(rs[0]))
	}
	return dedupe(gs)
}

// findRange finds the shard whose range holds key.
func findRange(rs []ShardRange, key string) *ShardRange {
	i := sort.Search(len(rs), func(i int) bool { return rs[i].Last >= key })
	if i < len(rs) && rs[i].First <= key {
		return &rs[i]
	}
	return nil
}

func (s *Searcher) wordShard(ctx context.Context, w string) (*WordShard, error) {
	r := findRange(s.words.Shards, w)
	if r == nil {
		return nil, nil
	}
	sh, err := s.shard(ctx, "words", r.SHA256, func() any { return &WordShard{} })
	if err != nil {
		return nil, err
	}
	return sh.(*WordShard), nil
}

func (s *Searcher) shard(ctx context.Context, kind, sha string, mk func() any) (any, error) {
	if !ValidHash(sha) {
		return nil, fmt.Errorf("bad shard hash %q in the %s manifest", sha, kind)
	}
	s.mu.Lock()
	if sh, ok := s.shards[sha]; ok {
		s.mu.Unlock()
		return sh, nil
	}
	s.mu.Unlock()
	sh := mk()
	if err := s.Client.Get(ctx, ShardPath(kind, sha), sh); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.shards[sha] = sh
	s.mu.Unlock()
	return sh, nil
}

func (s *Searcher) load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.man != nil {
		return nil
	}
	m, err := s.Client.Manifest(ctx)
	if err != nil {
		return err
	}
	var words, grams ShardManifest
	if err := s.Client.Get(ctx, IndexManifestPath("words", m.Index.Words), &words); err != nil {
		return err
	}
	if err := s.Client.Get(ctx, IndexManifestPath("grams", m.Index.Grams), &grams); err != nil {
		return err
	}
	var docs DocsManifest
	if err := s.Client.Get(ctx, IndexManifestPath("docs", m.Index.Docs), &docs); err != nil {
		return err
	}
	s.man, s.words, s.grams, s.docs = m, &words, &grams, &docs
	s.shards = map[string]any{}
	return nil
}
