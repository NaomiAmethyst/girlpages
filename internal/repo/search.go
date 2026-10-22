// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
)

// Hit is a search result.
type Hit struct {
	Name, Section, Description string
	// Sources are the keys of the sources the page comes from.
	Sources []string
	Score   float64
}

// Searcher queries a published index. web/search.js is the same algorithm
// for browsers; keep them in step.
type Searcher struct {
	Client *Client
	// Mode is how query words match the index's: ModeWords ("" too) or
	// ModeNgrams.
	Mode string

	mu     sync.Mutex
	man    *Manifest
	words  *ShardManifest
	grams  *ShardManifest
	names  *TableManifest
	docs   *TableManifest
	shards map[string]any
}

// Search modes.
const (
	// ModeWords finds words, the words they start, and (for words not in
	// the index) words a typo or two away.
	ModeWords = "words"
	// ModeNgrams finds words, and the words they're anywhere inside of
	// (dir finds mkdir and readdir), by the trigram index: words of three
	// letters or more, that is; shorter ones are found as words.
	ModeNgrams = "ngrams"
)

// Weights for words found other than exactly.
const (
	prefixWeight = 0.5
	// prefixCap is the most a word a query word is only the start of can
	// score, before prefixWeight: as much as a word of the name. Otherwise
	// "files" would find filesystems(5) by its whole name first.
	prefixCap      = 100
	fuzzyWeight    = 0.3
	maxPrefixWords = 50
	maxFuzzyWords  = 5
	// Words a query word is inside of score as words it starts do, by how
	// much of them it is, and the most common of them count.
	infixWeight   = prefixWeight
	maxInfixWords = 100
)

// Search finds up to limit pages for a query. Pages matching every word
// come first; if none do, pages matching any word are returned. Phrases in
// quotes and pkg: filters keep to pages with them (see ParseQuery).
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	return s.SearchIn(ctx, query, "", limit)
}

// maxFiltered is how many ranked pages a search within a source looks
// through for ones from that source.
const maxFiltered = 2000

// maxPhraseChecks is how many ranked pages a search for a phrase reads
// the text of, looking for it: each is a file or two to fetch.
const maxPhraseChecks = 500

// phraseJobs is how many pages' text are read at once.
const phraseJobs = 16

// SearchIn is Search limited to pages from one source (a key such as
// "debian" or "debian/trixie"; "" for all).
func (s *Searcher) SearchIn(ctx context.Context, query, source string, limit int) ([]Hit, error) {
	r, err := s.Find(ctx, query, source, limit)
	if err != nil {
		return nil, err
	}
	return r.Hits, nil
}

// Results are what Find found.
type Results struct {
	Hits []Hit
	// Partial says a phrase's words are each in so many pages that only
	// the pages they matter most in were looked through for it.
	Partial bool
	// TooCommon says the query is only phrases of words too common to
	// search by (stopwords, and words nearly every page has).
	TooCommon bool
}

// Find is SearchIn, saying how complete the search was.
func (s *Searcher) Find(ctx context.Context, query, source string, limit int) (*Results, error) {
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	rk, err := s.rank(ctx, query)
	if err != nil {
		return nil, err
	}
	out := &Results{Partial: rk.partial, TooCommon: rk.tooCommon}
	max := len(rk.ids)
	switch {
	case len(rk.phrases) > 0:
		max = min(max, maxPhraseChecks)
		out.Partial = out.Partial || len(rk.ids) > maxPhraseChecks
	case source != "":
		max = min(max, maxFiltered)
	}
	// A batch at a time, each page's checked at once, kept in order.
	batch := 1
	if len(rk.phrases) > 0 {
		batch = phraseJobs
	}
	for i := 0; i < max && (limit <= 0 || len(out.Hits) < limit); i += batch {
		ids := rk.ids[i:min(i+batch, max)]
		hits := make([]*Hit, len(ids))
		errs := make([]error, len(ids))
		var wg sync.WaitGroup
		for j, r := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				hits[j], errs[j] = s.check(ctx, r, source, rk.phrases)
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
		for _, h := range hits {
			if h != nil && (limit <= 0 || len(out.Hits) < limit) {
				out.Hits = append(out.Hits, *h)
			}
		}
	}
	return out, nil
}

