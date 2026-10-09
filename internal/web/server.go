// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/core"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
)

// Server is the girl --serve web app: browse by section, search, and read
// pages, converting uncached ones on demand.
type Server struct {
	Girl *core.Girl
	// CachedOnly turns off on-demand conversion.
	CachedOnly bool
	Version    string
	// Log receives conversion progress; nil discards it.
	Log io.Writer
	// IndexTTL is how long the page list is reused (default 5 minutes).
	IndexTTL time.Duration
	// WaitBeforeSpinner is how long a request waits for a conversion to
	// finish before showing the waiting page (default 1.5s), so aliases of
	// already-converted pages appear straight away.
	WaitBeforeSpinner time.Duration

	ctx context.Context

	idxMu sync.Mutex
	idx   *index

	doneMu   sync.Mutex
	doneAt   time.Time
	doneSet  map[string]bool
	jobMu    sync.Mutex
	jobs     map[string]*job
	initOnce sync.Once
}

type indexEntry struct {
	page  manpage.Page
	desc  string
	lname string
	ldesc string
}

type index struct {
	built time.Time
	pages []indexEntry
	byKey map[string]int
	// byName lists the sections each page name appears in.
	byName map[string][]string
}

// link is the URL for a reference like printf(3), or "" if there's no such
// page here. A bare section number finds subsections too (3 → 3ssl).
func (idx *index) link(name, sec string) string {
	if _, ok := idx.byKey[manpage.DescKey(name, sec)]; ok {
		return pageURL(name, sec)
	}
	for _, s := range idx.byName[name] {
		if strings.HasPrefix(s, sec) {
			return pageURL(name, s)
		}
	}
	return ""
}

type job struct {
	page     manpage.Page
	refresh  bool
	started  time.Time
	done     chan struct{}
	finished time.Time
	res      core.Result
	err      error

	mu          sync.Mutex
	part, parts int
}

func (s *Server) init(ctx context.Context) {
	s.initOnce.Do(func() {
		s.ctx = ctx
		s.jobs = map[string]*job{}
		if s.IndexTTL == 0 {
			s.IndexTTL = 5 * time.Minute
		}
		if s.WaitBeforeSpinner == 0 {
			s.WaitBeforeSpinner = 1500 * time.Millisecond
		}
		if s.Log == nil {
			s.Log = io.Discard
		}
	})
}

// Handler returns the app. Conversions it starts live as long as ctx.
func (s *Server) Handler(ctx context.Context) http.Handler {
	s.init(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /static/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		io.WriteString(w, styleCSS)
	})
	mux.HandleFunc("GET /section/{sec}", s.section)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /random", s.random)
	mux.HandleFunc("GET /man/{name}", s.bareName)
	mux.HandleFunc("GET /man/{sec}/{name}", s.page)
	mux.HandleFunc("GET /man/{sec}/{name}/original", s.original)
	mux.HandleFunc("POST /man/{sec}/{name}/convert", s.convert)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.oops(w, http.StatusNotFound, &oopsView{Emoji: "🙈", Heading: "this page doesn't exist, bestie",
			Message: "there's nothing at " + r.URL.Path + "."})
	})
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'self' 'unsafe-inline'; "+
			"script-src 'unsafe-inline'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}

