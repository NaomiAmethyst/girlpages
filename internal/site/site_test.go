// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/precompress"
	"github.com/NaomiAmethyst/girlpages/internal/render"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

type pageSpec struct {
	name, sec, desc string
	girl            bool
	added           int64
	// pkgs are where it's from; by default debian trixie.
	pkgs []repo.Package
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
				Packages: p.packages()})
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

func (p pageSpec) packages() []repo.Package {
	if p.pkgs != nil {
		return p.pkgs
	}
	return []repo.Package{{Distro: "debian", Release: "trixie", Name: p.name, Version: fmt.Sprint(p.added)}}
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
		`href="/girlpages/static/style.css?v=test"`, `<link rel="canonical" href="https://example.test/girlpages/">`,
		`<meta property="og:type" content="website">`, `"@type":"WebSite"`,
		`"urlTemplate":"https://example.test/girlpages/search/?q={search_term_string}"`,
		`href="/girlpages/#/debian/trixie"`, `<div id="app" data-repo="/girlpages/repo/"`} {
		if !strings.Contains(home, want) {
			t.Errorf("home page lacks %q", want)
		}
	}
	ls := read(t, filepath.Join(out, "girl", "1", "ls", "index.html"))
	for _, want := range []string{"<title>ls(1) — list directory contents, newer · girlpages 💅</title>",
		`<link rel="canonical" href="https://example.test/girlpages/girl/1/ls/">`,
		`<meta property="og:type" content="article">`, `<meta property="og:url" content="https://example.test/girlpages/girl/1/ls/">`,
		`<meta property="og:image" content="https://example.test/girlpages/static/icon-512.png">`,
		`<meta name="twitter:card" content="summary">`, `<meta property="article:modified_time"`,
		`<code>ls</code> slays, see <a href="/girlpages/girl/1/ls/">ls(1)</a> and nope(1)`, // only real pages are links
		`href="/girlpages/man/1/ls/" rel="nofollow" data-variant="man"`, "girlified by fake-1",
		`<details class="versions" id="versions"><summary>version 10 <span class="n">· 2 versions</span></summary>`,
		`<strong>10 · debian trixie (latest)</strong>`, `<a href="/girlpages/#/1/ls/0">0 · debian trixie</a>`, "</details>, in",
		`href="/girlpages/#/debian/trixie/1/ls"`, `data-keep-page="ls" data-keep-section="1"`} {
		if !strings.Contains(ls, want) {
			t.Errorf("ls page lacks %q", want)
		}
	}
	shell := read(t, filepath.Join(out, "static", "shell.html"))
	for _, want := range []string{"data-shell", `name="robots" content="noindex`, `id="app"`} {
		if !strings.Contains(shell, want) {
			t.Errorf("the shell lacks %q", want)
		}
	}
	if strings.Contains(ls, "noindex") {
		t.Error("the latest version should be indexable")
	}
	// Structured data parses and says the right things.
	m := regexp.MustCompile(`<script type="application/ld\+json">(.*?)</script>`).FindStringSubmatch(ls)
	if m == nil {
		t.Fatal("no JSON-LD")
	}
	var ldata struct {
		Graph []map[string]any `json:"@graph"`
	}
	if err := json.Unmarshal([]byte(m[1]), &ldata); err != nil || len(ldata.Graph) != 2 {
		t.Fatalf("JSON-LD: %v %s", err, m[1])
	}
	art := ldata.Graph[0]
	if art["@type"] != "TechArticle" || art["url"] != "https://example.test/girlpages/girl/1/ls/" ||
		art["about"].(map[string]any)["softwareVersion"] != "10" || ldata.Graph[1]["@type"] != "BreadcrumbList" {
		t.Errorf("JSON-LD: %s", m[1])
	}

	// Only the latest version is static: no originals, versions or sources.
	for _, gone := range []string{"man/1/ls/original", "man/1/ls/v", "src"} {
		if _, err := os.Stat(filepath.Join(out, gone)); err == nil {
			t.Errorf("%s shouldn't be there", gone)
		}
	}

	// Not girlified: the page is the original.
	gc := read(t, filepath.Join(out, "girl", "1", "git-commit", "index.html"))
	if !strings.Contains(gc, "hasn't gotten to this one yet") || !strings.Contains(gc, "&lt;b&gt;record changes to the repository&lt;/b&gt; &amp; more") {
		t.Error("ungirlified page should show the original, escaped")
	}
	if strings.Contains(gc, "variant=man") {
		t.Error("ungirlified page shouldn't link to a separate original")
	}

	// Odd names.
	sec1 := read(t, filepath.Join(out, "section", "1", "index.html"))
	if !strings.Contains(sec1, `href="/girlpages/girl/1/%5B/"`) || !strings.Contains(sec1, `"@type":"CollectionPage"`) {
		t.Error("section 1 should link [ as %5B, and say it's a collection")
	}
	if _, err := os.Stat(filepath.Join(out, "girl", "1", "[", "index.html")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(out, "girl", "7", "%2Ehidden", "index.html")); err != nil {
		t.Error("dot names should be escaped on disk: ", err)
	}
	sec3 := read(t, filepath.Join(out, "section", "3", "index.html"))
	if !strings.Contains(sec3, "SSL_new") || !strings.Contains(sec3, `href="/girlpages/section/3/3ssl/"`) {
		t.Errorf("section 3 should list 3ssl pages and link the subsection:\n%s", sec3)
	}

	sitemap := read(t, filepath.Join(out, "sitemap.xml"))
	if !strings.Contains(sitemap, "<loc>https://example.test/girlpages/girl/1/%5B/</loc>") || strings.Contains(sitemap, "browse") ||
		!strings.Contains(sitemap, "<lastmod>") {
		t.Errorf("sitemap:\n%s", sitemap)
	}
	robots := read(t, filepath.Join(out, "robots.txt"))
	if !strings.Contains(robots, "Disallow: /girlpages/repo/") || !strings.Contains(robots, "Disallow: /girlpages/browse/") {
		t.Errorf("robots.txt: %s", robots)
	}
	for _, f := range []string{"404.html", "search/index.html", "browse/index.html", "static/app.js", "static/render.js", ".nojekyll"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}
	if b := read(t, filepath.Join(out, "browse", "index.html")); !strings.Contains(b, "noindex") || !strings.Contains(b, `id="app"`) {
		t.Error("the browse app shouldn't be indexed")
	}
	// Every page can become the app in place, and prerenders the static
	// pages it links to (but not routes, or the repository).
	if ls := read(t, filepath.Join(out, "girl", "1", "ls", "index.html")); !strings.Contains(ls, `<div class="static-view">`) ||
		!strings.Contains(ls, `<div id="app" data-repo="/girlpages/repo/" data-base="/girlpages/" hidden>`) ||
		!strings.Contains(ls, `<script type="speculationrules">`) || !strings.Contains(ls, `repo/*`) {
		t.Error("ls page should be able to host the app, and prerender links")
	}

	// Rebuilding the same thing writes nothing.
	_, st = build(t, out, specs, Options{Base: "/girlpages", URL: "https://example.test/girlpages/", Version: "test"})
	if st.Written != 0 || st.Removed != 0 {
		t.Errorf("rebuild: %+v", st)
	}
}

