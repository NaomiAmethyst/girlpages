// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package remote

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

type spec struct {
	name, sec, desc string
	girl            bool
	pkg             repo.Package
	added           int64
}

var specs = []spec{
	// ls(1): the latest isn't girlified yet, an older version is.
	{name: "ls", sec: "1", desc: "list directory contents", girl: true, added: 1,
		pkg: repo.Package{Distro: "ubuntu", Release: "questing", Name: "coreutils", Version: "9.4-3"}},
	{name: "ls", sec: "1", desc: "list directory contents, newer", added: 2,
		pkg: repo.Package{Distro: "debian", Release: "trixie", Name: "coreutils", Version: "9.7-1"}},
	{name: "ls", sec: "1posix", desc: "list directory contents (POSIX)", girl: true},
	{name: "printf", sec: "3", desc: "formatted output conversion", girl: true},
	{name: "printf", sec: "1", desc: "format and print data", girl: true},
	{name: "printf", sec: "3p", desc: "print formatted output (POSIX)"},
	{name: "CGI", sec: "3pm", desc: "Handle Common Gateway Interface requests", girl: true},
	{name: "9110", sec: "rfc", desc: "HTTP Semantics"},
}

// published makes a repository of specs, published, and a Repo reading it.
func published(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	s, err := repo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range specs {
		title := strings.ToUpper(p.name) + "(" + p.sec + ")"
		text := title + "   Manual   " + title + "\n\nNAME\n       " + p.name + " - " + p.desc +
			"\n\nDESCRIPTION\n       " + p.desc + "\n\nfooter   " + title + "\n"
		src := repo.Hash([]byte(text))
		mi, err := s.PutVariant(repo.ManVariant(src, text))
		if err != nil {
			t.Fatal(err)
		}
		vs := map[string]repo.VariantInfo{repo.VariantMan: mi}
		if p.girl {
			md := "# ✨ NAME ✨\n\n`" + p.name + "` slays 💅\n"
			gi, err := s.PutVariant(repo.GirlVariant(src, title, md, &repo.Generator{Model: "fake-1", Generated: time.Unix(1700000000, 0)}))
			if err != nil {
				t.Fatal(err)
			}
			vs[repo.VariantGirl] = gi
		}
		pkg := p.pkg
		if pkg.Distro == "" {
			pkg = repo.Package{Distro: "debian", Release: "trixie", Name: p.name, Version: "1.0-1"}
		}
		err = s.UpdateListing(p.name, p.sec, func(l *repo.Listing) error {
			l.Versions = append(l.Versions, repo.Version{Source: src, Title: title, Description: p.desc,
				Added: time.Unix(1700000000+p.added, 0).UTC(), Variants: vs, Packages: []repo.Package{pkg}})
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
	return New(repo.NewClient(dir, time.Minute))
}

func TestRequests(t *testing.T) {
	for _, c := range []struct {
		args []string
		want []Request
	}{
		{[]string{"ls"}, []Request{{"ls", ""}}},
		{[]string{"1", "ls", "cp"}, []Request{{"ls", "1"}, {"cp", "1"}}},
		{[]string{"ls(1posix)", "open"}, []Request{{"ls", "1posix"}, {"open", ""}}},
		{[]string{"rfc", "09110"}, []Request{{"9110", "rfc"}}},
		{[]string{"RFC9110"}, []Request{{"9110", "rfc"}}},
		// A section alone is a page's name, as man has it.
		{[]string{"1"}, []Request{{"1", ""}}},
		{[]string{"systemd.unit"}, []Request{{"systemd.unit", ""}}},
	} {
		if got := Requests(c.args); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Requests(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

// titles lists pages found as NAME(SECTION).
func titles(ps []Page) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Title())
	}
	return out
}

func TestFind(t *testing.T) {
	r := published(t)
	ctx := context.Background()
	for _, c := range []struct {
		q    Request
		o    Options
		want []string
	}{
		{Request{"ls", ""}, Options{}, []string{"LS(1)"}},
		{Request{"ls", "1posix"}, Options{}, []string{"LS(1posix)"}},
		// Like man: 1 before 3, 3 before 3p.
		{Request{"printf", ""}, Options{All: true}, []string{"PRINTF(1)", "PRINTF(3)", "PRINTF(3p)"}},
		{Request{"printf", ""}, Options{Sections: []string{"3"}}, []string{"PRINTF(3)"}},
		{Request{"printf", "3"}, Options{All: true}, []string{"PRINTF(3)", "PRINTF(3p)"}},
		{Request{"printf", "3"}, Options{}, []string{"PRINTF(3)"}},
		// Any case, as man finds them.
		{Request{"PRINTF", ""}, Options{}, []string{"PRINTF(1)"}},
		{Request{"cgi", ""}, Options{}, []string{"CGI(3pm)"}},
		{Request{"9110", "rfc"}, Options{}, []string{"RFC 9110"}},
	} {
		ps, err := r.Find(ctx, c.q, c.o)
		if err != nil {
			t.Errorf("Find(%v, %+v): %v", c.q, c.o, err)
			continue
		}
		if got := titles(ps); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Find(%v, %+v) = %v, want %v", c.q, c.o, got, c.want)
		}
	}
	for _, q := range []Request{{"nope", ""}, {"ls", "8"}, {"printf", "2"}} {
		_, err := r.Find(ctx, q, Options{})
		if !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("Find(%v) = %v, want ErrNotFound", q, err)
		} else if strings.Count(err.Error(), "repo") != 1 {
			t.Errorf("Find(%v) says %q", q, err)
		}
	}
}

func TestVersionsAndVariants(t *testing.T) {
	r := published(t)
	ctx := context.Background()
	find := func(o Options) (*Page, error) {
		ps, err := r.Find(ctx, Request{"ls", "1"}, o)
		if err != nil {
			return nil, err
		}
		return &ps[0], nil
	}

	// The latest, which isn't girlified: the original, saying which is.
	p, err := find(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Latest() || p.Version.Upstream() != "9.7" || p.Variant.Variant != repo.VariantMan || !p.Fallback {
		t.Errorf("latest: %s %s, fallback %v", p.Version.Upstream(), p.Variant.Variant, p.Fallback)
	}
	if g := p.Girly(); g != "9.4" {
		t.Errorf("Girly() = %q, want 9.4", g)
	}
	if a := p.About(); a != "" {
		t.Errorf("About() of the latest original = %q", a)
	}

	// Picking a version, every way it can be.
	older := ""
	for _, want := range []string{"9.4", "ubuntu", "ubuntu/questing", "ubuntu questing"} {
		p, err := find(Options{Version: want})
		if err != nil {
			t.Errorf("version %q: %v", want, err)
			continue
		}
		if p.Version.Upstream() != "9.4" || p.Variant.Variant != repo.VariantGirl || p.Fallback {
			t.Errorf("version %q: %s %s", want, p.Version.Upstream(), p.Variant.Variant)
		}
		if a := p.About(); a != "version 9.4 from ubuntu questing" {
			t.Errorf("version %q: About() = %q", want, a)
		}
		older = p.Version.Source
	}
	if p, err := find(Options{Version: older[:8]}); err != nil || p.Version.Source != older {
		t.Errorf("by hash: %v", err)
	}
	if p, err := find(Options{Version: "debian/trixie"}); err != nil || p.Version.Upstream() != "9.7" {
		t.Errorf("debian/trixie: %v", err)
	}

	if p, err := find(Options{Version: "9.4", Variant: "man"}); err != nil || p.Variant.Variant != repo.VariantMan || p.Fallback {
		t.Errorf("9.4's original: %v", err)
	}
	for _, o := range []Options{{Version: "1.0"}, {Variant: "pirate"}, {Variant: "girl"}} {
		if _, err := find(o); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("%+v: %v, want ErrNotFound", o, err)
		}
	}

	ls, err := r.Listings(ctx, Request{"ls", "1"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := WriteVersions(&b, ls[0]); err != nil {
		t.Fatal(err)
	}
	want := "ls(1): 2 versions\n" +
		"  9.7  debian trixie    man  (latest)\n" +
		"  9.4  ubuntu questing  girl man\n"
	if b.String() != want {
		t.Errorf("WriteVersions:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestSearch(t *testing.T) {
	r := published(t)
	ctx := context.Background()
	hits, err := r.Search(ctx, "formatted output", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := WriteHits(&b, hits); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), "printf (3)           - formatted output conversion\n") {
		t.Errorf("search:\n%s", b.String())
	}
	hits, err = r.Search(ctx, "formatted output", []string{"3p"}, 10)
	if err != nil || len(hits) != 1 || hits[0].Section != "3p" {
		t.Errorf("search in 3p: %v %v", hits, err)
	}
	hits, err = r.Whatis(ctx, Request{"printf", ""}, nil)
	if err != nil || len(hits) != 3 || hits[0].Section != "1" {
		t.Errorf("whatis printf: %v %v", hits, err)
	}
}

func TestReader(t *testing.T) {
	r := published(t)
	ctx := context.Background()
	read := func(o Options, args ...string) (string, string, int) {
		var out, errs bytes.Buffer
		rd := &Reader{Repo: r, Options: o, Prog: "girlc", Width: 80, Footer: "girlpages test", Stdout: &out, Stderr: &errs}
		st := rd.Read(ctx, args)
		return out.String(), errs.String(), st
	}

	out, _, st := read(Options{}, "ls")
	if st != ExitOK || !strings.HasPrefix(out, "💭 she hasn't gotten to this one yet, bestie, so here's the original (--page-version 9.4 is girlified)\n\n") ||
		!strings.Contains(out, "list directory contents, newer") {
		t.Errorf("ls (%d):\n%s", st, out)
	}
	out, _, st = read(Options{Version: "9.4"}, "ls")
	if st != ExitOK || !strings.Contains(out, "slays") || !strings.Contains(out, "girlpages test · version 9.4 from ubuntu questing") {
		t.Errorf("ls 9.4 (%d):\n%s", st, out)
	}
	out, _, st = read(Options{Version: "9.4", Variant: "man"}, "ls")
	if st != ExitOK || !strings.HasPrefix(out, "📌 LS(1), version 9.4 from ubuntu questing\n\n") {
		t.Errorf("ls 9.4 original (%d):\n%s", st, out)
	}
	out, errs, st := read(Options{}, "printf", "nope")
	if st != ExitNotFound || !strings.Contains(out, "slays") || errs != "girlc: there's no nope in the repo 🤷\n" {
		t.Errorf("printf nope (%d): %q\n%s", st, errs, out)
	}
	if _, _, st := read(Options{}, "nope"); st != ExitNotFound {
		t.Errorf("nope: %d", st)
	}
}