// ListenAndServe serves until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string, log io.Writer) error {
	if s.Log == nil {
		s.Log = log
	}
	if !s.CachedOnly {
		if _, err := s.Girl.Backend(); err != nil {
			fmt.Fprintf(log, "girl: no LLM backend (%v), so only cached pages will be shown\n", err)
			s.CachedOnly = true
		}
	}
	s.init(ctx)
	idx, err := s.index()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Fprintf(log, "💅 girlpages is serving %d pages at http://%s/\n", len(idx.pages), displayAddr(ln.Addr()))
	srv := &http.Server{Handler: s.Handler(ctx), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func displayAddr(a net.Addr) string {
	host, port, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

// index returns the list of pages, rebuilding it when it gets old.
func (s *Server) index() (*index, error) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	if s.idx != nil && time.Since(s.idx.built) < s.IndexTTL {
		return s.idx, nil
	}
	ctx := s.ctx
	mo := s.Girl.Man
	dirs, err := manpage.ManPath(ctx, mo)
	if err != nil {
		return nil, err
	}
	pages, err := manpage.Enumerate(dirs, nil)
	if err != nil {
		return nil, err
	}
	descs, _ := manpage.Descriptions(ctx, mo) // descriptions are a nicety
	idx := &index{built: time.Now(), byKey: map[string]int{}, byName: map[string][]string{}}
	for _, p := range pages {
		d := descs[manpage.DescKey(p.Name, p.Section)]
		idx.byKey[manpage.DescKey(p.Name, p.Section)] = len(idx.pages)
		idx.byName[p.Name] = append(idx.byName[p.Name], p.Section)
		idx.pages = append(idx.pages, indexEntry{page: p, desc: d, lname: strings.ToLower(p.Name), ldesc: strings.ToLower(d)})
	}
	s.idx = idx
	return idx, nil
}

// girlified is the set of source paths with a cached conversion. Walking
// the caches isn't free, so it's reused briefly.
func (s *Server) girlified() map[string]bool {
	s.doneMu.Lock()
	defer s.doneMu.Unlock()
	if s.doneSet != nil && time.Since(s.doneAt) < 15*time.Second {
		return s.doneSet
	}
	set := map[string]bool{}
	for _, d := range s.Girl.Caches {
		_ = d.Entries(func(e *cache.Entry) error { set[e.Source] = true; return nil })
	}
	s.doneSet, s.doneAt = set, time.Now()
	return set
}

func (s *Server) forgetGirlified() {
	s.doneMu.Lock()
	s.doneSet = nil
	s.doneMu.Unlock()
}

var sectionInfo = map[string][2]string{
	"1": {"User commands", "💻"},
	"2": {"System calls", "📞"},
	"3": {"Library functions", "📚"},
	"4": {"Special files", "🔌"},
	"5": {"File formats & config", "📝"},
	"6": {"Games", "🎮"},
	"7": {"Miscellany", "🌈"},
	"8": {"System administration", "👑"},
	"9": {"Kernel routines", "🧠"},
	"n": {"Tcl/Tk", "🪶"},
	"l": {"Local", "🏡"},
	"o": {"Old", "🕰️"},
}

func sectionName(key string) (string, string) {
	if info, ok := sectionInfo[key[:1]]; ok {
		return info[0], info[1]
	}
	return "Section " + key, "📖"
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	idx, err := s.index()
	if err != nil {
		s.fail(w, err)
		return
	}
	done := s.girlified()
	cards := map[string]*sectionCard{}
	v := &view{Title: "the manual, but make it girly", Version: s.Version}
	for _, e := range idx.pages {
		key := e.page.Section[:1]
		c := cards[key]
		if c == nil {
			name, emoji := sectionName(key)
			c = &sectionCard{Key: key, Name: name, Emoji: emoji}
			cards[key] = c
		}
		c.Count++
		v.Total++
		if done[e.page.Path] {
			c.Girlified++
			v.Girlified++
		}
	}
	for _, c := range cards {
		v.Sections = append(v.Sections, *c)
	}
	sort.Slice(v.Sections, func(i, j int) bool {
		a, b := v.Sections[i].Key, v.Sections[j].Key
		if isDigit(a) != isDigit(b) {
			return isDigit(a)
		}
		return a < b
	})
	s.render(w, http.StatusOK, "home", v)
}

func isDigit(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

func (s *Server) section(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("sec")
	if !manpage.IsSection(key) {
		s.notFound(w, "that's not a section, bestie")
		return
	}
	idx, err := s.index()
	if err != nil {
		s.fail(w, err)
		return
	}
	onlyDone := r.URL.Query().Get("girlified") != ""
	done := s.girlified()
	top := key[:1]
	subs := map[string]int{}
	var items []listItem
	matched := 0
	for _, e := range idx.pages {
		if strings.HasPrefix(e.page.Section, top) {
			subs[e.page.Section]++
		}
		if !strings.HasPrefix(e.page.Section, key) {
			continue
		}
		matched++
		if onlyDone && !done[e.page.Path] {
			continue
		}
		items = append(items, s.item(e, done))
	}
	if matched == 0 {
		s.notFound(w, "there are no pages in section "+key)
		return
	}
	name, emoji := sectionName(key)
	v := &view{Title: "section " + key, Version: s.Version, Heading: "section " + key + " · " + name + " " + emoji}
	girlifiedCount := 0
	for _, it := range items {
		if it.Girlified {
			girlifiedCount++
		}
	}
	v.Sub = fmt.Sprintf("%s, %d girlified ✨", pages(len(items)), girlifiedCount)
	q := ""
	if onlyDone {
		q = "?girlified=1"
	}
	v.Chips = append(v.Chips, chip{Label: "all of " + top, URL: "/section/" + top + q, On: key == top})
	var subKeys []string
	for k := range subs {
		subKeys = append(subKeys, k)
	}
	sort.Strings(subKeys)
	if len(subKeys) > 1 {
		for _, k := range subKeys {
			v.Chips = append(v.Chips, chip{Label: fmt.Sprintf("%s (%d)", k, subs[k]), URL: "/section/" + k + q, On: key == k})
		}
	}
	if onlyDone {
		v.Chips = append(v.Chips, chip{Label: "✨ girlified only", URL: "/section/" + key, On: true})
	} else {
		v.Chips = append(v.Chips, chip{Label: "✨ girlified only", URL: "/section/" + key + "?girlified=1"})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	v.Groups, v.Letters = groupByLetter(items)
	s.render(w, http.StatusOK, "list", v)
}

func groupByLetter(items []listItem) ([]group, []string) {
	var groups []group
	var letters []string
	for _, it := range items {
		l := "#"
		if r := []rune(it.Name); len(r) > 0 && unicode.IsLetter(r[0]) && r[0] < 0x80 {
			l = strings.ToUpper(string(r[0]))
		}
		if len(groups) == 0 || groups[len(groups)-1].Letter != l {
			groups = append(groups, group{Letter: l})
			letters = append(letters, l)
		}
		groups[len(groups)-1].Items = append(groups[len(groups)-1].Items, it)
	}
	return groups, letters
}

func (s *Server) item(e indexEntry, done map[string]bool) listItem {
	return listItem{Name: e.page.Name, Section: e.page.Section, Desc: e.desc,
		URL: pageURL(e.page.Name, e.page.Section), Girlified: done[e.page.Path]}
}

var refRE = regexp.MustCompile(`^\s*([^\s()]+)\s*\(([^)\s]+)\)\s*$`)

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	idx, err := s.index()
	if err != nil {
		s.fail(w, err)
		return
	}
	// "ls(1)" goes straight to the page.
	if m := refRE.FindStringSubmatch(q); m != nil {
		if i, ok := idx.byKey[manpage.DescKey(m[1], m[2])]; ok {
			p := idx.pages[i].page
			http.Redirect(w, r, pageURL(p.Name, p.Section), http.StatusSeeOther)
			return
		}
	}
	words := strings.Fields(strings.ToLower(q))
	type hit struct {
		score, order int
		e            indexEntry
	}
	var hits []hit
	for i, e := range idx.pages {
		score := 0
		for _, wd := range words {
			switch {
			case e.lname == wd:
			case strings.HasPrefix(e.lname, wd):
				score += 1
			case strings.Contains(e.lname, wd):
				score += 2
			case strings.Contains(e.ldesc, wd):
				score += 4
			default:
				score = -1
			}
			if score < 0 {
				break
			}
		}
		if score >= 0 {
			hits = append(hits, hit{score, i, e})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score < hits[j].score
		}
		return hits[i].order < hits[j].order
	})
	const max = 250
	v := &view{Title: "search: " + q, Version: s.Version, Query: q, Heading: "search: “" + q + "”"}
	switch {
	case len(hits) > max:
		v.Sub = fmt.Sprintf("%d matches, showing the best %d 🔎", len(hits), max)
		hits = hits[:max]
	case len(hits) == 1:
		v.Sub = "1 match 💖"
	default:
		v.Sub = fmt.Sprintf("%d matches 🔎", len(hits))
	}
	done := s.girlified()
	var items []listItem
	for _, h := range hits {
		items = append(items, s.item(h.e, done))
	}
	if len(items) > 0 {
		v.Groups = []group{{Items: items}}
	}
	s.render(w, http.StatusOK, "list", v)
}

func (s *Server) random(w http.ResponseWriter, r *http.Request) {
	idx, err := s.index()
	if err != nil {
		s.fail(w, err)
		return
	}
	done := s.girlified()
	var pool []manpage.Page
	for _, e := range idx.pages {
		if done[e.page.Path] {
			pool = append(pool, e.page)
		}
	}
	if len(pool) == 0 {
		for _, e := range idx.pages {
			pool = append(pool, e.page)
		}
	}
	if len(pool) == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	p := pool[rand.IntN(len(pool))]
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, pageURL(p.Name, p.Section), http.StatusSeeOther)
}