func TestNoURL(t *testing.T) {
	out := t.TempDir()
	build(t, out, specs, Options{})
	ls := read(t, filepath.Join(out, "girl", "1", "ls", "index.html"))
	if strings.Contains(ls, "og:url") || strings.Contains(ls, "canonical") || strings.Contains(ls, `"url"`) {
		t.Error("without a site URL there should be no absolute URLs")
	}
	if _, err := os.Stat(filepath.Join(out, "sitemap.xml")); err == nil {
		t.Error("no sitemap without a URL")
	}
}

func TestPrune(t *testing.T) {
	out := t.TempDir()
	s, _ := build(t, out, specs, Options{})
	// Leftovers from an older girlrepo.
	for _, old := range []string{"man/1/ls/original/index.html", "man/1/ls/v/abc/index.html", "src/debian/index.html"} {
		os.MkdirAll(filepath.Dir(filepath.Join(out, old)), 0o755)
		os.WriteFile(filepath.Join(out, old), []byte("old"), 0o644)
	}
	if err := os.Remove(filepath.Join(s.Dir, repo.NamePath("git-commit", "1"))); err != nil {
		t.Fatal(err)
	}
	_, st := build(t, out, nil, Options{})
	if st.Removed != 4 {
		t.Errorf("removed %d pages", st.Removed)
	}
	for _, gone := range []string{"man/1/git-commit", "man/1/ls/v", "src"} {
		if _, err := os.Stat(filepath.Join(out, gone)); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
}

// node runs a module script, skipping the test if there's no node.
func node(t *testing.T, script string) []byte {
	t.Helper()
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(bin, "--input-type=module", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	return out
}

func staticFile(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("static", name))
	if err != nil {
		t.Fatal(err)
	}
	return "file://" + p
}

// TestSearchJS checks that the browser search agrees with the Go one,
// within sources too.
func TestSearchJS(t *testing.T) {
	out := t.TempDir()
	more := append([]pageSpec{}, specs...)
	arch := []repo.Package{{Distro: "arch", Release: "rolling", Name: "x", Version: "1"}}
	for i := 0; i < 40; i++ {
		ps := []pageSpec{{name: fmt.Sprintf("filler%02d", i), sec: "3", desc: "a filler page about lists"}}
		if i%3 == 0 {
			ps[0].pkgs = arch
		}
		more = append(more, ps...)
	}
	build(t, out, more, Options{})
	srv := httptest.NewServer(http.FileServer(http.Dir(out)))
	defer srv.Close()

	type query struct {
		Q    string `json:"q"`
		Src  string `json:"src"`
		Mode string `json:"mode"`
	}
	queries := []query{{"ls", "", ""}, {"list", "", ""}, {"lsit", "", ""}, {"directory contents", "", ""}, {"git-commit", "", ""}, {"commit", "", ""},
		{"[", "", ""}, {"printf", "", ""}, {"prntf", "", ""}, {"ssl", "", ""}, {"fill", "", ""}, {"filler07", "", ""}, {"zzzz", "", ""}, {"hidden", "", ""},
		{"lists page", "", ""}, {"filler", "arch/rolling", ""}, {"filler", "arch", ""}, {"filler", "debian/trixie", ""}, {"ls", "arch", ""},
		// Parts of words.
		{"ill", "", "ngrams"}, {"ommit", "", "ngrams"}, {"rintf", "", "ngrams"}, {"list", "", "ngrams"}, {"ls", "", "ngrams"},
		{"ectory cont", "", "ngrams"}, {"ill", "arch", "ngrams"}, {"ectory", "", ""},
		// Phrases and packages.
		{`"directory contents"`, "", ""}, {`"contents directory"`, "", ""}, {`"record changes" commit`, "", ""},
		{`filler03 "filler page about"`, "arch", ""}, {`"page about lists" pkg:x`, "", ""}, {`"the and"`, "", ""},
		{"pkg:x", "", ""}, {"pkg:arch/x", "", ""}, {"pkg:debian/printf", "", ""}, {"pkg:x filler06", "", ""}, {"pkg:x filler07", "", ""},
		{`pkg:ls "list directory"`, "", "ngrams"}}
	sr := &repo.Searcher{Client: repo.NewClient(srv.URL+"/repo", 5*time.Second)}
	var want []string
	for _, q := range queries {
		sr.Mode = q.Mode
		hits, err := sr.SearchIn(context.Background(), q.Q, q.Src, 10)
		if err != nil {
			t.Fatal(err)
		}
		var line []string
		for _, h := range hits {
			line = append(line, fmt.Sprintf("%s(%s)%s=%g[%s]", h.Name, h.Section, h.Description, h.Score, strings.Join(h.Sources, " ")))
		}
		want = append(want, q.Q+"@"+q.Src+"/"+q.Mode+": "+strings.Join(line, " "))
	}
	if !strings.HasPrefix(want[0], "ls@/: ls(1)") || !strings.Contains(want[2], "ls(1)") ||
		!strings.Contains(want[15], "filler00(3)") || strings.Contains(want[15], "filler01(3)") || want[18] != "ls@arch/: " {
		t.Fatalf("Go search found the wrong things: %q", want)
	}
	// Parts of words are found by ngrams, and not otherwise.
	if !strings.Contains(want[19], "filler") || !strings.Contains(want[20], "git-commit(1)") || !strings.Contains(want[21], "printf") ||
		want[26] != "ectory@/: " {
		t.Fatalf("ngrams found the wrong things: %q", want[19:])
	}

	// Phrases are looked for in pages' text; packages keep to theirs.
	for i, w := range []string{
		`"directory contents"@/: ls(1)`, `"contents directory"@/: `, `"record changes" commit@/: git-commit(1)`,
		`filler03 "filler page about"@arch/: filler03(3)`, `"page about lists" pkg:x@/: filler00(3)`, `"the and"@/: `,
		`pkg:x@/: filler00(3)`, `pkg:arch/x@/: filler00(3)`, `pkg:debian/printf@/: printf(3)`, `pkg:x filler06@/: filler06(3)`, `pkg:x filler07@/: `,
		`pkg:ls "list directory"@/ngrams: ls(1)`,
	} {
		if g := want[27+i]; !strings.HasPrefix(g, w) {
			t.Errorf("Go search: %s, want it to start %s", g, w)
		}
	}

	qs, _ := json.Marshal(queries)
	got := node(t, fmt.Sprintf(`
const { Searcher } = await import(%q);
const s = new Searcher(%q);
for (const q of %s) {
  const hits = await s.search(q.q, 10, q.src, null, q.mode || "words");
  console.log(q.q + "@" + q.src + "/" + q.mode + ": " + hits.map(h => h.name + "(" + h.section + ")" + h.description + "=" + h.score + "[" + h.sources.join(" ") + "]").join(" "));
}`, staticFile(t, "search.js"), srv.URL+"/repo/", qs))
	gotLines := strings.Split(strings.TrimSpace(string(got)), "\n")
	for i := range want {
		if i >= len(gotLines) || strings.TrimRight(gotLines[i], " ") != strings.TrimRight(want[i], " ") {
			g := ""
			if i < len(gotLines) {
				g = gotLines[i]
			}
			t.Errorf("JS and Go disagree:\n  go: %s\n  js: %s", want[i], g)
		}
	}
}

// TestRenderJS checks the browser renders markup exactly as Go does.
func TestRenderJS(t *testing.T) {
	cases := []string{
		"# ✨ NAME ✨\n\n`ls` - list stuff\nand more\n\n- **`-a`** — all\n  continued\n  - nested\n1. first\n2) second\n\n```\ncode  here\n  indented\n```\n\n> careful\n> bestie\n\n---\n",
		"<pink>a <mint>b</mint> c</pink> and <rainbow>rainbow</rainbow> <file> <b> *em* **bold** ***both*** `<pink>`",
		"see ls(1), printf(3), git-commit(1), x(9z) and https://example.org/a_(b). or <https://x.y/z>, \\*not em\\*",
		"7z(1) and 7za(1), 2048(1), 0desktop(1), foo-7z(1), 10(1) and v1.2(3)\n",
		"| a | b |\n|---|:-:|\n| `x` | **y** |\n\n# Same\n\n# Same\n\n## Sub heading ##\n\n### deeper\n",
		"   ```bash\n   echo \"hi\" & <bye>\n\tindented\n   ```\n\n* star list\n+ plus list\n• bullet\n10. ten\n",
		"files *.c and 2*3 lone ** stars a*b*c _under_ x_y_z émoji 💅 *italic é* 'quotes' \"dq\" <3 & ok",
		"",
		"no heading at all\n\n\n\nlots of blanks\n   \n",
		"- a\n\n- b\n    - c\n        - d\n- e\n\n1. x\n- y\n",
		"keywords are in RFC 2119 and [RFC8174]; RFC0791 is IP, RFC 99999 isn't, and NOTRFC 1 neither\n",
		"<bold>big</bold>, <italic>soft</italic>, <kbd>Ctrl</kbd>+<KBD>C</KBD>, <pink><bold>both</bold></pink>, the <kbd> element, stray </italic>\n",
	}
	link := func(name, sec string) string {
		if name == "nope" {
			return ""
		}
		return "/man/" + sec + "/" + name + "/"
	}
	type result struct {
		HTML string
		TOC  []render.Heading
	}
	var want []result
	for _, c := range cases {
		h, toc := render.HTML(c, render.HTMLOptions{Link: link})
		want = append(want, result{h, toc})
	}
	cs, _ := json.Marshal(cases)
	got := node(t, fmt.Sprintf(`
const { renderHTML } = await import(%q);
const out = [];
for (const c of %s) {
  const r = renderHTML(c, { link: (n, s) => n === "nope" ? "" : "/man/" + s + "/" + n + "/" });
  out.push({ HTML: r.html, TOC: r.toc.map(h => ({ ID: h.id, Text: h.text, Level: h.level })) });
}
console.log(JSON.stringify(out));`, staticFile(t, "render.js"), cs))
	var js []result
	if err := json.Unmarshal(got, &js); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	for i := range want {
		if want[i].TOC == nil {
			want[i].TOC = []render.Heading{}
		}
		if js[i].TOC == nil {
			js[i].TOC = []render.Heading{}
		}
		if js[i].HTML != want[i].HTML || fmt.Sprint(js[i].TOC) != fmt.Sprint(want[i].TOC) {
			t.Errorf("case %d differs:\n  go: %q %v\n  js: %q %v", i, want[i].HTML, want[i].TOC, js[i].HTML, js[i].TOC)
		}
	}
}

func TestPWAFiles(t *testing.T) {
	out := t.TempDir()
	build(t, out, specs, Options{Base: "/girlpages/", Version: "test"})
	var m struct {
		StartURL string `json:"start_url"`
		Scope    string
		Icons    []struct{ Src, Sizes, Purpose string }
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(out, "manifest.webmanifest"))), &m); err != nil {
		t.Fatal(err)
	}
	if m.StartURL != "/girlpages/" || m.Scope != "/girlpages/" || len(m.Icons) != 4 {
		t.Errorf("web app manifest: %+v", m)
	}
	for _, ic := range m.Icons {
		f, err := os.Open(filepath.Join(out, ic.Src))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(ic.Src, ".png") {
			cfg, err := png.DecodeConfig(f)
			if err != nil || fmt.Sprintf("%dx%d", cfg.Width, cfg.Height) != ic.Sizes {
				t.Errorf("%s: %+v, %v", ic.Src, cfg, err)
			}
		}
		f.Close()
	}
	sw := read(t, filepath.Join(out, "sw.js"))
	if strings.Contains(sw, `/*BASE*/`) || !strings.Contains(sw, `const BASE = "/girlpages/";`) ||
		!strings.Contains(sw, `"static/app.js"`) || !strings.Contains(sw, `"browse/"`) || !strings.Contains(sw, `"offline/"`) {
		t.Errorf("service worker constants not filled in:\n%s", sw[:600])
	}
	home := read(t, filepath.Join(out, "index.html"))
	for _, want := range []string{`<link rel="manifest" href="/girlpages/manifest.webmanifest">`,
		`data-repo="/girlpages/repo/"`, `<script type="module" src="/girlpages/static/girlpages.js">`} {
		if !strings.Contains(home, want) {
			t.Errorf("home page lacks %q", want)
		}
	}
	if !strings.Contains(read(t, filepath.Join(out, "offline", "index.html")), "offline") {
		t.Error("no offline page")
	}
}

