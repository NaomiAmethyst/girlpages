// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package ingest

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// OSRelease reads the distribution's ID and release from os-release.
func OSRelease(root string) (distro, release string) {
	for _, p := range []string{"etc/os-release", "usr/lib/os-release"} {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			continue
		}
		vals := map[string]string{}
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok {
				vals[k] = strings.Trim(v, `"'`)
			}
		}
		release = vals["VERSION_CODENAME"]
		if release == "" {
			release = vals["VERSION_ID"]
		}
		if release == "" && vals["BUILD_ID"] == "rolling" {
			release = "rolling"
		}
		return vals["ID"], release
	}
	return "", ""
}

// Owners maps installed files to the packages that own them, from
// whichever package database the system has (dpkg, pacman or rpm). Only
// files under a man directory are included.
func Owners() map[string]repo.Package {
	owners := map[string]repo.Package{}
	dpkgOwners(owners)
	pacmanOwners(owners)
	if len(owners) == 0 {
		rpmOwners(owners)
	}
	return owners
}

func isManFile(p string) bool { return strings.Contains(p, "/man/") }

func dpkgOwners(owners map[string]repo.Package) {
	out, err := exec.Command("dpkg-query", "-W", "-f", "${binary:Package}\t${Version}\t${Architecture}\n").Output()
	if err != nil {
		return
	}
	versions := map[string]repo.Package{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		name, _, _ := strings.Cut(f[0], ":")
		p := repo.Package{Name: name, Version: f[1], Arch: f[2]}
		versions[f[0]] = p
		versions[name] = p
	}
	lists, _ := filepath.Glob("/var/lib/dpkg/info/*.list")
	for _, l := range lists {
		pkg := strings.TrimSuffix(filepath.Base(l), ".list")
		p, ok := versions[pkg]
		if !ok {
			continue
		}
		b, err := os.ReadFile(l)
		if err != nil {
			continue
		}
		for _, f := range strings.Split(string(b), "\n") {
			if isManFile(f) {
				owners[f] = p
			}
		}
	}
}

func pacmanOwners(owners map[string]repo.Package) {
	dirs, _ := filepath.Glob("/var/lib/pacman/local/*/desc")
	for _, desc := range dirs {
		b, err := os.ReadFile(desc)
		if err != nil {
			continue
		}
		fields := pacmanFields(b)
		p := repo.Package{Name: first(fields["%NAME%"]), Version: first(fields["%VERSION%"]), Arch: first(fields["%ARCH%"])}
		fb, err := os.ReadFile(filepath.Join(filepath.Dir(desc), "files"))
		if err != nil {
			continue
		}
		for _, f := range pacmanFields(fb)["%FILES%"] {
			if isManFile("/" + f) {
				owners["/"+f] = p
			}
		}
	}
}

func pacmanFields(b []byte) map[string][]string {
	out := map[string][]string{}
	var key string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "%") && strings.HasSuffix(l, "%"):
			key = l
		case l == "":
			key = ""
		case key != "":
			out[key] = append(out[key], l)
		}
	}
	return out
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func rpmOwners(owners map[string]repo.Package) {
	out, err := exec.Command("rpm", "-qa", "--qf", `[%{FILENAMES}\t%{NAME}\t%{VERSION}-%{RELEASE}\t%{ARCH}\n]`).Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 4 && isManFile(f[0]) {
			owners[f[0]] = repo.Package{Name: f[1], Version: f[2], Arch: f[3]}
		}
	}
}