// validName rejects anything man might take as an option or a file path.
func validName(n string) bool {
	return n != "" && n[0] != '-' && n[0] != '.' && !strings.ContainsAny(n, "/\x00") && len(n) < 256
}

// resolve finds the page for /man/{sec}/{name}, redirecting to the page's
// real section when man found it under a subsection.
func (s *Server) resolve(w http.ResponseWriter, r *http.Request, suffix string) (manpage.Page, bool) {
	sec, name := r.PathValue("sec"), r.PathValue("name")
	if !manpage.IsSection(sec) || !validName(name) {
		s.notFound(w, "that doesn't look like a man page, bestie")
		return manpage.Page{}, false
	}
	idx, err := s.index()
	if err != nil {
		s.fail(w, err)
		return manpage.Page{}, false
	}
	if i, ok := idx.byKey[manpage.DescKey(name, sec)]; ok {
		return idx.pages[i].page, true
	}
	mo := s.Girl.Man
	mo.Stderr = io.Discard
	mo.All, mo.Sections = false, ""
	pages, _, err := manpage.Locate(r.Context(), []string{sec, name}, mo)
	if err != nil || len(pages) == 0 {
		s.notFound(w, "there's no "+name+"("+sec+") on this system")
		return manpage.Page{}, false
	}
	p := pages[0]
	if p.Section != sec || p.Name != name {
		if p.Section != "" && validName(p.Name) {
			http.Redirect(w, r, pageURL(p.Name, p.Section)+suffix, http.StatusFound)
			return manpage.Page{}, false
		}
	}
	return p, true
}

