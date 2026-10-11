// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package ingest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// IsGit reports whether an ingest argument names a git repository:
// git+https://…, git://…, ssh://…, git@host:… or anything ending in .git,
// with an optional #ref.
func IsGit(arg string) bool {
	u, _, _ := strings.Cut(arg, "#")
	return strings.HasPrefix(u, "git+") || strings.HasPrefix(u, "git://") ||
		strings.HasPrefix(u, "git@") || strings.HasSuffix(u, ".git")
}

var hexRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// CloneGit makes a shallow clone of a git repository, at ref if the spec
// ends in #ref (a branch, tag or commit), and returns it as a Source:
// with Loose set, as man pages in source trees can live anywhere. Call
// cleanup when done with it.
func CloneGit(ctx context.Context, spec string) (Source, func(), error) {
	u, ref, _ := strings.Cut(spec, "#")
	u = strings.TrimPrefix(u, "git+")
	if strings.HasPrefix(u, "-") {
		return Source{}, nil, fmt.Errorf("%q isn't a git URL", spec)
	}
	dir, err := os.MkdirTemp("", "girlrepo-git-")
	if err != nil {
		return Source{}, nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		// Never stop to ask for a password.
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %s", args[0], firstLine(stderr.String(), err))
		}
		return strings.TrimSpace(string(out)), nil
	}
	// init + fetch works for branches, tags and (on most hosts) commits.
	want := ref
	if want == "" {
		want = "HEAD"
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", "--", u},
		{"fetch", "--quiet", "--depth", "1", "origin", want},
		{"checkout", "--quiet", "FETCH_HEAD"},
	} {
		if _, err := git(args...); err != nil {
			cleanup()
			return Source{}, nil, fmt.Errorf("cloning %s: %w", u, err)
		}
	}
	commit, err := git("rev-parse", "--short=12", "HEAD")
	if err != nil {
		cleanup()
		return Source{}, nil, err
	}
	version := commit
	if ref != "" && !hexRE.MatchString(ref) {
		version = ref + " (" + commit + ")"
	}
	name := strings.TrimSuffix(filepath.Base(strings.TrimRight(u, "/")), ".git")
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		name = name[i+1:]
	}
	return Source{Path: dir, Loose: true, Package: repo.Package{Name: name, Version: version, URL: u}}, cleanup, nil
}

var (
	roffRE   = regexp.MustCompile(`(?m)^[.'][ \t]*(TH|Dt|so)[ \t]`)
	localeRE = regexp.MustCompile(`^[a-z]{2}([_-][A-Za-z]{2,4})?(\.[A-Za-z0-9-]+)?$`)
)

// skipDirs are never looked in.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".hg": true, ".svn": true}

// scanLoose finds roff man pages anywhere under top, by file name
// (name.section, perhaps compressed) and content (.TH or .Dt near the
// start). Translations, in directories named for a locale under a man or
// doc directory, are left out.
func scanLoose(top string, pkg repo.Package, sections []string) ([]candidate, []string, error) {
	var out []candidate
	seenRoot := map[string]bool{}
	var roots []string
	addRoot := func(d string) {
		if !seenRoot[d] {
			seenRoot[d] = true
			roots = append(roots, d)
		}
	}
	addRoot(top)
	err := filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != top && (skipDirs[d.Name()] || isTranslationDir(path)) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name, sec := manpage.ParseFilename(path)
		if sec == "" || name == "" || !manpage.IsSection(sec) || !manpage.SectionMatches(sec, sections) {
			return nil
		}
		if !looksLikeRoff(path) {
			return nil
		}
		dir := filepath.Dir(path)
		addRoot(dir)
		addRoot(filepath.Dir(dir))
		out = append(out, candidate{path: path, name: name, section: sec, pkg: pkg})
		return nil
	})
	return out, roots, err
}

func isTranslationDir(path string) bool {
	parent := strings.ToLower(filepath.Base(filepath.Dir(path)))
	return localeRE.MatchString(filepath.Base(path)) && (parent == "man" || parent == "doc" || parent == "docs" || parent == "po")
}

func looksLikeRoff(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head []byte
	if b := filepath.Base(path); strings.HasSuffix(b, ".gz") || strings.HasSuffix(b, ".bz2") ||
		strings.HasSuffix(b, ".xz") || strings.HasSuffix(b, ".zst") {
		if head, err = manpage.Decompress(io.LimitReader(f, 16<<20), path); err != nil {
			return false
		}
	} else {
		head = make([]byte, 64<<10)
		n, _ := io.ReadFull(f, head)
		head = head[:n]
	}
	return roffRE.Match(head)
}
