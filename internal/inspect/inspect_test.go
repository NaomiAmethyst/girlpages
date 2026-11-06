// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package inspect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

type spec struct {
	name, sec, distro, pkg, version string
	girl                            bool
}

var specs = []spec{
	{"ls", "1", "arch", "coreutils", "9.12-1", true},
	{"ls", "1", "fedora", "coreutils", "9.12-2", true},
	{"ls", "1", "debian", "coreutils", "9.7-1", true},
	{"ls", "1", "arch", "9base", "6-8", false},
	{"ls", "1", "freebsd", "base", "15.1", false},
	{"printf", "3", "debian", "manpages-dev", "6.9-1", true},
	{"grep", "1", "debian", "grep", "3.11-4", true},
	{"7", "rfc", "ietf", "", "", false},
	{"SSL_new", "3ssl", "debian", "libssl-doc", "3.0-1", true},
}

// published makes a published repository, with a page with several versions,
// a variant remade, and packs, and returns where it is.
func published(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	s, err := repo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range specs {
		title := strings.ToUpper(p.name) + "(" + p.sec + ")"
		text := title + " " + p.pkg + " " + p.version + "\n\nNAME\n       " + p.name + " - does things\n\nDESCRIPTION\n       does things in version " + p.version + "\n\nfooter\n"
		src := repo.Hash([]byte(text))
		mi, err := s.PutVariant(repo.ManVariant(src, text))
		if err != nil {
			t.Fatal(err)
		}
		vs := map[string]repo.VariantInfo{repo.VariantMan: mi}
		if p.girl {
			gi, err := s.PutVariant(repo.GirlVariant(src, title, "# ✨ NAME ✨\n\n`"+p.name+"` slays 💅\n\n# 🌸 OPTIONS 🌸\n\nall of them\n", &repo.Generator{Model: "fake", Generated: time.Unix(1700000000, 0).UTC()}))
			if err != nil {
				t.Fatal(err)
			}
			if p.name == "printf" {
				// Remade, so there's an older generation.
				if gi, err = s.PutVariant(repo.GirlVariant(src, title, "# ✨ NAME ✨\n\n`printf` slays harder 💅\n", &repo.Generator{Model: "fake-2", Generated: time.Unix(1700000001, 0).UTC()})); err != nil {
					t.Fatal(err)
				}
			}
			vs[repo.VariantGirl] = gi
		}
		pkg := repo.Package{Distro: p.distro, Release: "x", Name: p.pkg, Version: p.version}
		err = s.UpdateListing(p.name, p.sec, func(l *repo.Listing) error {
			l.Versions = append(l.Versions, repo.Version{Source: src, Title: title, Description: "does things",
				Added: time.Unix(1700000000+int64(i), 0).UTC(), Variants: vs, Packages: []repo.Package{pkg}})
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
	if _, err := s.Publish(pages); err != nil {
		t.Fatal(err)
	}
	// (Without the listings each version above left behind.)
	if _, err := s.GC(false); err != nil {
		t.Fatal(err)
	}
	return dir
}

// serve serves a directory as a remote repository is: files, and no listings.
func serve(t *testing.T, dir string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.FromSlash(r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, p)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// both runs a test against a repository locally, and over HTTP.
func both(t *testing.T, dir string, test func(t *testing.T, r *Repo)) {
	t.Helper()
	t.Run("local", func(t *testing.T) { test(t, New(repo.NewClient(dir, time.Minute))) })
	t.Run("remote", func(t *testing.T) { test(t, New(repo.NewClient(serve(t, dir), time.Minute))) })
}

func kinds(t *testing.T, r *Repo, addr string) []string {
	t.Helper()
	ctx := context.Background()
	n, err := r.Resolve(ctx, addr)
	if err != nil {
		t.Fatalf("%s: %v", addr, err)
	}
	var out []string
	if err := n.Children(ctx, func(c *Node) error {
		out = append(out, c.Addr+" "+c.Kind)
		return nil
	}); err != nil {
		t.Fatalf("%s: %v", addr, err)
	}
	return out
}

func TestEveryFileIsReachable(t *testing.T) {
	dir := published(t)
	var want []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		// What isn't part of a repository (see doc/repo-format.md).
		if rel == ".lock" || strings.HasPrefix(filepath.Base(rel), ".tmp-") || regexp.MustCompile(`\.(br|gz|zst)$`).MatchString(rel) {
			return nil
		}
		want = append(want, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(want) < 20 {
		t.Fatalf("only %d files to look for", len(want))
	}
	both(t, dir, func(t *testing.T, r *Repo) {
		ctx := context.Background()
		var mu sync.Mutex
		got := map[string]bool{}
		var problems []string
		st := r.Check(ctx, r.Root(), CheckOptions{Recursive: true, Jobs: 4,
			Checked: func(n *Node) {
				mu.Lock()
				defer mu.Unlock()
				if n.File != "" {
					got[n.File] = true
				}
			},
			Problem: func(n *Node, warn bool, message string) {
				mu.Lock()
				defer mu.Unlock()
				problems = append(problems, n.Addr+": "+message)
			},
		})
		if st.Problems != 0 || st.Warnings != 0 {
			t.Errorf("a good repository has problems: %q", problems)
		}
		for _, f := range want {
			if !got[f] {
				t.Errorf("nothing leads to %s", f)
			}
		}
	})
}

func TestAddresses(t *testing.T) {
	dir := published(t)
	both(t, dir, func(t *testing.T, r *Repo) {
		ctx := context.Background()
		for addr, wantKind := range map[string]string{
			"":                                "manifest",
			"/":                               "manifest",
			"manifest.json":                   "manifest",
			"chapters":                        "group",
			"chapters/1":                      "chapter",
			"chapters/1/pointer":              "pointer",
			"chapters/1/shards/1":             "chapter-shard",
			"chapters/3/sections/3ssl":        "section",
			"sections/3ssl":                   "section",
			"sources":                         "group",
			"sources/debian":                  "source",
			"sources/debian/x":                "source",
			"pages":                           "group",
			"pages/ls(1)":                     "page",
			"ls(1)":                           "page",
			"ls":                              "page",
			"7(rfc)":                          "page",
			"pages/ls(1)/pointer":             "pointer",
			"ls(1)/pointer":                   "pointer",
			"ls(1)@9.7":                       "version",
			"pages/ls(1)/9.7":                 "version",
			"ls(1)/latest":                    "version",
			"ls(1)/debian":                    "version",
			"ls(1)/debian/x":                  "version",
			"ls(1)@9.7/contents":              "contents",
			"ls(1)@9.7/girl":                  "variant",
			"ls(1)/girl":                      "variant",
			"ls(1)/man":                       "variant",
			"ls(1)@9.7/girl/headings/0":       "heading",
			"ls(1)@9.7/girl/headings/OPTIONS": "heading",
			"printf(3)/girl/generations/1":    "variant",
			"printf(3)/girl/generations/2":    "variant",
			"index":                           "group",
			"index/words":                     "shard-manifest",
			"index/words/shards/1":            "word-shard",
			"index/words/ls":                  "word",
			"index/grams":                     "shard-manifest",
			"index/grams/shards/1":            "gram-shard",
			"index/grams/^ls":                 "gram",
			"index/names":                     "table-manifest",
			"index/names/shards/1":            "name-shard",
			"index/names/0":                   "name",
			"index/docs":                      "table-manifest",
			"index/docs/shards/1":             "doc-shard",
			"index/docs/0":                    "doc",
			"index/variants":                  "group",
			"index/variants/girl":             "shard-manifest",
			"index/variants/girl/shards/1":    "source-shard",
			"index/packages":                  "package-index",
			"index/packages/shards/1":         "package-chunk",
			"index/packages/coreutils":        "package",
			"index/packages/coreutils/pages":  "package-pages",
		} {
			n, err := r.Resolve(ctx, addr)
			if err != nil {
				t.Errorf("%q: %v", addr, err)
				continue
			}
			if n.Kind != wantKind {
				t.Errorf("%q is a %s, not a %s", addr, n.Kind, wantKind)
			}
			// Everything has something to say, however it's asked.
			var b bytes.Buffer
			if err := n.Describe(ctx, &b); err != nil || b.Len() == 0 {
				t.Errorf("describe %q: %v", addr, err)
			}
			b.Reset()
			if err := n.Cat(ctx, &b); err != nil {
				t.Errorf("cat %q: %v", addr, err)
			}
			if n.File != "" || n.value != nil {
				doc, err := n.JSON(ctx)
				if err != nil || !json.Valid(doc) {
					t.Errorf("get %q: %v: %.60s", addr, err, doc)
				}
			}
		}
		for _, addr := range []string{"nope(1)", "nothing", "chapters/99", "chapters/1/shards/99", "sections/99", "sources/nope",
			"ls(1)@99", "ls(1)/witch", "ls(1)/girl/headings/99", "ls(1)/girl/generations/9", "printf(3)/girl/nope",
			"index/nope", "index/words/shards/999", "index/words/zzzzzzzz", "index/names/9999999", "index/packages/nope", "packs/99"} {
			if _, err := r.Resolve(ctx, addr); !errors.Is(err, repo.ErrNotFound) {
				t.Errorf("%q: %v", addr, err)
			}
		}
	})
}

func TestTree(t *testing.T) {
	dir := published(t)
	both(t, dir, func(t *testing.T, r *Repo) {
		ctx := context.Background()
		root := kinds(t, r, "/")
		for _, want := range []string{"chapters group", "sections group", "pages group", "sources group", "index group"} {
			if !slices.Contains(root, want) {
				t.Errorf("/ doesn't have %q: %q", want, root)
			}
		}
		page := kinds(t, r, "ls(1)")
		if page[0] != "pages/ls(1)/pointer pointer" || !strings.HasPrefix(page[1], "pages/ls(1)/") || len(page) != 6 {
			t.Errorf("ls(1): %q", page)
		}
		version := kinds(t, r, "ls(1)@9.7")
		if !slices.Contains(version, "pages/ls(1)/9.7/contents contents") || !slices.Contains(version, "pages/ls(1)/9.7/girl variant") {
			t.Errorf("a version: %q", version)
		}
		variant := kinds(t, r, "printf(3)/girl")
		if !slices.ContainsFunc(variant, func(s string) bool { return strings.HasSuffix(s, "/girl/headings/0 heading") }) ||
			!slices.ContainsFunc(variant, func(s string) bool { return strings.HasSuffix(s, "/girl/generations/1 variant") }) {
			t.Errorf("a variant: %q", variant)
		}

		// --follow from a chapter is its pages, and doesn't go through sections.
		n, err := r.Resolve(ctx, "chapters/3")
		if err != nil {
			t.Fatal(err)
		}
		var pages []string
		if err := Follow(ctx, n, 1, func(c *Node) error {
			pages = append(pages, c.Addr)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		slices.Sort(pages)
		if want := []string{"pages/SSL_new(3ssl)", "pages/printf(3)"}; !slices.Equal(pages, want) {
			t.Errorf("following chapter 3: %q, want %q", pages, want)
		}

		// A section's a view of its chapter.
		if got := kinds(t, r, "sections/3ssl"); !slices.Equal(got, []string{"pages/SSL_new(3ssl) page"}) {
			t.Errorf("a section: %q", got)
		}

		// Text, of each kind.
		for addr, want := range map[string]string{
			"ls(1)":                           "slays",
			"ls(1)/man":                       "does things",
			"printf(3)/girl":                  "slays harder",
			"printf(3)/girl/generations/1":    "`printf` slays 💅",
			"ls(1)@9.7/girl/headings/OPTIONS": "all of them",
			"chapters/1":                      "grep(1)",
			"index/words/ls":                  "ls(1)",
			"index/grams/^ls":                 "ls",
			"index/packages/coreutils/pages":  "ls(1)",
		} {
			n, err := r.Resolve(ctx, addr)
			if err != nil {
				t.Errorf("%s: %v", addr, err)
				continue
			}
			var b bytes.Buffer
			if err := n.Cat(ctx, &b); err != nil || !strings.Contains(b.String(), want) {
				t.Errorf("cat %s: %v: %q, want %q", addr, err, b.String(), want)
			}
		}

		// Why the current version is what it is.
		n, err = r.Resolve(ctx, "ls(1)")
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if err := n.Describe(ctx, &b); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"main package: coreutils 9.12", "9base 6", "15.1           freebsd", "← current"} {
			if !strings.Contains(b.String(), want) {
				t.Errorf("describing ls(1), no %q:\n%s", want, b.String())
			}
		}
	})
}

func TestFilesByPath(t *testing.T) {
	dir := published(t)
	r := New(repo.NewClient(dir, time.Minute))
	ctx := context.Background()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		n, err := r.Resolve(ctx, rel)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			return nil
		}
		if n.File != rel {
			t.Errorf("%s is %s", rel, n.File)
		}
		if KindOfFile(rel) == "" {
			t.Errorf("%s has no kind", rel)
		}
		r.Check(ctx, n, CheckOptions{Problem: func(_ *Node, _ bool, message string) { t.Errorf("%s: %s", rel, message) }})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// damaged checks a repository with something done to one of its files.
func damaged(t *testing.T, addr string, damage func(file string, data []byte) []byte) []string {
	t.Helper()
	dir := published(t)
	r := New(repo.NewClient(dir, time.Minute))
	ctx := context.Background()
	n, err := r.Resolve(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, filepath.FromSlash(n.File))
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, damage(n.File, data), 0o644); err != nil {
		t.Fatal(err)
	}
	r = New(repo.NewClient(dir, time.Minute))
	var mu sync.Mutex
	var problems []string
	r.Check(ctx, r.Root(), CheckOptions{Recursive: true, Jobs: 4, Problem: func(n *Node, warn bool, message string) {
		mu.Lock()
		defer mu.Unlock()
		problems = append(problems, n.Addr+": "+message)
	}})
	return problems
}

func TestCheckFindsProblems(t *testing.T) {
	flip := func(old, new string) func(string, []byte) []byte {
		return func(_ string, b []byte) []byte { return bytes.Replace(b, []byte(old), []byte(new), 1) }
	}
	for _, c := range []struct {
		what, addr string
		damage     func(string, []byte) []byte
		want       string
	}{
		{"a listing changed", "ls(1)", flip("does things", "did things"), "doesn't match its hash"},
		{"a variant changed", "ls(1)/girl", flip("slays", "slayed"), "doesn't match its hash"},
		{"a shard changed", "index/words/shards/1", func(_ string, b []byte) []byte { return append(b, ' ') }, "doesn't match its hash"},
		{"not JSON", "index/grams/shards/1", func(string, []byte) []byte { return []byte("<html>oops") }, "isn't JSON"},
		{"a pointer to elsewhere", "ls(1)/pointer", func(_ string, b []byte) []byte {
			return regexp.MustCompile(`"sha256":\s*"[0-9a-f]+"`).ReplaceAll(b, []byte(`"sha256":"`+strings.Repeat("0", 64)+`"`))
		}, "its pointer says"},
		{"a pointer for something else", "ls(1)/pointer", flip(`"ls"`, `"lsd"`), "it's for lsd(1)"},
		{"a missing file", "printf(3)/girl/generations/1", func(string, []byte) []byte { return nil }, "isn't JSON"},
	} {
		t.Run(c.what, func(t *testing.T) {
			problems := damaged(t, c.addr, c.damage)
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, c.want) }) {
				t.Errorf("no %q in %q", c.want, problems)
			}
		})
	}

	// A file that's gone is noticed by what leads to it.
	dir := published(t)
	r := New(repo.NewClient(dir, time.Minute))
	ctx := context.Background()
	n, err := r.Resolve(ctx, "ls(1)/man")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(n.File))); err != nil {
		t.Fatal(err)
	}
	st := r.Check(ctx, r.Root(), CheckOptions{Recursive: true, Jobs: 4})
	if st.Problems == 0 {
		t.Error("a missing variant wasn't noticed")
	}
}