func (s *Server) bareName(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validName(name) {
		s.notFound(w, "that doesn't look like a man page, bestie")
		return
	}
	mo := s.Girl.Man
	mo.Stderr = io.Discard
	mo.All, mo.Sections = false, ""
	pages, _, err := manpage.Locate(r.Context(), []string{name}, mo)
	if err != nil || len(pages) == 0 || pages[0].Section == "" {
		s.notFound(w, "there's no "+name+" on this system")
		return
	}
	http.Redirect(w, r, pageURL(pages[0].Name, pages[0].Section), http.StatusFound)
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	p, ok := s.resolve(w, r, "")
	if !ok {
		return
	}
	if j := s.job(p.Path); j != nil {
		s.jobPage(w, p, j)
		return
	}
	if res, ok := s.Girl.Cached(p); ok {
		s.showPage(w, p, res)
		return
	}
	if s.CachedOnly {
		s.oops(w, http.StatusNotFound, &oopsView{Emoji: "🥺", Heading: "not girlified yet",
			Message:  p.Title() + " hasn't been girlified, and this server isn't converting new pages.",
			Original: pageURL(p.Name, p.Section) + "/original"})
		return
	}
	j := s.startJob(p, false)
	select {
	case <-j.done:
	case <-time.After(s.WaitBeforeSpinner):
	case <-r.Context().Done():
		return
	}
	s.jobPage(w, p, j)
}

func (s *Server) jobPage(w http.ResponseWriter, p manpage.Page, j *job) {
	select {
	case <-j.done:
	default:
		s.waiting(w, p, j)
		return
	}
	if j.err != nil {
		u := pageURL(p.Name, p.Section)
		v := &oopsView{Emoji: "😭", Heading: "couldn't girlify " + p.Title(),
			Message: "the model had a moment. you can try again, or read the original.",
			Detail:  j.err.Error(), Original: u + "/original"}
		if !s.CachedOnly {
			v.Retry = u + "/convert"
		}
		s.oops(w, http.StatusBadGateway, v)
		return
	}
	s.showPage(w, p, j.res)
}

func (s *Server) showPage(w http.ResponseWriter, p manpage.Page, res core.Result) {
	title := core.DisplayTitle(p, res.Title)
	link := pageURL
	if idx, err := s.index(); err == nil {
		link = idx.link
	}
	d := newDocView(p, title, res.Markup, res.Entry, res.Stale, link)
	d.Crumbs, d.Actions, d.CanConvert = true, true, !s.CachedOnly
	s.render(w, http.StatusOK, "page", &view{Title: title, Version: s.Version, Docs: []docView{d}})
}

var moods = []string{
	"reading the original 📖", "doing her makeup 💄", "picking the perfect emoji 🎀", "fact-checking every flag 🔍",
	"adding sparkles ✨", "asking the group chat 📱", "choosing a colour palette 🎨", "being iconic 👑",
}
var spinners = []string{"💅", "✨", "💖", "🎀", "🦋", "🌸"}