// TestRepoCacheJS runs the browser's repository cache in node against a
// repository that can be republished or taken down.
func TestRepoCacheJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	v1, v2 := t.TempDir(), t.TempDir()
	build(t, v1, specs, Options{})
	more := append([]pageSpec{}, specs...)
	for i := 0; i < 30; i++ {
		more = append(more, pageSpec{name: fmt.Sprintf("new%02d", i), sec: "8", desc: "a page added later"})
	}
	build(t, v2, more, Options{})
	var current atomic.Pointer[string]
	current.Store(&v1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/switch":
			current.Store(&v2)
			return
		}
		http.FileServer(http.Dir(*current.Load())).ServeHTTP(w, r)
	}))
	defer srv.Close()

	gp, _ := filepath.Abs("static/cache.js")
	sj, _ := filepath.Abs("static/search.js")
	fm, _ := filepath.Abs("static/format.js")
	script := fmt.Sprintf(`
const { RepoCache, MemoryStore } = await import(%q);
const { Searcher } = await import(%q);
const { escapeName } = await import(%q);
class Local {
  constructor() { this.m = new Map(); }
  getItem(k) { return this.m.has(k) ? this.m.get(k) : null; }
  setItem(k, v) { this.m.set(k, String(v)); }
}
const repo = %q;
let fetched = [], down = false;
const counting = async (url, init) => {
  if (down) throw new TypeError("fetch failed");
  fetched.push(url.slice(repo.length));
  const r = await fetch(url, init);
  if (!r.ok) { const e = new Error(url + ": " + r.status); e.status = r.status; throw e; }
  return r.text();
};
const out = {};
const store = new MemoryStore(), storage = new Local();
const cache = (s = store) => new RepoCache(repo, { store: s, storage, fetch: counting });
const names = async (c, q) => (await new Searcher(repo, c).search(q, 5)).map(h => h.name + "(" + h.section + ")");

out.first = await names(cache(), "list");
out.firstFetches = fetched.length;
fetched = [];
const again = cache();
out.again = await names(again, "list");
await again.settled(); // the manifest is refreshed after
out.againFetches = [...fetched];
down = true;
out.offline = await names(cache(), "list");
try {
  const zs = new Searcher(repo, cache());
  await zs.search("zebra", 5);
  out.offlineNew = zs.missedTypos ? "missed typos" : "worked?";
}
catch (e) { out.offlineNew = e.constructor.name; }
down = false;

// Save section 1, then read one of its pages offline, as the browse app
// does.
const saver = cache();
let progress = [];
out.saved = await saver.save("1", { progress: (d, n) => progress.push(d + "/" + n) });
out.progress = progress[progress.length - 1];
out.savedState = saver.saved("1");
down = true;
const c1 = cache();
const ptr = await c1.get("page/name-1/" + escapeName("ls") + ".json");
const l = await c1.get("page/page-" + ptr.sha256.slice(0, 2) + "/" + ptr.sha256 + ".json");
const ver = l.versions.find(v => v.source === l.current);
const body = await c1.get("contents/" + l.current.slice(0, 2) + "/" + l.current + "/girl-" + ver.variants.girl.sha256 + ".json");
out.offlinePage = body.variant + " " + body.title;
// Its chapter's index too.
const ce = (await c1.get("manifest.json")).chapters.find(c => c.chapter === "1");
const ci = await c1.chapterIndex(ce);
out.offlineListing = [String(ci.count), ...(await c1.chapterChunk(ci, 1)).map(p => p.name + (p.variants.includes("girl") ? "✨" : ""))];
down = false;

// A page read is kept for offline.
const keeper = cache();
await keeper.keepPage("git-commit", "1");
down = true;
const kept = cache();
const kp = await kept.get("page/name-1/git-commit.json");
out.keptPage = (await kept.get("page/page-" + kp.sha256.slice(0, 2) + "/" + kp.sha256 + ".json")).name;
down = false;

// Search saved in parts: words, then typo matching.
{
  const sc = cache(new MemoryStore());
  out.wordsSize = await sc.searchSize("words");
  await sc.saveSearch("words");
  down = true;
  const s1 = new Searcher(repo, sc);
  out.offlineWords = (await s1.search("list", 5)).map(h => h.name);
  out.offlineTypo = [(await s1.search("lsit", 5)).length, s1.missedTypos];
  down = false;
  await sc.saveSearch("typos");
  down = true;
  const s2 = new Searcher(repo, sc);
  out.offlineTypoSaved = [(await s2.search("lsit", 5)).map(h => h.name), s2.missedTypos];
  // Only pages that are saved: ls is (it's in section 1), printf isn't.
  for (const [k, v] of store.m) if (!sc.store.m.has(k)) sc.store.m.set(k, v);
  out.hasPage = [await sc.hasPage("ls", "1"), await sc.hasPage("printf", "3")];
  out.savedOnly = (await s2.search("printf ls", 5, "", (h) => sc.hasPage(h.name, h.section))).map(h => h.name);
  down = false;
}

// Republish; a check notices and collects garbage.
const oldManifest = store.m.get("manifest.json");
await fetch(repo.replace(/repo\/$/, "switch"));
const c = cache();
let collected;
c.onNewManifest = (m) => { collected = c.gc(m); };
await c.check(true);
out.removed = collected ? await collected : -1;
out.kept = [...store.m.keys()];
c.onNewManifest = () => { out.secondGC = true; };
await c.check(true);
out.after = await names(c, "added later");

// Files that change are first given from the store, then refreshed;
// revalidate makes sure of them.
store.m.set("manifest.json", oldManifest);
const swr = cache();
const before = (await swr.get("manifest.json")).pages;
await swr.settled();
out.swr = [before, (await swr.get("manifest.json")).pages];
store.m.set("manifest.json", oldManifest);
const rv = cache();
await rv.get("manifest.json");
out.revalidated = [await rv.revalidate(), (await rv.get("manifest.json")).pages];
out.savedAfter = (await c.get("manifest.json")).generated !== out.savedState.generated;

// A small store: everything still works, and it stays within it.
const small = new MemoryStore(40000);
out.small = await names(cache(small), "list directory");
out.smallSize = small.size();
console.log(JSON.stringify(out));
`, "file://"+gp, "file://"+sj, "file://"+fm, srv.URL+"/repo/")
	cmd := exec.Command(node, "--input-type=module", "-e", script)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, b)
	}
	var got struct {
		First, Again, Offline, After, Small []string
		FirstFetches                        int
		AgainFetches                        []string
		KeptPage                            string
		Swr, Revalidated                    []any
		WordsSize                           int
		OfflineWords, SavedOnly             []string
		OfflineTypo, OfflineTypoSaved       []any
		HasPage                             []bool
		OfflineNew                          string
		Saved                               struct{ Pages, Failed int }
		Progress                            string
		SavedState                          struct{ Pages int }
		OfflinePage                         string
		OfflineListing                      []string
		Removed                             int
		Kept                                []string
		SecondGC                            bool
		SmallSize                           int
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if len(got.First) == 0 || !reflect.DeepEqual(got.First, got.Again) || !reflect.DeepEqual(got.First, got.Offline) {
		t.Errorf("results differ: %v / %v / %v", got.First, got.Again, got.Offline)
	}
	if got.FirstFetches < 5 || !reflect.DeepEqual(got.AgainFetches, []string{"manifest.json"}) {
		t.Errorf("fetched %d files at first, then %v (want only the manifest)", got.FirstFetches, got.AgainFetches)
	}
	if got.WordsSize < 1000 || len(got.OfflineWords) == 0 || fmt.Sprint(got.OfflineTypo) != "[0 true]" ||
		!strings.Contains(fmt.Sprint(got.OfflineTypoSaved), "ls") || !strings.HasSuffix(fmt.Sprint(got.OfflineTypoSaved), "false]") {
		t.Errorf("search saved in parts: %d bytes; offline %v; a typo without typo matching %v, with %v",
			got.WordsSize, got.OfflineWords, got.OfflineTypo, got.OfflineTypoSaved)
	}
	if fmt.Sprint(got.HasPage) != "[true false]" || fmt.Sprint(got.SavedOnly) != "[ls]" {
		t.Errorf("saved pages: %v; searching only them: %v", got.HasPage, got.SavedOnly)
	}
	if got.KeptPage != "git-commit" {
		t.Errorf("a page kept for offline: %q", got.KeptPage)
	}
	m1, _ := repo.NewClient(filepath.Join(v1, "repo"), time.Second).Manifest(context.Background())
	m2c, _ := repo.NewClient(filepath.Join(v2, "repo"), time.Second).Manifest(context.Background())
	if fmt.Sprint(got.Swr) != fmt.Sprintf("[%d %d]", m1.Pages, m2c.Pages) || fmt.Sprint(got.Revalidated) != fmt.Sprintf("[true %d]", m2c.Pages) {
		t.Errorf("stale manifest, then refreshed: %v; revalidated: %v", got.Swr, got.Revalidated)
	}
	// Offline, a search needing files that aren't saved fails, or (if only
	// typo matching's are missing) goes without them.
	if got.OfflineNew != "TypeError" && got.OfflineNew != "missed typos" {
		t.Errorf("an uncached search offline gave %q", got.OfflineNew)
	}
	s1, _ := repo.Open(filepath.Join(v1, "repo"))
	pages, _ := s1.AllPages()
	n1 := 0
	for _, p := range pages {
		if p.Pointer.Section[0] == '1' {
			n1++
		}
	}
	if got.Saved.Pages != n1 || got.Saved.Failed != 0 || got.Progress != fmt.Sprintf("%d/%d", n1, n1) || got.SavedState.Pages != n1 {
		t.Errorf("saving section 1 (%d pages): %+v, progress %s, state %+v", n1, got.Saved, got.Progress, got.SavedState)
	}
	if strings.Join(got.OfflineListing, " ") != "3 [ git-commit ls✨" {
		t.Errorf("a saved listing offline: %v", got.OfflineListing)
	}
	if got.OfflinePage != "girl LS(1)" {
		t.Errorf("a saved page offline: %q", got.OfflinePage)
	}
	if got.Removed < 1 || got.SecondGC {
		t.Errorf("GC removed %d entries; ran again for the same manifest: %v", got.Removed, got.SecondGC)
	}
	// The search index and section indexes kept are the new manifest's;
	// pages saved stay.
	s2, _ := repo.Open(filepath.Join(v2, "repo"))
	m2, _ := s2.Manifest()
	live := map[string]bool{}
	for _, e := range m2.Chapters {
		live[repo.SectionIndexPath(e.Chapter, e.SHA256)] = true
		var ci repo.ChapterIndex
		s2.Read(repo.SectionIndexPath(e.Chapter, e.SHA256), &ci)
		for _, r := range ci.Shards {
			live[repo.SectionShardPath(r.SHA256)] = true
		}
	}
	for _, kind := range []string{"words", "grams", "names", "docs"} {
		sha := map[string]string{"words": m2.Index.Words, "grams": m2.Index.Grams, "names": m2.Index.Names, "docs": m2.Index.Docs}[kind]
		live[repo.IndexManifestPath(kind, sha)] = true
		if kind == "names" || kind == "docs" {
			var tm repo.TableManifest
			s2.Read(repo.IndexManifestPath(kind, sha), &tm)
			for _, r := range tm.Shards {
				live[repo.ShardPath(kind, r.SHA256)] = true
			}
			continue
		}
		var sm repo.ShardManifest
		s2.Read(repo.IndexManifestPath(kind, sha), &sm)
		for _, r := range sm.Shards {
			live[repo.ShardPath(kind, r.SHA256)] = true
		}
	}
	contents := 0
	for _, k := range got.Kept {
		if (strings.HasPrefix(k, "index/") || strings.HasPrefix(k, "section/")) && !live[k] {
			t.Errorf("GC kept %s, which the new manifest doesn't use", k)
		}
		if strings.HasPrefix(k, "contents/") {
			contents++
		}
	}
	if contents < n1 {
		t.Errorf("GC removed saved pages: %d variants left", contents)
	}
	if len(got.After) == 0 || !strings.HasPrefix(got.After[0], "new") {
		t.Errorf("search after republishing: %v", got.After)
	}
	if len(got.Small) == 0 || got.SmallSize > 40000 {
		t.Errorf("small store: %v, %d bytes used", got.Small, got.SmallSize)
	}
}

