// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package crawl

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/NaomiAmethyst/girlpages/internal/ingest"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

func roff(name, desc string) string {
	return ".TH " + strings.ToUpper(name) + " 1 2026-01-01 test\n.SH NAME\n" + name + " \\- " + desc + "\n"
}

func gz(b []byte) []byte {
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	w.Write(b)
	w.Close()
	return out.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// tarball makes a gzipped tar of files (path → content).
func tarball(files map[string][]byte) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg})
		tw.Write(files[n])
	}
	tw.Close()
	return gz(buf.Bytes())
}

// deb makes a .deb holding a man page.
func deb(name, desc string) []byte {
	data := tarball(map[string][]byte{"./usr/share/man/man1/" + name + ".1.gz": gz([]byte(roff(name, desc)))})
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	for _, m := range []struct {
		name string
		data []byte
	}{{"debian-binary", []byte("2.0\n")}, {"control.tar.gz", gz(nil)}, {"data.tar.gz", data}} {
		fmt.Fprintf(&b, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", m.name, 0, 0, 0, "100644", len(m.data))
		b.Write(m.data)
		if len(m.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

// rpmFile makes an .rpm holding a man page (cpio is needed).
func rpmFile(t *testing.T, name, desc string) []byte {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "usr", "share", "man", "man1", name+".1.gz")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, gz([]byte(roff(name, desc))), 0o644)
	cmd := exec.Command("cpio", "-o", "-H", "newc", "--quiet")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("./usr/share/man/man1/" + name + ".1.gz\n")
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
	b.Write(gz(payload))
	return b.Bytes()
}

// server serves files and records what was asked for.
type server struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	got   []string
}

func serve(files map[string][]byte) *server {
	s := &server{files: files}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.got = append(s.got, r.URL.Path)
		b, ok := s.files[r.URL.Path]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	return s
}

func (s *server) fetched(substr string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, g := range s.got {
		if strings.Contains(g, substr) {
			n++
		}
	}
	return n
}

func needTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range append([]string{"man", "tar"}, tools...) {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
}

func crawlOnce(t *testing.T, s *repo.Store, r Repository, state string) Stats {
	t.Helper()
	st, err := Run(context.Background(), s, r, Options{Jobs: 2, Group: 2, State: state, Log: io.Discard, Quiet: true,
		Ingest: ingest.Options{Log: io.Discard}})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestApt(t *testing.T) {
	needTools(t)
	old, cur, other := deb("hello", "say hello"), deb("hello", "say hello, newer"), deb("nope", "x")
	contents := "FILE                                LOCATION\n" +
		"usr/share/man/man1/hello.1.gz       utils/hello\n" +
		"usr/share/man/de/man1/nope.1.gz     utils/nope\n" + // a translation only
		"usr/bin/nope                        utils/nope\n"
	packages := ""
	for _, p := range []struct {
		name, ver string
		b         []byte
	}{{"hello", "1.0-1", old}, {"hello", "1.0-2", cur}, {"nope", "1", other}} {
		packages += fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: amd64\nDescription: x\n continued\n"+
			"Filename: pool/main/%s_%s_amd64.deb\nSize: %d\nSHA256: %s\n\n", p.name, p.ver, p.name, p.ver, len(p.b), sum(p.b))
	}
	srv := serve(map[string][]byte{
		"/debian/dists/stable/InRelease":                     []byte("-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\nOrigin: Debian\nCodename: trixie-security\nComponents: main\n"),
		"/debian/dists/stable/main/Contents-amd64.gz":        gz([]byte(contents)),
		"/debian/dists/stable/main/binary-amd64/Packages.gz": gz([]byte(packages)),
		"/debian/pool/main/hello_1.0-1_amd64.deb":            old,
		"/debian/pool/main/hello_1.0-2_amd64.deb":            cur,
		"/debian/pool/main/nope_1_amd64.deb":                 other,
	})
	defer srv.Close()
	s, _ := repo.Open(t.TempDir())
	state := filepath.Join(t.TempDir(), "state.json")
	r := &Apt{URL: srv.URL + "/debian", Suite: "stable"}
	st := crawlOnce(t, s, r, state)
	if st.Total != 3 || st.WithMan != 1 || st.Fetched != 1 || st.Pages.New != 1 {
		t.Fatalf("crawl: %+v", st)
	}
	if srv.fetched("nope_") != 0 || srv.fetched("hello_1.0-1") != 0 {
		t.Errorf("fetched packages it didn't need: %v", srv.got)
	}
	l, _, _ := s.Listing("hello", "1")
	v := l.CurrentVersion()
	if v.Description != "say hello, newer" || len(v.Packages) != 1 ||
		v.Packages[0] != (repo.Package{Distro: "debian", Release: "trixie", Name: "hello", Version: "1.0-2", Arch: "amd64"}) {
		t.Errorf("hello: %+v", v)
	}
	// Again: nothing to fetch.
	if st := crawlOnce(t, s, r, state); st.Fetched != 0 || st.Done != 1 || srv.fetched("hello_1.0-2") != 1 {
		t.Errorf("second crawl: %+v", st)
	}
}

func TestAptBadChecksum(t *testing.T) {
	needTools(t)
	d := deb("hello", "say hello")
	packages := fmt.Sprintf("Package: hello\nVersion: 1\nArchitecture: all\nFilename: pool/hello.deb\nSize: %d\nSHA256: %s\n",
		len(d), strings.Repeat("0", 64))
	srv := serve(map[string][]byte{
		"/d/dists/s/Release":                    []byte("Origin: Test\nComponents: main\n"),
		"/d/dists/s/Contents-amd64":             []byte("usr/share/man/man1/hello.1.gz admin/hello\n"),
		"/d/dists/s/main/binary-amd64/Packages": []byte(packages),
		"/d/pool/hello.deb":                     d,
	})
	defer srv.Close()
	s, _ := repo.Open(t.TempDir())
	st, err := Run(context.Background(), s, &Apt{URL: srv.URL + "/d", Suite: "s"}, Options{Log: io.Discard, Quiet: true})
	if err != nil || st.Failed != 1 || st.Fetched != 0 {
		t.Errorf("bad checksum: %+v, %v", st, err)
	}
}

func TestRPM(t *testing.T) {
	needTools(t, "cpio")
	type pkg struct {
		name, ver, rel, epoch string
		b                     []byte
		man                   bool
	}
	pkgs := []pkg{
		{"which", "2.21", "30.el9", "0", rpmFile(t, "which", "shows the full path"), true},
		{"tar", "1.34", "11.el9", "2", rpmFile(t, "tar", "old"), true},
		{"tar", "1.34", "13.el9_8", "2", rpmFile(t, "tar", "archives"), true},
		{"nomen", "1", "1", "0", []byte("x"), false},
	}
	primary := `<?xml version="1.0" encoding="UTF-8"?><metadata xmlns="http://linux.duke.edu/metadata/common" packages="4">`
	filelists := `<?xml version="1.0" encoding="UTF-8"?><filelists xmlns="http://linux.duke.edu/metadata/filelists" packages="4">`
	files := map[string][]byte{}
	for _, p := range pkgs {
		id := sum(p.b)
		href := fmt.Sprintf("Packages/%s-%s-%s.x86_64.rpm", p.name, p.ver, p.rel)
		files["/os/"+href] = p.b
		primary += fmt.Sprintf(`<package type="rpm"><name>%s</name><arch>x86_64</arch><version epoch="%s" ver="%s" rel="%s"/>`+
			`<checksum type="sha256" pkgid="YES">%s</checksum><size package="%d"/><location href="%s"/></package>`,
			p.name, p.epoch, p.ver, p.rel, id, len(p.b), href)
		file := "/usr/bin/" + p.name
		if p.man {
			file = "/usr/share/man/man1/" + p.name + ".1.gz"
		}
		filelists += fmt.Sprintf(`<package pkgid="%s" name="%s" arch="x86_64"><version epoch="%s" ver="%s" rel="%s"/><file>%s</file></package>`,
			id, p.name, p.epoch, p.ver, p.rel, file)
	}
	files["/os/repodata/repomd.xml"] = []byte(`<?xml version="1.0"?><repomd xmlns="http://linux.duke.edu/metadata/repo">` +
		`<data type="primary"><location href="repodata/abc-primary.xml.gz"/></data>` +
		`<data type="filelists"><location href="repodata/def-filelists.xml.gz"/></data></repomd>`)
	files["/os/repodata/abc-primary.xml.gz"] = gz([]byte(primary + "</metadata>"))
	files["/os/repodata/def-filelists.xml.gz"] = gz([]byte(filelists + "</filelists>"))
	srv := serve(files)
	defer srv.Close()
	s, _ := repo.Open(t.TempDir())
	st, err := Run(context.Background(), s, &RPM{URL: srv.URL + "/os"}, Options{Distro: "rocky", Release: "9", Log: io.Discard,
		Quiet: true, Ingest: ingest.Options{Log: io.Discard}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 4 || st.WithMan != 2 || st.Fetched != 2 {
		t.Fatalf("crawl: %+v", st)
	}
	if srv.fetched("tar-1.34-11") != 0 || srv.fetched("nomen") != 0 {
		t.Errorf("fetched packages it didn't need: %v", srv.got)
	}
	l, _, _ := s.Listing("tar", "1")
	if v := l.CurrentVersion(); v.Description != "archives" || v.Packages[0].Version != "2:1.34-13.el9_8" || v.Packages[0].Distro != "rocky" {
		t.Errorf("tar: %+v", v)
	}
}

func TestPacman(t *testing.T) {
	needTools(t)
	pkg := tarball(map[string][]byte{".PKGINFO": []byte("pkgname = which\n"),
		"usr/share/man/man1/which.1.gz": gz([]byte(roff("which", "shows the full path")))})
	db := tarball(map[string][]byte{
		"which-2.21-6/desc": []byte(fmt.Sprintf("%%FILENAME%%\nwhich-2.21-6-x86_64.pkg.tar.gz\n\n%%NAME%%\nwhich\n\n%%VERSION%%\n2.21-6\n\n"+
			"%%ARCH%%\nx86_64\n\n%%CSIZE%%\n%d\n\n%%SHA256SUM%%\n%s\n\n", len(pkg), sum(pkg))),
		"which-2.21-6/files": []byte("%FILES%\nusr/\nusr/bin/which\nusr/share/man/man1/which.1.gz\n\n"),
		"zlib-1-1/desc":      []byte("%FILENAME%\nzlib-1-1-x86_64.pkg.tar.gz\n\n%NAME%\nzlib\n\n%VERSION%\n1-1\n"),
		"zlib-1-1/files":     []byte("%FILES%\nusr/lib/libz.so\n"),
	})
	srv := serve(map[string][]byte{"/core/os/x86_64/core.files": db, "/core/os/x86_64/which-2.21-6-x86_64.pkg.tar.gz": pkg})
	defer srv.Close()
	s, _ := repo.Open(t.TempDir())
	st := crawlOnce(t, s, &Pacman{URL: srv.URL + "/core/os/x86_64"}, "")
	if st.Total != 2 || st.WithMan != 1 || st.Fetched != 1 || st.Pages.New != 1 {
		t.Fatalf("crawl: %+v", st)
	}
	l, _, _ := s.Listing("which", "1")
	if v := l.CurrentVersion(); v.Packages[0] != (repo.Package{Distro: "arch", Release: "rolling", Name: "which", Version: "2.21-6", Arch: "x86_64"}) {
		t.Errorf("which: %+v", v.Packages)
	}
}

func TestDryRunAndMatch(t *testing.T) {
	db := tarball(map[string][]byte{
		"a-1-1/desc":  []byte("%NAME%\na\n\n%VERSION%\n1-1\n\n%FILENAME%\na.pkg.tar.gz\n\n%CSIZE%\n100\n"),
		"a-1-1/files": []byte("%FILES%\nusr/share/man/man1/a.1.gz\n"),
		"b-1-1/desc":  []byte("%NAME%\nb\n\n%VERSION%\n1-1\n\n%FILENAME%\nb.pkg.tar.gz\n\n%CSIZE%\n200\n"),
		"b-1-1/files": []byte("%FILES%\nusr/share/man/man8/b.8.gz\n"),
	})
	srv := serve(map[string][]byte{"/x/os/any/x.files": db})
	defer srv.Close()
	s, _ := repo.Open(t.TempDir())
	st, err := Run(context.Background(), s, &Pacman{URL: srv.URL + "/x/os/any"},
		Options{DryRun: true, Match: regexp.MustCompile("^b$"), Log: io.Discard})
	if err != nil || st.WithMan != 1 || st.Bytes != 200 || srv.fetched(".pkg.tar") != 0 {
		t.Errorf("dry run: %+v, %v", st, err)
	}
}

func TestIsManPath(t *testing.T) {
	for p, want := range map[string]bool{
		"usr/share/man/man1/ls.1.gz": true, "/usr/share/man/man3/printf.3.gz": true, "usr/share/man/man1/": false,
		"usr/share/man/de/man1/ls.1.gz": false, // translations
		"usr/share/doc/man/README":      false, "usr/share/man/man.conf": false,
	} {
		if got := IsManPath(p); got != want {
			t.Errorf("IsManPath(%q) = %v", p, got)
		}
	}
}