func (s *Server) waiting(w http.ResponseWriter, p manpage.Page, j *job) {
	el := time.Since(j.started)
	tick := int(el / (4 * time.Second))
	j.mu.Lock()
	part, parts := j.part, j.parts
	j.mu.Unlock()
	wv := &waitView{Title: p.Title(), Mood: moods[tick%len(moods)], Emoji: spinners[tick%len(spinners)],
		Elapsed: el.Round(time.Second).String(), URL: pageURL(p.Name, p.Section)}
	if parts > 1 {
		wv.Part, wv.Parts = min(part+1, parts), parts
		wv.Percent = 100 * part / parts
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, http.StatusAccepted, "waiting", &view{Title: "girlifying " + p.Title(), Version: s.Version, Refresh: 2, Wait: wv})
}

func (s *Server) original(w http.ResponseWriter, r *http.Request) {
	p, ok := s.resolve(w, r, "/original")
	if !ok {
		return
	}
	text, err := manpage.Text(r.Context(), p.Path, s.Girl.Man)
	if err != nil {
		s.fail(w, err)
		return
	}
	title := core.DisplayTitle(p, manpage.HeaderTitle(text))
	d := docView{Title: title, Name: p.Name, SectionKey: p.Section, URL: pageURL(p.Name, p.Section)}
	s.render(w, http.StatusOK, "original", &view{Title: title + " (original)", Version: s.Version, Docs: []docView{d}, Original: text})
}

func (s *Server) convert(w http.ResponseWriter, r *http.Request) {
	// A cross-site form shouldn't be able to spend your LLM budget.
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	if s.CachedOnly {
		http.Error(w, "converting is switched off on this server", http.StatusForbidden)
		return
	}
	p, ok := s.resolve(w, r, "/convert")
	if !ok {
		return
	}
	refresh := r.FormValue("refresh") != ""
	s.jobMu.Lock()
	if j := s.jobs[p.Path]; j != nil && !j.finished.IsZero() {
		delete(s.jobs, p.Path)
	}
	s.jobMu.Unlock()
	s.startJob(p, refresh)
	http.Redirect(w, r, pageURL(p.Name, p.Section), http.StatusSeeOther)
}

func (s *Server) job(path string) *job {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	return s.jobs[path]
}

// startJob converts a page in the background, or returns the conversion
// already running for it.
func (s *Server) startJob(p manpage.Page, refresh bool) *job {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	for k, j := range s.jobs {
		if !j.finished.IsZero() && time.Since(j.finished) > 10*time.Minute {
			delete(s.jobs, k)
		}
	}
	if j := s.jobs[p.Path]; j != nil && j.finished.IsZero() {
		return j
	}
	j := &job{page: p, refresh: refresh, started: time.Now(), done: make(chan struct{})}
	s.jobs[p.Path] = j
	go func() {
		converting := false
		res, err := s.Girl.Get(s.ctx, p, core.GetOptions{
			Refresh: refresh,
			Started: func(llm.Backend) func() {
				converting = true
				fmt.Fprintf(s.Log, "💅 girlifying %s\n", p.Title())
				return func() {}
			},
			Progress: func(done, total int) {
				j.mu.Lock()
				j.part, j.parts = done, total
				j.mu.Unlock()
			},
		})
		s.jobMu.Lock()
		j.res, j.err, j.finished = res, err, time.Now()
		s.jobMu.Unlock()
		close(j.done)
		if err != nil {
			fmt.Fprintf(s.Log, "💔 %s: %v\n", p.Title(), err)
		} else if converting {
			fmt.Fprintf(s.Log, "💖 %s (%s)\n", p.Title(), time.Since(j.started).Round(time.Second))
		}
		s.forgetGirlified()
	}()
	return j
}

func (s *Server) render(w http.ResponseWriter, status int, name string, v *view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := execute(w, name, v); err != nil {
		fmt.Fprintf(s.Log, "girl: rendering %s: %v\n", name, err)
	}
}

func (s *Server) oops(w http.ResponseWriter, status int, o *oopsView) {
	s.render(w, status, "oops", &view{Title: o.Heading, Version: s.Version, Oops: o})
}

func (s *Server) notFound(w http.ResponseWriter, msg string) {
	s.oops(w, http.StatusNotFound, &oopsView{Emoji: "🙈", Heading: "not found", Message: msg})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.oops(w, http.StatusInternalServerError, &oopsView{Emoji: "💔", Heading: "something broke",
		Message: "sorry bestie, that didn't work.", Detail: err.Error()})
}