func TestSources(t *testing.T) {
	out := t.TempDir()
	trixie := repo.Package{Distro: "debian", Release: "trixie", Name: "coreutils", Version: "9.7-3"}
	noble := repo.Package{Distro: "ubuntu", Release: "noble", Name: "coreutils", Version: "9.4-2"}
	archp := repo.Package{Distro: "arch", Release: "rolling", Name: "grep", Version: "3.12-1"}
	git := repo.Package{Name: "tool", Version: "v1 (abc)", URL: "https://example.org/tool.git"}
	s, _ := build(t, out, []pageSpec{
		{name: "ls", sec: "1", desc: "list directory contents", girl: true, pkgs: []repo.Package{trixie}},
		{name: "ls", sec: "1", desc: "list directory contents, older", added: 10, pkgs: []repo.Package{noble}},
		{name: "grep", sec: "1", desc: "print lines", girl: true, pkgs: []repo.Package{archp}},
		{name: "tool", sec: "8", desc: "does tool things", pkgs: []repo.Package{git}},
	}, Options{})
	m, _ := s.Manifest()
	var labels []string
	for _, e := range m.Sources {
		labels = append(labels, fmt.Sprintf("%s=%d%v", e.Key(), e.Pages, e.Sections))
	}
	if got := strings.Join(labels, " "); got != "arch=1map[1:1] arch/rolling=1map[1:1] debian=1map[1:1] debian/trixie=1map[1:1] "+
		"ubuntu=1map[1:1] ubuntu/noble=1map[1:1] upstream=1map[8:1]" {
		t.Errorf("manifest sources: %s", got)
	}
	// 9.7 beats 9.4, though ubuntu's was added later.
	l, _, _ := s.Listing("ls", "1")
	if l.CurrentVersion().Description != "list directory contents" || l.Sources["ubuntu/noble"] == l.Current ||
		l.Sources["debian/trixie"] != l.Current || l.Versions[0].Version == "" {
		t.Errorf("ls listing: %+v", l)
	}
	// The chapter index says which sources have a page, and which of
	// their versions are girly.
	var si repo.ChapterIndex
	s.Read(repo.SectionIndexPath("1", m.Chapters[0].SHA256), &si)
	for _, p := range si.Pages {
		if p.Name != "ls" {
			continue
		}
		var girl []string
		for _, k := range p.Sources {
			if v := l.Find(l.Sources[k]); v != nil && v.Variants[repo.VariantGirl].SHA256 != "" {
				girl = append(girl, k)
			}
		}
		if strings.Join(p.Sources, " ") != "debian debian/trixie ubuntu ubuntu/noble" || fmt.Sprint(p.Girl) != fmt.Sprint(girl) ||
			p.Section != "1" || p.Versions != 2 || p.Version != "9.7" {
			t.Errorf("ls in chapter 1: %+v (girly in %v)", p, girl)
		}
	}
	// Static pages link into the browse app for sources.
	page := read(t, filepath.Join(out, "girl", "1", "ls", "index.html"))
	if !strings.Contains(page, "<summary>version 9.7 <span") || !strings.Contains(page, `href="/#/debian/trixie/1/ls"`) {
		t.Error("ls page should say where it's from")
	}
	if tp := read(t, filepath.Join(out, "girl", "8", "tool", "index.html")); !strings.Contains(tp, `href="/#/upstream/8/tool">upstream</a>`) {
		t.Error("git page should say it's upstream")
	}
	sec1 := read(t, filepath.Join(out, "section", "1", "index.html"))
	if !strings.Contains(sec1, `href="/#/arch/rolling/section/1"`) || strings.Contains(sec1, "#/upstream") {
		t.Error("section 1 should offer the sources that have pages in it, and only those")
	}
}