// check looks up a ranked page, giving its Hit if it's from source (if
// that's not "") and has the phrases, nil if not.
func (s *Searcher) check(ctx context.Context, r idScore, source string, phrases [][]string) (*Hit, error) {
	d, err := s.doc(ctx, r.id)
	if err != nil {
		return nil, err
	}
	srcs := strings.Fields(d[3])
	if source != "" && !contains(srcs, source) {
		return nil, nil
	}
	if len(phrases) > 0 {
		ok, err := s.hasPhrases(ctx, d[0], d[1], phrases)
		if err != nil || !ok {
			return nil, err
		}
	}
	return &Hit{Name: d[0], Section: d[1], Description: d[2], Sources: srcs, Score: r.score}, nil
}

type idScore struct {
	id    int
	score float64
}

// ranking is a query's pages, best first, before any are looked at: the
// phrases are still to check in their text.
type ranking struct {
	ids                []idScore
	phrases            [][]string
	partial, tooCommon bool
}

// rank scores the pages for a query, best first.
func (s *Searcher) rank(ctx context.Context, query string) (*ranking, error) {
	q := ParseQuery(query)
	rk := &ranking{phrases: q.Phrases}
	terms := dedupe(SeriesTerms(Tokens(q.Words), s.man))
	whole := strings.ToLower(q.Words)

	// Pages a phrase could be in (nil: any), and from the packages asked
	// for (nil: any).
	var must map[int]float64
	if len(q.Phrases) > 0 {
		var err error
		if must, rk.partial, err = s.phrasePages(ctx, q.Phrases); err != nil {
			return nil, err
		}
	}
	var pkgs map[int]bool
	if len(q.Packages) > 0 {
		pkgs = map[int]bool{}
		for _, k := range q.Packages {
			ps, err := s.postings(ctx, PackagePrefix+k)
			if err != nil {
				return nil, err
			}
			for _, p := range ps {
				pkgs[p[0]] = true
			}
		}
	}
	// A package's pages are all listed, so with one, every page from it
	// can be looked through for the phrases: they score by the phrases'
	// words, but needn't be among the best pages for them.
	scores := must
	if pkgs != nil && rk.partial {
		must, rk.partial = nil, false
	}
	allowed := func(id int) bool {
		if must != nil {
			if _, ok := must[id]; !ok {
				return false
			}
		}
		return pkgs == nil || pkgs[id]
	}

	res := map[int]float64{}
	if len(terms) == 0 && whole == "" {
		switch {
		case must != nil:
			for id, sc := range must {
				if allowed(id) {
					res[id] = sc
				}
			}
		case pkgs != nil:
			// Just a package: its pages, in the page table's order (by
			// section, then name), or by a phrase's words.
			for id := range pkgs {
				res[id] = scores[id]
			}
		default:
			rk.tooCommon = len(q.Phrases) > 0
			return rk, nil
		}
	} else {
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
				if allowed(p[0]) {
					addBest(bonus, p[0], float64(p[1]))
				}
			}
		}
		combine := func(all bool) map[int]float64 {
			out := map[int]float64{}
			for id, sc := range bonus {
				out[id] = sc + scores[id]
			}
			seen := map[int]bool{}
			for _, pt := range perTerm {
				for id := range pt {
					if seen[id] {
						continue
					}
					seen[id] = true
					if !allowed(id) {
						continue
					}
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
					out[id] = total + bonus[id] + scores[id]
				}
			}
			return out
		}
		res = combine(true)
		if len(res) == len(bonus) {
			res = combine(false)
		}
	}
	rk.ids = make([]idScore, 0, len(res))
	for id, sc := range res {
		rk.ids = append(rk.ids, idScore{id, sc})
	}
	sort.Slice(rk.ids, func(i, j int) bool {
		a, b := rk.ids[i], rk.ids[j]
		if a.score != b.score {
			return a.score > b.score
		}
		return a.id < b.id
	})
	return rk, nil
}

// phrasePages finds the pages phrases could be in, scored by their words:
// those having every word of them the index can say where it is
// (stopwords and words nearly every page has it can't). A word's postings
// are its best maxPostings pages, so when there are fewer they're all of
// them, and the pages having all those words are every page that could
// have the phrases; if every word has more (partial), the pages having
// most of them, of their best, are what can be looked through. nil means
// any page could (no word can be searched by).
func (s *Searcher) phrasePages(ctx context.Context, phrases [][]string) (map[int]float64, bool, error) {
	common := map[string]bool{}
	for _, w := range s.words.Common {
		common[w] = true
	}
	var lists [][][2]int
	seen := map[string]bool{}
	for _, ph := range phrases {
		for _, w := range ph {
			if seen[w] || stopwords[w] || common[w] {
				continue
			}
			seen[w] = true
			ps, err := s.postings(ctx, w)
			if err != nil {
				return nil, false, err
			}
			if len(ps) == 0 {
				return map[int]float64{}, false, nil
			}
			lists = append(lists, ps)
		}
	}
	return phraseCandidates(lists)
}

