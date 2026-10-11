// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/cache"
	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/llm/llmtest"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

type fake struct{ calls atomic.Int32 }

func (f *fake) Name() string  { return "fake" }
func (f *fake) Model() string { return "fake-1" }
func (f *fake) Complete(_ context.Context, _, user string) (string, error) {
	f.calls.Add(1)
	var opts []string
	for _, w := range strings.Fields(user) {
		if strings.HasPrefix(w, "-") {
			opts = append(opts, w)
		}
	}
	return "# ✨ NAME ✨\n\nslay " + strings.Join(opts, " "), nil
}

func roff(name, sec, desc string) string {
	return ".TH " + strings.ToUpper(name) + " " + sec + " 2026-01-01 test\n.SH NAME\n" + name + " \\- " + desc +
		"\n.SH OPTIONS\n.TP\n.B \\-v\nbe loud\n"
}

func gzipped(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func needTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range append([]string{"man"}, tools...) {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
}

// packageTree lays out an unpacked package: a page, a symlink alias, an
// absolute symlink, a .so stub and a translation (which is left out).
func packageTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	man := filepath.Join(root, "usr", "share", "man")
	write(t, filepath.Join(man, "man1", "hello.1.gz"), gzipped(roff("hello", "1", "say hello")))
	write(t, filepath.Join(man, "man5", "hello.conf.5"), []byte(".so man1/hello.1\n"))
	write(t, filepath.Join(man, "de", "man1", "hello.1.gz"), gzipped(roff("hello", "1", "sag hallo")))
	if err := os.Symlink("hello.1.gz", filepath.Join(man, "man1", "hi.1.gz")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/share/man/man1/hello.1.gz", filepath.Join(man, "man1", "hey.1.gz")); err != nil {
		t.Fatal(err)
	}
	return root
}

func sh(t *testing.T, dir, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, stderr.String())
	}
	return out
}