func TestHandler(t *testing.T) {
	out := t.TempDir()
	build(t, out, specs, Options{})
	if _, err := precompress.Sweep(out, precompress.Encodings); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(out))
	defer srv.Close()
	get := func(path, enc string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if enc != "" {
			req.Header.Set("Accept-Encoding", enc)
		}
		resp, err := http.DefaultTransport.RoundTrip(req) // no automatic gzip
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	// The smallest copy the client accepts is sent.
	size := func(ext string) int64 {
		fi, err := os.Stat(filepath.Join(out, "girl", "1", "ls", "index.html"+ext))
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}
	smallest := func(encs ...string) string {
		ext := map[string]string{"zstd": ".zst", "br": ".br", "gzip": ".gz"}
		best := ""
		for _, e := range encs {
			if best == "" || size(ext[e]) < size(ext[best]) {
				best = e
			}
		}
		return best
	}
	for _, c := range []struct{ accept, want string }{
		{"zstd, br, gzip", smallest("zstd", "br", "gzip")}, {"gzip, br", smallest("br", "gzip")}, {"gzip", "gzip"},
		{"br;q=0, gzip", "gzip"}, {"", ""}, {"*", smallest("zstd", "br", "gzip")},
	} {
		resp := get("/girl/1/ls/", c.accept)
		if got := resp.Header.Get("Content-Encoding"); got != c.want || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" ||
			resp.Header.Get("Vary") != "Accept-Encoding" {
			t.Errorf("Accept-Encoding %q: got %q, %q", c.accept, got, resp.Header.Get("Content-Type"))
		}
	}
	s, _ := repo.Open(filepath.Join(out, "repo"))
	m, _ := s.Manifest()
	ls, _, _ := s.Listing("ls", "1")
	for path, want := range map[string]string{
		"/repo/" + repo.ContentsPath(ls.Current): CachePointer,
		"/repo/" + repo.VariantPath(ls.Current, repo.VariantGirl, ls.CurrentVersion().Variants[repo.VariantGirl].SHA256): CacheImmutable,
		"/repo/manifest.json": CachePointer,
		"/repo/" + repo.SectionIndexPath("1", m.Chapters[0].SHA256): CacheImmutable,
		"/repo/" + repo.NamePath("ls", "1"):                         CachePointer,
		"/girl/1/ls/":                                               CacheSite,
		"/sw.js":                                                    CacheRevalidate,
	} {
		if got := get(path, "br").Header.Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", path, got, want)
		}
	}
	if resp := get("/nope/", ""); resp.StatusCode != 404 {
		t.Errorf("missing page: %d", resp.StatusCode)
	}
	if resp := get("/girl/1/ls", ""); resp.StatusCode != 301 {
		t.Errorf("directory without slash: %d", resp.StatusCode)
	}
	// The repository client negotiates and decodes.
	c := repo.NewClient(srv.URL+"/repo", 5*time.Second)
	if _, err := c.Manifest(context.Background()); err != nil {
		t.Errorf("client through the handler: %v", err)
	}
}