// phraseCandidates is phrasePages' arithmetic, given each word's postings.
func phraseCandidates(lists [][][2]int) (map[int]float64, bool, error) {
	if len(lists) == 0 {
		return nil, false, nil
	}
	score := map[int]float64{}
	count := map[int]int{}
	complete := 0
	inAll := map[int]int{} // pages: how many complete lists have them
	for _, ps := range lists {
		whole := len(ps) < maxPostings
		if whole {
			complete++
		}
		for _, p := range ps {
			score[p[0]] += float64(p[1])
			count[p[0]]++
			if whole {
				inAll[p[0]]++
			}
		}
	}
	out := map[int]float64{}
	if complete > 0 {
		for id, n := range inAll {
			if n == complete {
				out[id] = score[id]
			}
		}
		return out, false, nil
	}
	for id, n := range count {
		if n == len(lists) {
			out[id] = score[id]
		}
	}
	if len(out) == 0 {
		// None of the best pages for each word has them all: those with
		// the most of them first.
		for id, n := range count {
			out[id] = score[id] * float64(n) / float64(len(lists))
		}
	}
	return out, true, nil
}

// hasPhrases reports whether a page's text (any of its versions', as the
// index has them all) has every phrase.
func (s *Searcher) hasPhrases(ctx context.Context, name, section string, phrases [][]string) (bool, error) {
	l, err := s.Client.Page(ctx, name, section)
	if err != nil {
		return false, err
	}
	var text strings.Builder
	for _, v := range l.Versions {
		variant := VariantMan
		info, ok := v.Variants[variant]
		if !ok {
			variant = VariantGirl
			if info, ok = v.Variants[variant]; !ok {
				continue
			}
		}
		doc, err := s.Client.VariantAt(ctx, v.Source, variant, info.SHA256)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		text.WriteString(doc.Text())
		text.WriteString("\n\n")
	}
	for _, ph := range phrases {
		if !HasPhrase(text.String(), ph) {
			return false, nil
		}
	}
	return true, nil
}

// Named finds the pages called name (ignoring case), in man's order: by
// section, as man looks through them. Every page's whole name is a word
// in the index, scoring more than the word can for any page not called
// it, so its postings have them all.
func (s *Searcher) Named(ctx context.Context, name string) ([]Hit, error) {
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return nil, nil
	}
	ps, err := s.postings(ctx, want)
	if err != nil {
		return nil, err
	}
	var hits []Hit
	for _, p := range ps {
		if p[1] < scoreName {
			continue
		}
		d, err := s.doc(ctx, p[0])
		if err != nil {
			return nil, err
		}
		if strings.ToLower(d[0]) != want {
			continue
		}
		hits = append(hits, Hit{Name: d[0], Section: d[1], Description: d[2], Sources: strings.Fields(d[3]), Score: float64(p[1])})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if ra, rb := manpage.SectionRank(a.Section), manpage.SectionRank(b.Section); ra != rb {
			return ra < rb
		}
		if a.Section != b.Section {
			// 3 before 3p, then 3p before 3ssl.
			if len(a.Section) != len(b.Section) && (strings.HasPrefix(b.Section, a.Section) || strings.HasPrefix(a.Section, b.Section)) {
				return len(a.Section) < len(b.Section)
			}
			return a.Section < b.Section
		}
		return a.Name < b.Name
	})
	return hits, nil
}