// makeDeb builds a .deb by hand: an ar archive holding data.tar.gz.
func makeDeb(t *testing.T, tree, path string) {
	t.Helper()
	data := sh(t, tree, "tar", "-czf", "-", ".")
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	member := func(name string, content []byte) {
		fmt.Fprintf(&b, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", name, 0, 0, 0, "100644", len(content))
		b.Write(content)
		if len(content)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	member("debian-binary", []byte("2.0\n"))
	member("control.tar.gz", gzipped(""))
	member("data.tar.gz", data)
	write(t, path, b.Bytes())
}

// makeRPM builds an .rpm by hand: a lead, two empty headers and a gzipped
// cpio payload.
func makeRPM(t *testing.T, tree, path string) {
	t.Helper()
	files := sh(t, tree, "find", ".")
	cmd := exec.Command("cpio", "-o", "-H", "newc", "--quiet")
	cmd.Dir = tree
	cmd.Stdin = bytes.NewReader(files)
	payload, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	lead := make([]byte, 96)
	copy(lead, []byte{0xed, 0xab, 0xee, 0xdb, 3, 0})
	b.Write(lead)
	for i := 0; i < 2; i++ {
		b.Write([]byte{0x8e, 0xad, 0xe8, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	}
	b.Write(gzipped(string(payload)))
	write(t, path, b.Bytes())
}

func TestPackageFromFile(t *testing.T) {
	cases := map[string]repo.Package{
		"hello_2.10-5ubuntu1_amd64.deb":           {Name: "hello", Version: "2.10-5ubuntu1", Arch: "amd64"},
		"cowsay_1%3a3.03-8_all.deb":               {Name: "cowsay", Version: "1:3.03-8", Arch: "all"},
		"man-db-2.13.1-1-x86_64.pkg.tar.zst":      {Name: "man-db", Version: "2.13.1-1", Arch: "x86_64"},
		"python-foo-bar-1.0-2-any.pkg.tar.xz":     {Name: "python-foo-bar", Version: "1.0-2", Arch: "any"},
		"coreutils-9.5-6.fc41.x86_64.rpm":         {Name: "coreutils", Version: "9.5-6.fc41", Arch: "x86_64"},
		"perl-File-Temp-0.231.100-512.noarch.rpm": {Name: "perl-File-Temp", Version: "0.231.100-512", Arch: "noarch"},
	}
	for file, want := range cases {
		if got := PackageFromFile(file); got != want {
			t.Errorf("%s: %+v, want %+v", file, got, want)
		}
	}
	if PackageKind("foo.src.rpm") != "" || PackageKind("notes.txt") != "" {
		t.Error("source rpms and other files aren't packages")
	}
}

func TestIngestPackages(t *testing.T) {
	needTools(t, "tar", "cpio", "find")
	tree := packageTree(t)
	pkgs := t.TempDir()
	deb := filepath.Join(pkgs, "hello_2.10-1_amd64.deb")
	arch := filepath.Join(pkgs, "hello-2.10-1-x86_64.pkg.tar.gz")
	rpm := filepath.Join(pkgs, "hello-2.10-1.fc41.x86_64.rpm")
	makeDeb(t, tree, deb)
	sh(t, tree, "tar", "-czf", arch, "usr")
	makeRPM(t, tree, rpm)

	s, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, c := range []struct{ file, distro string }{{deb, "debian"}, {arch, "arch"}, {rpm, "fedora"}} {
		st, err := Paths(ctx, s, []string{c.file}, Options{Package: repo.Package{Distro: c.distro}, Quiet: true, Log: io.Discard})
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if st.Pages != 4 || st.Skipped != 0 {
			t.Errorf("%s: %+v", c.file, st)
		}
	}
	pages, err := s.AllPages()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range pages {
		names = append(names, p.Pointer.Name+"("+p.Pointer.Section+")")
	}
	if want := []string{"hello(1)", "hey(1)", "hi(1)", "hello.conf(5)"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("pages = %v, want %v", names, want)
	}
	// One source, so one version, found in all three packages.
	src := pages[0].Current().Source
	for _, p := range pages {
		v := p.Current()
		if len(p.Listing.Versions) != 1 || v.Source != src {
			t.Errorf("%s: %d versions, source %s", p.Pointer.Name, len(p.Listing.Versions), v.Source)
		}
		var distros []string
		for _, pk := range v.Packages {
			distros = append(distros, pk.Distro+"/"+pk.Name+"/"+pk.Version)
		}
		sort.Strings(distros)
		if want := []string{"arch/hello/2.10-1", "debian/hello/2.10-1", "fedora/hello/2.10-1.fc41"}; !reflect.DeepEqual(distros, want) {
			t.Errorf("%s: packages %v", p.Pointer.Name, distros)
		}
		if v.Title != "HELLO(1)" || v.Description != "say hello" {
			t.Errorf("%s: %q %q", p.Pointer.Name, v.Title, v.Description)
		}
	}
	man, err := s.Variant(src, repo.VariantMan)
	if err != nil || !strings.Contains(man.Text(), "say hello") {
		t.Fatalf("man variant: %v", err)
	}
}

func TestIngestAndGirlify(t *testing.T) {
	needTools(t)
	root := filepath.Join(t.TempDir(), "man")
	write(t, filepath.Join(root, "man1", "foo.1"), []byte(roff("foo", "1", "does foo")))
	write(t, filepath.Join(root, "man1", "bar.1"), []byte(roff("bar", "1", "does bar")))
	write(t, filepath.Join(root, "man3", "broken.3"), []byte(".so man3/nowhere.3\n"))
	s, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// foo is already girlified in the local cache.
	text, err := renderText(ctx, filepath.Join(root, "man1", "foo.1"))
	if err != nil {
		t.Fatal(err)
	}
	cdir := cache.Dir(t.TempDir())
	if err := cdir.PutObject(cache.Hash(text), "# NAME\n\nfoo from the cache 💅\n"); err != nil {
		t.Fatal(err)
	}
	cdir.PutEntry(&cache.Entry{Source: "/x/foo.1", Hash: cache.Hash(text), Backend: "openai", Model: "cached-model"})

	st, err := Paths(ctx, s, []string{root}, Options{Cache: cache.Set{cdir}, Quiet: true, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if st.Pages != 3 || st.New != 2 || st.FromCache != 1 || st.Skipped != 1 {
		t.Fatalf("ingest: %+v", st)
	}
	l, _, _ := s.Listing("foo", "1")
	g, err := s.Variant(l.Current, repo.VariantGirl)
	if err != nil || !strings.Contains(g.Text(), "foo from the cache") || g.Generator.Model != "cached-model" {
		t.Fatalf("cached girl variant: %+v, %v", g, err)
	}

	b := &fake{}
	gst, err := Girlify(ctx, s, GirlifyOptions{Backend: b, Jobs: 2, Quiet: true, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if gst.Converted != 1 || b.calls.Load() != 1 {
		t.Fatalf("girlify: %+v, %d calls", gst, b.calls.Load())
	}
	l, _, _ = s.Listing("bar", "1")
	v := l.CurrentVersion()
	if _, ok := v.Variants[repo.VariantGirl]; !ok {
		t.Fatal("bar has no girl variant in its listing")
	}
	g, err = s.Variant(v.Source, repo.VariantGirl)
	if err != nil || !strings.Contains(g.Text(), "-v") || g.Generator.Model != "fake-1" || g.Title != "BAR(1)" {
		t.Fatalf("girlified bar: %+v, %v", g, err)
	}
	// Nothing left to do.
	if gst, _ := Girlify(ctx, s, GirlifyOptions{Backend: b, Quiet: true, Log: io.Discard}); gst.Converted != 0 {
		t.Errorf("second girlify: %+v", gst)
	}

	// A new version of foo becomes current; the old one stays listed.
	time.Sleep(1100 * time.Millisecond) // versions are stamped to the second
	write(t, filepath.Join(root, "man1", "foo.1"), []byte(roff("foo", "1", "does foo, now faster")))
	if _, err := Paths(ctx, s, []string{root}, Options{Quiet: true, Log: io.Discard}); err != nil {
		t.Fatal(err)
	}
	l, _, _ = s.Listing("foo", "1")
	if len(l.Versions) != 2 || l.CurrentVersion().Description != "does foo, now faster" || l.Versions[0].Source != l.Current {
		t.Fatalf("after update: %+v", l)
	}
	// Re-adding the old one with KeepCurrent doesn't change which is current.
	write(t, filepath.Join(root, "man1", "foo.1"), []byte(roff("foo", "1", "does foo")))
	if _, err := Paths(ctx, s, []string{root}, Options{KeepCurrent: true, Quiet: true, Log: io.Discard}); err != nil {
		t.Fatal(err)
	}
	l, _, _ = s.Listing("foo", "1")
	if l.CurrentVersion().Description != "does foo, now faster" {
		t.Errorf("KeepCurrent changed the current version: %+v", l)
	}
}

// renderText renders a page the way ingest does.
func renderText(ctx context.Context, path string) (string, error) {
	src, err := manpage.Source(path, nil)
	if err != nil {
		return "", err
	}
	return manpage.SourceText(ctx, src, filepath.Base(path), manpage.Options{})
}

func TestGirlifyBatch(t *testing.T) {
	needTools(t)
	srv := llmtest.NewBatchServer()
	defer srv.Close()
	srv.Reply = llmtest.EchoOptions("batched")
	root := filepath.Join(t.TempDir(), "man")
	write(t, filepath.Join(root, "man1", "foo.1"), []byte(roff("foo", "1", "does foo")))
	write(t, filepath.Join(root, "man1", "bar.1"), []byte(roff("bar", "1", "does bar")))
	s, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := Paths(ctx, s, []string{root}, Options{Quiet: true, Log: io.Discard}); err != nil {
		t.Fatal(err)
	}
	o := GirlifyOptions{Quiet: true, Log: io.Discard, NoWait: true,
		Batch: &llm.Batcher{Client: &llm.BatchClient{URL: srv.BatchURL(), Model: "cheap-model"},
			Dir: t.TempDir(), Poll: 10 * time.Millisecond}}
	st, err := Girlify(ctx, s, o)
	if err != nil || st.Batched != 2 || srv.Batches() != 1 {
		t.Fatalf("first: %+v, %v", st, err)
	}
	st, err = Girlify(ctx, s, o) // still running
	if err != nil || st.Pending != 2 || srv.Batches() != 1 {
		t.Fatalf("second: %+v, %v", st, err)
	}
	st, err = Girlify(ctx, s, o)
	if err != nil || st.Converted != 2 || srv.Batches() != 1 {
		t.Fatalf("third: %+v, %v", st, err)
	}
	l, _, _ := s.Listing("foo", "1")
	if _, ok := l.CurrentVersion().Variants[repo.VariantGirl]; !ok {
		t.Fatal("foo's listing has no girl variant")
	}
	g, err := s.Variant(l.Current, repo.VariantGirl)
	if err != nil || !strings.Contains(g.Text(), "batched") || g.Generator.Model != "cheap-model" || g.Title != "FOO(1)" {
		t.Fatalf("foo: %+v, %v", g, err)
	}
}