// TestOriginalJS checks that the browser reflows originals exactly as the
// static site does.
func TestOriginalJS(t *testing.T) {
	type doc struct {
		Header        string                   `json:"header"`
		Footer        string                   `json:"footer"`
		Sections      []render.OriginalSection `json:"sections"`
		Indent, Width int
		TitleBlock    bool
	}
	b, err := os.ReadFile("../render/testdata/original.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Header, Footer string
		Sections       []struct{ Heading, Body string }
	}
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	docs := []doc{{Header: fixture.Header, Footer: fixture.Footer}}
	for _, s := range fixture.Sections {
		docs[0].Sections = append(docs[0].Sections, render.OriginalSection{Heading: s.Heading, Body: s.Body})
	}
	docs = append(docs,
		doc{Sections: []render.OriginalSection{{Heading: "SYNOPSIS", Body: ""}, {Heading: "É", Body: "       café-\n       au-lait\n"}}},
		doc{Header: "A  B", Sections: []render.OriginalSection{{Body: "\t\tx\r\n   y\x07\n\n\n"}}},
		doc{Sections: []render.OriginalSection{{Heading: "X", Body: "       [{\n           \"a\": 1\n       }]\n       -n with numbers\n"}}},
		// Right at the fill width: one more column and it would be filled.
		doc{Sections: []render.OriginalSection{{Heading: "X", Body: "       " + strings.Repeat("ab ", 22) + "a\n       word and more words\n"}}},
		// An RFC: text at 3, filled to 72, subsections at the margin.
		doc{Indent: 3, Width: 72, TitleBlock: true, Sections: []render.OriginalSection{
			{Body: "Internet Engineering Task Force (IETF)        R. Fielding, Ed.\nRequest for Comments: 9110            Adobe\n\n                 HTTP Semantics\n"},
			{Heading: "1.  Introduction", Body: "\n" +
				"   " + strings.Repeat("word ", 13) + "words\n   go on and on, as RFC 2119 says they MUST.\n\n" +
				"1.1.  Requirements Language\n\n   The key words \"MUST\" and \"SHOULD\" are to be interpreted as in\n   BCP 14.\n\n" +
				"      +--------+\n      | header |\n      +--------+\n"}}},
	)
	// What RFC 1459 and others had taken for headings, and the headings.
	rb, err := os.ReadFile("../render/testdata/rfc.json")
	if err != nil {
		t.Fatal(err)
	}
	var rfcSections []render.OriginalSection
	if err := json.Unmarshal(rb, &rfcSections); err != nil {
		t.Fatal(err)
	}
	docs = append(docs, doc{Indent: 3, Width: 72, Sections: rfcSections})
	type result struct {
		HTML string
		TOC  []render.Heading
	}
	link := func(n, s string) string { return "/man/" + s + "/" + n + "/" }
	var want []result
	for _, d := range docs {
		h, toc := render.OriginalHTML(d.Header, d.Footer, d.Sections, render.HTMLOptions{Link: link, Indent: d.Indent, Width: d.Width, TitleBlock: d.TitleBlock})
		if toc == nil {
			toc = []render.Heading{}
		}
		want = append(want, result{h, toc})
	}
	// The JS reads a variant's own field names.
	var in []map[string]any
	for _, d := range docs {
		var ss []map[string]string
		for _, s := range d.Sections {
			ss = append(ss, map[string]string{"heading": s.Heading, "body": s.Body})
		}
		in = append(in, map[string]any{"header": d.Header, "footer": d.Footer, "sections": ss, "indent": d.Indent, "width": d.Width, "titleBlock": d.TitleBlock})
	}
	ds, _ := json.Marshal(in)
	got := node(t, fmt.Sprintf(`
const { renderOriginalHTML } = await import(%q);
const out = [];
for (const d of %s) {
  const r = renderOriginalHTML(d, { link: (n, s) => "/man/" + s + "/" + n + "/", indent: d.indent, width: d.width, titleBlock: d.titleBlock });
  out.push({ HTML: r.html, TOC: r.toc.map(h => ({ ID: h.id, Text: h.text, Level: h.level })) });
}
console.log(JSON.stringify(out));`, staticFile(t, "render.js"), ds))
	var js []result
	if err := json.Unmarshal(got, &js); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	for i := range want {
		if js[i].TOC == nil {
			js[i].TOC = []render.Heading{}
		}
		if js[i].HTML != want[i].HTML || fmt.Sprint(js[i].TOC) != fmt.Sprint(want[i].TOC) {
			t.Errorf("doc %d differs:\n  go: %q %v\n  js: %q %v", i, want[i].HTML, want[i].TOC, js[i].HTML, js[i].TOC)
		}
	}
}

