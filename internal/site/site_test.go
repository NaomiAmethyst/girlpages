// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

type pageSpec struct {
	name, sec, desc string
	girl            bool
	added           int64
}

// build makes a repository from specs in out/repo and publishes it.
func build(t *testing.T, out string, specs []pageSpec, o Options) (*repo.Store, Stats) {
	t.Helper()
	s, err := repo.Open(filepath.Join(out, "repo"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range specs {
		title := strings.ToUpper(p.name) + "(" + p.sec + ")"
		text := title + "   Manual   " + title + "\n\nNAME\n       " + p.name + " - " + p.desc +
			"\n\nDESCRIPTION\n       <b>" + p.desc + "</b> & more\n\nfooter   " + title + "\n"
		src := repo.Hash([]byte(text))
		if s.HasVariant(src, repo.VariantMan) {
			continue
		}
		man := repo.ManVariant(src, text)
		mi, err := s.PutVariant(man)
		if err != nil {
			t.Fatal(err)
		}
		vs := map[string]repo.VariantInfo{repo.VariantMan: mi}
		if p.girl {
			md := "# ✨ NAME ✨\n\n`" + p.name + "` slays, see ls(1) and nope(1) 💅\n\n# OPTIONS\n\n- **`-v`** loud\n"
			gi, err := s.PutVariant(repo.GirlVariant(src, title, md, &repo.Generator{Model: "fake-1", Generated: time.Unix(1700000000, 0)}))
			if err != nil {
				t.Fatal(err)
			}
			vs[repo.VariantGirl] = gi
		}
		err = s.UpdateListing(p.name, p.sec, func(l *repo.Listing) error {
			l.Versions = append(l.Versions, repo.Version{Source: src, Title: title, Description: p.desc,
				Added: time.Unix(1700000000+p.added, 0).UTC(), Variants: vs,
				Packages: []repo.Package{{Distro: "debian", Release: "trixie", Name: p.name, Version: fmt.Sprint(p.added)}}})
			l.Current = src
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	pages, err := s.AllPages()
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Publish(pages)
	if err != nil {
		t.Fatal(err)
	}
	o.Out, o.Log = out, io.Discard
	st, err := Build(s, pages, m, o)
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

var specs = []pageSpec{
	{name: "ls", sec: "1", desc: "list directory contents", girl: true},
	{name: "ls", sec: "1", desc: "list directory contents, newer", girl: true, added: 10},
	{name: "git-commit", sec: "1", desc: "record changes to the repository"},
	{name: "[", sec: "1", desc: "check file types"},
	{name: "SSL_new", sec: "3ssl", desc: "create a new SSL structure", girl: true},
	{name: "printf", sec: "3", desc: "formatted output conversion", girl: true},
	{name: ".hidden", sec: "7", desc: "a page whose name starts with a dot"},
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuild(t *testing.T) {
	out := t.TempDir()
	_, st := build(t, out, specs, Options{Base: "/girlpages", URL: "https://example.test/girlpages/", Version: "test"})
	if st.Written == 0 {
		t.Fatal("nothing written")
	}
	home := read(t, filepath.Join(out, "index.html"))
	for _, want := range []string{`href="/girlpages/section/1/"`, `href="/girlpages/section/3/"`, "6 pages", "3 girlified",
		`href="/girlpages/static/style.css?v=test"`, `<link rel="canonical" href="https://example.test/girlpages/">`} {
		if !strings.Contains(home, want) {
			t.Errorf("home page lacks %q", want)
		}
	}
	ls := read(t, filepath.Join(out, "man", "1", "ls", "index.html"))
	for _, want := range []string{"<title>ls(1) — list directory contents, newer · girlpages 💅</title>",
		`<meta name="description" content="list directory contents, newer">`,
		`<code>ls</code> slays, see <a href="/girlpages/man/1/ls/">ls(1)</a> and nope(1)`, // only real pages are links
		`href="/girlpages/man/1/ls/original/"`, "girlified by fake-1", "debian trixie: ls 10",
		`href="/girlpages/man/1/ls/v/`, "other versions"} {
		if !strings.Contains(ls, want) {
			t.Errorf("ls page lacks %q", want)
		}
	}
	if strings.Contains(ls, "noindex") {
		t.Error("the current version should be indexable")
	}
	orig := read(t, filepath.Join(out, "man", "1", "ls", "original", "index.html"))
	if !strings.Contains(orig, "&lt;b&gt;list directory contents, newer&lt;/b&gt; &amp; more") || !strings.Contains(orig, "noindex") {
		t.Error("original page should be escaped and not indexed")
	}
	olds, _ := filepath.Glob(filepath.Join(out, "man", "1", "ls", "v", "*", "index.html"))
	if len(olds) != 1 {
		t.Fatalf("old versions: %v", olds)
	}
	old := read(t, olds[0])
	if !strings.Contains(old, "older version") || !strings.Contains(old, "noindex") ||
		!strings.Contains(old, `<link rel="canonical" href="https://example.test/girlpages/man/1/ls/">`) {
		t.Error("old version page should point at the current one")
	}

	// Not girlified: the page is the original, and there's no separate one.
	gc := read(t, filepath.Join(out, "man", "1", "git-commit", "index.html"))
	if !strings.Contains(gc, "hasn't gotten to this one yet") || !strings.Contains(gc, "record changes to the repository") {
		t.Error("ungirlified page should show the original")
	}
	if _, err := os.Stat(filepath.Join(out, "man", "1", "git-commit", "original")); err == nil {
		t.Error("ungirlified page shouldn't have a separate original")
	}

	// Odd names.
	sec1 := read(t, filepath.Join(out, "section", "1", "index.html"))
	if !strings.Contains(sec1, `href="/girlpages/man/1/%5B/"`) {
		t.Error("section 1 should link [ as %5B")
	}
	if _, err := os.Stat(filepath.Join(out, "man", "1", "[", "index.html")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(out, "man", "7", "%2Ehidden", "index.html")); err != nil {
		t.Error("dot names should be escaped on disk: ", err)
	}
	// Subsections get their own lists under the main section.
	sec3 := read(t, filepath.Join(out, "section", "3", "index.html"))
	if !strings.Contains(sec3, "SSL_new") || !strings.Contains(sec3, `href="/girlpages/section/3/3ssl/"`) {
		t.Error("section 3 should list 3ssl pages and link the subsection")
	}
	if !strings.Contains(read(t, filepath.Join(out, "section", "3", "3ssl", "index.html")), "SSL_new") {
		t.Error("no 3ssl list")
	}

	sitemap := read(t, filepath.Join(out, "sitemap.xml"))
	if !strings.Contains(sitemap, "<loc>https://example.test/girlpages/man/1/%5B/</loc>") || strings.Contains(sitemap, "/v/") {
		t.Errorf("sitemap:\n%s", sitemap)
	}
	if robots := read(t, filepath.Join(out, "robots.txt")); !strings.Contains(robots, "Disallow: /girlpages/repo/") {
		t.Errorf("robots.txt: %s", robots)
	}
	for _, f := range []string{"404.html", "search/index.html", "static/style.css", "static/search.js", ".nojekyll"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}

	// Rebuilding the same thing writes nothing.
	_, st = build(t, out, specs, Options{Base: "/girlpages", URL: "https://example.test/girlpages/", Version: "test"})
	if st.Written != 0 || st.Removed != 0 {
		t.Errorf("rebuild: %+v", st)
	}
}

func TestPrune(t *testing.T) {
	out := t.TempDir()
	s, _ := build(t, out, specs, Options{})
	if err := os.Remove(filepath.Join(s.Dir, repo.NamePath("git-commit", "1"))); err != nil {
		t.Fatal(err)
	}
	_, st := build(t, out, nil, Options{})
	if st.Removed != 1 {
		t.Errorf("removed %d pages", st.Removed)
	}
	if _, err := os.Stat(filepath.Join(out, "man", "1", "git-commit")); err == nil {
		t.Error("git-commit's page is still there")
	}
}

// TestSearchJS checks that the browser search agrees with the Go one.
func TestSearchJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out := t.TempDir()
	more := append([]pageSpec{}, specs...)
	for i := 0; i < 40; i++ {
		more = append(more, pageSpec{name: fmt.Sprintf("filler%02d", i), sec: "3", desc: "a filler page about lists"})
	}
	build(t, out, more, Options{})
	srv := httptest.NewServer(http.FileServer(http.Dir(out)))
	defer srv.Close()

	queries := []string{"ls", "list", "lsit", "directory contents", "git-commit", "commit", "[", "printf", "prntf",
		"ssl", "fill", "filler07", "zzzz", "hidden", "lists page"}
	sr := &repo.Searcher{Client: repo.NewClient(srv.URL+"/repo", 5*time.Second)}
	var want []string
	for _, q := range queries {
		hits, err := sr.Search(context.Background(), q, 10)
		if err != nil {
			t.Fatal(err)
		}
		var line []string
		for _, h := range hits {
			line = append(line, fmt.Sprintf("%s(%s)%s=%g", h.Name, h.Section, h.Description, h.Score))
		}
		want = append(want, q+": "+strings.Join(line, " "))
	}

	if !strings.HasPrefix(want[0], "ls: ls(1)") || !strings.Contains(want[2], "ls(1)") {
		t.Fatalf("Go search found the wrong things: %q", want[:3])
	}

	js, _ := filepath.Abs("static/search.js")
	qs, _ := json.Marshal(queries)
	script := fmt.Sprintf(`
const { Searcher } = await import(%q);
const s = new Searcher(%q);
for (const q of %s) {
  const hits = await s.search(q, 10);
  console.log(q + ": " + hits.map(h => h.name + "(" + h.section + ")" + h.description + "=" + h.score).join(" "));
}`, "file://"+js, srv.URL+"/repo/", qs)
	cmd := exec.Command(node, "--input-type=module", "-e", script)
	got, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, got)
	}
	gotLines := strings.Split(strings.TrimSpace(string(got)), "\n")
	for i := range want {
		if i >= len(gotLines) || gotLines[i] != want[i] {
			g := ""
			if i < len(gotLines) {
				g = gotLines[i]
			}
			t.Errorf("JS and Go disagree:\n  go: %s\n  js: %s", want[i], g)
		}
	}
}