func TestCheckStaleCurrent(t *testing.T) {
	dir := published(t)
	r := New(repo.NewClient(dir, time.Minute))
	ctx := context.Background()
	n, err := r.Resolve(ctx, "ls(1)")
	if err != nil {
		t.Fatal(err)
	}
	var l repo.Listing
	if err := r.get(ctx, n.File, &l); err != nil {
		t.Fatal(err)
	}
	var problems []string
	checkListing(&l, "ls", "1", func(_ bool, format string, a ...any) { problems = append(problems, format) })
	if len(problems) != 0 {
		t.Errorf("a good listing: %q", problems)
	}
	// What an older girlrepo chose, say.
	l.Current = l.Versions[len(l.Versions)-1].Source
	if l.Current == l.Latest().Source {
		l.Current = l.Versions[0].Source
	}
	problems = nil
	checkListing(&l, "ls", "1", func(_ bool, format string, a ...any) { problems = append(problems, format) })
	if len(problems) != 1 || !strings.Contains(problems[0], "publish again") {
		t.Errorf("a stale current: %q", problems)
	}
}

// What a repository says can be anything: it's a problem, not a crash.
func TestBadHashes(t *testing.T) {
	bad := []string{"", "x", "../../etc/passwd", strings.Repeat("z", 64)}
	ctx := context.Background()
	for _, h := range bad {
		for what, damage := range map[string]func(file string) *regexp.Regexp{
			"ls(1)/pointer": func(string) *regexp.Regexp { return regexp.MustCompile(`"sha256":\s*"[0-9a-f]+"`) },
			"ls(1)":         func(string) *regexp.Regexp { return regexp.MustCompile(`"source":\s*"[0-9a-f]+"`) },
			"ls(1)/latest/contents": func(string) *regexp.Regexp {
				return regexp.MustCompile(`"sha256":\s*"[0-9a-f]+"`)
			},
		} {
			field := strings.SplitN(damage("").String(), `"`, 3)[1]
			problems := damaged(t, what, func(file string, b []byte) []byte {
				return damage(file).ReplaceAll(b, []byte(`"`+field+`":"`+h+`"`))
			})
			if len(problems) == 0 {
				t.Errorf("%s with hash %q: no problems", what, h)
			}
		}
	}
	// A manifest with nonsense in it too.
	dir := published(t)
	manifest := filepath.Join(dir, "manifest.json")
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	b = regexp.MustCompile(`"sha256":\s*"[0-9a-f]+"`).ReplaceAll(b, []byte(`"sha256":"nope"`))
	b = regexp.MustCompile(`"(words|grams|names|docs|packages)":\s*"[0-9a-f]+"`).ReplaceAll(b, []byte(`"$1":"nope"`))
	if err := os.WriteFile(manifest, b, 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(repo.NewClient(dir, time.Minute))
	if st := r.Check(ctx, r.Root(), CheckOptions{Recursive: true, Jobs: 4}); st.Problems == 0 {
		t.Error("a manifest of bad hashes passed")
	}
}

func TestOrderIgnoresCase(t *testing.T) {
	var problems []string
	report := func(_ bool, format string, a ...any) { problems = append(problems, format) }
	checkOrder([]repo.IndexPage{{Name: "LAcheck", Section: "1"}, {Name: "lacheck", Section: "1"}, {Name: "ls", Section: "1"}}, report)
	checkOrder([]repo.IndexPage{{Name: "lacheck", Section: "1"}, {Name: "LAcheck", Section: "1"}}, report)
	if len(problems) != 0 {
		t.Errorf("names differing in case: %q", problems)
	}
	checkOrder([]repo.IndexPage{{Name: "ls", Section: "1"}, {Name: "cat", Section: "1"}}, report)
	if len(problems) != 1 {
		t.Errorf("out of order: %q", problems)
	}
}

func TestSample(t *testing.T) {
	dir := published(t)
	r := New(repo.NewClient(dir, time.Minute))
	ctx := context.Background()
	all := r.Check(ctx, r.Root(), CheckOptions{Recursive: true, Jobs: 4})
	some := r.Check(ctx, r.Root(), CheckOptions{Recursive: true, Jobs: 4, Sample: 1})
	if some.Checked == 0 || some.Checked >= all.Checked {
		t.Errorf("a sample checked %d of %d", some.Checked, all.Checked)
	}
}

func TestPacks(t *testing.T) {
	dir := published(t)
	ctx := context.Background()
	packsDir := t.TempDir()
	if _, err := repo.AppendPacks(ctx, packsDir, dir, repo.PackOptions{}); err != nil {
		t.Fatal(err)
	}
	log, err := repo.ReadPackLog(packsDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.WritePackManifest(packsDir, log.Packs); err != nil {
		t.Fatal(err)
	}
	both(t, packsDir, func(t *testing.T, r *Repo) {
		// A pack-only repository has its packs, and what's in them.
		m, err := r.Manifest(ctx)
		if err != nil || len(m.Packs) == 0 {
			t.Fatalf("%v, %+v", err, m)
		}
		if _, err := r.Resolve(ctx, "ls(1)"); err == nil || !strings.Contains(err.Error(), "pack-only") {
			t.Errorf("a page, of a pack-only repository: %v", err)
		}
		first := r.pack(m.Packs[0])
		if got := kinds(t, r, "/"); !slices.Contains(got, "packs group") {
			t.Errorf("/: %q", got)
		}
		if got := kinds(t, r, "packs"); !slices.Contains(got, first.Addr+" pack") {
			t.Errorf("packs: %q", got)
		}
		// It's described and checked, but what's in it isn't read out of it.
		n, err := r.Resolve(ctx, first.Addr)
		if err != nil || n.Kind != "pack" {
			t.Fatalf("%s: %v, %+v", first.Addr, err, n)
		}
		if got := kinds(t, r, first.Addr); len(got) != 0 {
			t.Errorf("under a pack: %q", got)
		}
		if _, err := r.Resolve(ctx, first.Addr+"/page/name-1/ls.json"); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("a file in a pack: %v", err)
		}
		doc, err := n.JSON(ctx)
		if err != nil || !bytes.Contains(doc, []byte(`"header"`)) {
			t.Errorf("a pack's JSON: %v: %s", err, doc)
		}
		if _, err := r.Resolve(ctx, first.Hash[:8]); err == nil {
			t.Errorf("a bare hash isn't an address")
		}
		if byHash, err := r.Resolve(ctx, "packs/"+first.Hash[:8]); err != nil || byHash.Hash != first.Hash {
			t.Errorf("a pack by its hash: %v", err)
		}
		var b bytes.Buffer
		if err := first.Describe(ctx, &b); err != nil || !strings.Contains(b.String(), "puts:") {
			t.Errorf("describing a pack: %v: %s", err, b.String())
		}
		var problems []string
		r.Check(ctx, first, CheckOptions{Problem: func(_ *Node, _ bool, message string) { problems = append(problems, message) }})
		if len(problems) != 0 {
			t.Errorf("a good pack: %q", problems)
		}
	})

	// A damaged one is noticed.
	r := New(repo.NewClient(packsDir, time.Minute))
	m, err := r.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(packsDir, filepath.FromSlash(m.Packs[0].Path))
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if st := r.Check(ctx, r.pack(m.Packs[0]), CheckOptions{}); st.Problems == 0 {
		t.Error("a damaged pack wasn't noticed")
	}
}

func TestAsFile(t *testing.T) {
	dir := published(t)
	r := New(repo.NewClient(dir, time.Minute))
	ctx := context.Background()
	n, err := r.Resolve(ctx, "ls(1)")
	if err != nil {
		t.Fatal(err)
	}
	data, err := r.fetch(ctx, n.File)
	if err != nil {
		t.Fatal(err)
	}
	stray := New(repo.NewClient(t.TempDir(), time.Minute))
	check := func(kind, name string, b []byte) int64 {
		t.Helper()
		f, err := stray.AsFile(kind, name, b)
		if err != nil {
			t.Fatal(err)
		}
		return stray.Check(ctx, f, CheckOptions{}).Problems
	}
	if p := check("page", "stray.json", data); p != 0 {
		t.Errorf("a good listing: %d problems", p)
	}
	if p := check("page", "stray.json", []byte(`{"format":1,"name":"x","section":"1","versions":[]}`)); p == 0 {
		t.Error("a listing with no versions passed")
	}
	if p := check("pointer", "stray.json", []byte("not json")); p == 0 {
		t.Error("something not JSON passed")
	}
	if p := check("page", strings.Repeat("0", 64)+".json", data); p == 0 {
		t.Error("a listing named after the wrong hash passed")
	}
	if _, err := stray.AsFile("nonsense", "x", nil); err == nil {
		t.Error("a nonsense kind was taken")
	}
	for _, kind := range Kinds {
		f, err := stray.AsFile(kind, "x.json", []byte(`{"format":1}`))
		if err != nil {
			t.Errorf("%s: %v", kind, err)
			continue
		}
		stray.Check(ctx, f, CheckOptions{})
	}
}