// TestPagination checks that long listings are split into pages, linked
// to each other and from the sitemap, and that pages left over when a
// listing shrinks go.
func TestPagination(t *testing.T) {
	defer func(n int) { listPage = n }(listPage)
	listPage = 4
	out := t.TempDir()
	var ps []pageSpec
	for _, n := range []string{"apple", "avocado", "banana", "blueberry", "cherry", "date", "elder", "fig", "grape", "guava"} {
		ps = append(ps, pageSpec{name: n, sec: "6", desc: "a fruit"})
	}
	o := Options{URL: "https://example.test", Version: "test"}
	build(t, out, ps, o)
	p1 := read(t, filepath.Join(out, "section", "6", "index.html"))
	p2 := read(t, filepath.Join(out, "section", "6", "page", "2", "index.html"))
	p3 := read(t, filepath.Join(out, "section", "6", "page", "3", "index.html"))
	for name, want := range map[string][]string{
		"page 1": {"apple", "blueberry", `<link rel="next" href="/section/6/page/2/">`, `href="/section/6/#letter-A">A</a>`,
			`href="/section/6/page/2/#letter-C">C</a>`, `<span class="pill on" aria-current="page">1</span>`,
			`data-first="/section/6/" data-page="1" data-pages="3"`, "page 1 of 3", `src="/static/takeover.js"`},
		"page 2": {"cherry", "fig", `<link rel="prev" href="/section/6/">`, `<link rel="next" href="/section/6/page/3/">`,
			`<link rel="canonical" href="https://example.test/section/6/page/2/">`, "· page 2 · girlpages 💅</title>"},
		"page 3": {"grape", "guava", `<h2 class="letter" id="letter-G">G</h2>`},
	} {
		page := map[string]string{"page 1": p1, "page 2": p2, "page 3": p3}[name]
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Errorf("%s lacks %q", name, w)
			}
		}
	}
	if strings.Contains(p1, ">cherry<") || strings.Contains(p2, ">apple<") || strings.Contains(p3, `rel="next"`) {
		t.Error("pages overlap, or the last goes on")
	}
	// A letter carried over a page boundary is marked, without an id.
	build(t, out, append(ps, pageSpec{name: "banyan", sec: "6", desc: "a tree"}, pageSpec{name: "beech", sec: "6", desc: "a tree"}), o)
	p2 = read(t, filepath.Join(out, "section", "6", "page", "2", "index.html"))
	if !strings.Contains(p2, `<h2 class="letter cont">B <span class="sub">(continued)</span></h2>`) || strings.Contains(p2, `id="letter-B"`) {
		t.Errorf("page 2 should carry B on from page 1:\n%s", p2)
	}
	sitemap := read(t, filepath.Join(out, "sitemap.xml"))
	for _, u := range []string{"https://example.test/section/6/", "https://example.test/section/6/page/3/"} {
		if !strings.Contains(sitemap, "<loc>"+u+"</loc>") {
			t.Errorf("sitemap lacks %s", u)
		}
	}
	// Fewer pages: the last listing page goes.
	os.RemoveAll(filepath.Join(out, "repo"))
	build(t, out, ps[:6], o)
	if _, err := os.Stat(filepath.Join(out, "section", "6", "page", "3", "index.html")); err == nil {
		t.Error("page 3 of section 6 is still there")
	}
}