// SeriesTerms joins a series' name and the number after it in a query
// ("rfc 9110") into one word, as documents in a series are indexed
// (rfc9110).
func SeriesTerms(terms []string, m *Manifest) []string {
	series := map[string]bool{}
	if m != nil {
		for _, e := range m.Sections {
			if manpage.IsSeries(e.Section) {
				series[strings.ToLower(e.Section)] = true
			}
		}
	}
	var out []string
	for i := 0; i < len(terms); i++ {
		if series[terms[i]] && i+1 < len(terms) && isDigits(terms[i+1]) {
			out = append(out, terms[i]+strings.TrimLeft(terms[i+1], "0"))
			i++
			continue
		}
		out = append(out, terms[i])
	}
	return out
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
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
			addBest(out, p[0], float64(min(p[1], prefixCap))*prefixWeight)
		}
	}
	if s.Mode == ModeNgrams && len([]rune(t)) >= 3 {
		in, err := s.infixWords(ctx, t)
		if err != nil {
			return nil, err
		}
		lt := float64(len([]rune(t)))
		for _, w := range in {
			ps, err := s.postings(ctx, w)
			if err != nil {
				return nil, err
			}
			// The more of the word it is, the better: kdir finds mkdir
			// before arcmkdir. (Uncapped, as prefixes aren't: halved and
			// less than whole, it can't beat the word itself, and a page
			// called mkdir should beat one with mkdir in its name.)
			fit := lt / float64(len([]rune(w)))
			for _, p := range ps {
				addBest(out, p[0], float64(p[1])*infixWeight*fit)
			}
		}
		return out, nil
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
	flat := sh.Words[w]
	out := make([][2]int, 0, len(flat)/2)
	for i := 0; i+1 < len(flat); i += 2 {
		out = append(out, [2]int{flat[i], flat[i+1]})
	}
	return out, nil
}

// doc looks up a page in the page table: [name, section, description,
// sources]. A name of "" is an id no page has any more.
func (s *Searcher) doc(ctx context.Context, id int) ([4]string, error) {
	var out [4]string
	nr, ok := s.names.Shards.Find(id)
	dr, ok2 := s.docs.Shards.Find(id)
	if !ok || !ok2 {
		return out, fmt.Errorf("search index has no page %d", id)
	}
	sh, err := s.shard(ctx, "names", nr.SHA256, func() any { return &NameShard{} })
	if err != nil {
		return out, err
	}
	ns := sh.(*NameShard)
	if i := id - ns.First; i >= 0 && i < len(ns.Names) {
		out[0], out[1] = ns.Names[i].Name, ns.Names[i].Section
	}
	sh, err = s.shard(ctx, "docs", dr.SHA256, func() any { return &DocShard{} })
	if err != nil {
		return out, err
	}
	ds := sh.(*DocShard)
	if i := id - ds.First; i >= 0 && i < len(ds.Docs) {
		out[2], out[3] = ds.Docs[i].Description, ds.Docs[i].Sources
	}
	return out, nil
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
			if w != p && strings.HasPrefix(w, p) && !strings.HasPrefix(w, PackagePrefix) {
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

// innerGrams is a word's trigrams but those marking its start and end:
// what any word it's inside of has too.
func innerGrams(t string) []string {
	rs := []rune(t)
	var out []string
	for i := 0; i+3 <= len(rs); i++ {
		out = append(out, string(rs[i:i+3]))
	}
	return dedupe(out)
}

// infixWords lists indexed words (of those the trigram index has) that t
// is inside of, but isn't, those in the most pages first: the words having
// every trigram t has, that really have t.
func (s *Searcher) infixWords(ctx context.Context, t string) ([]string, error) {
	gs := innerGrams(t)
	if len(gs) == 0 {
		return nil, nil
	}
	var cands map[string]int // word → how many pages have it
	for _, g := range gs {
		r := findRange(s.grams.Shards, g)
		if r == nil {
			return nil, nil
		}
		sh, err := s.shard(ctx, "grams", r.SHA256, func() any { return &GramShard{} })
		if err != nil {
			return nil, err
		}
		gsh := sh.(*GramShard)
		next := map[string]int{}
		for i, w := range gsh.Grams[g] {
			n := 0
			if i < len(gsh.Pages[g]) {
				n = gsh.Pages[g][i]
			}
			if _, ok := cands[w]; cands == nil || ok {
				next[w] = n
			}
		}
		cands = next
		if len(cands) == 0 {
			return nil, nil
		}
	}
	type cand struct {
		w string
		n int
	}
	var cs []cand
	for w, n := range cands {
		if w != t && strings.Contains(w, t) {
			cs = append(cs, cand{w, n})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].n != cs[j].n {
			return cs[i].n > cs[j].n
		}
		return cs[i].w < cs[j].w
	})
	var out []string
	for i := 0; i < len(cs) && i < maxInfixWords; i++ {
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
	var names, docs TableManifest
	if err := s.Client.Get(ctx, IndexManifestPath("names", m.Index.Names), &names); err != nil {
		return err
	}
	if err := s.Client.Get(ctx, IndexManifestPath("docs", m.Index.Docs), &docs); err != nil {
		return err
	}
	s.man, s.words, s.grams, s.names, s.docs = m, &words, &grams, &names, &docs
	s.shards = map[string]any{}
	return nil
}