// TestBase91JS checks that the browser decodes basE91 as Go encodes it.
func TestBase91JS(t *testing.T) {
	var in [][]byte
	var enc []string
	for i := 0; i < 200; i++ {
		b := make([]byte, i%40)
		for j := range b {
			b[j] = byte((i*131 + j*31) ^ (j * 7))
		}
		in = append(in, b)
		enc = append(enc, repo.Encode91(b))
	}
	es, _ := json.Marshal(enc)
	got := node(t, fmt.Sprintf(`
const { decode91 } = await import(%q);
console.log(JSON.stringify(%s.map(e => decode91(e))));`, staticFile(t, "format.js"), es))
	var dec [][]int
	if err := json.Unmarshal(got, &dec); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	for i, b := range in {
		ok := len(dec[i]) == len(b)
		for j := 0; ok && j < len(b); j++ {
			ok = dec[i][j] == int(b[j])
		}
		if !ok {
			t.Errorf("%x encoded %q, decoded %v", b, enc[i], dec[i])
		}
	}
}

// No variant girlrepo makes may be named as one of the site's own
// directories, or every page in it would be one of those.
func TestReservedNames(t *testing.T) {
	reserved := map[string]bool{}
	for _, r := range Reserved {
		reserved[r] = true
	}
	for _, v := range llm.Variants() {
		if reserved[v.Name] || manpage.IsSeries(v.Name) && v.Name == "rfc" {
			t.Errorf("the %s variant is named as a reserved directory", v.Name)
		}
	}
	if reserved["girl"] || reserved["man"] {
		t.Error("girl and man are variants")
	}
}
