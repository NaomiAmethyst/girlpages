// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package manpage finds man pages and renders them to plain text using the
// system's man(1), so girl resolves names exactly the way man does.
package manpage

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Page is one man page source file.
type Page struct {
	Path    string // absolute path to the source (possibly compressed)
	Name    string
	Section string
}

// Title is how man pages are conventionally referred to, e.g. "ls(1)".
func (p Page) Title() string {
	if p.Section == "" {
		return p.Name
	}
	return p.Name + "(" + p.Section + ")"
}

var compressionExts = []string{".gz", ".bz2", ".xz", ".zst", ".lzma", ".lz", ".Z", ".z"}

// ParseFilename splits a man page file name such as "printf.3p.gz" into its
// page name and section.
func ParseFilename(path string) (name, section string) {
	base := filepath.Base(path)
	for _, ext := range compressionExts {
		if strings.HasSuffix(base, ext) {
			base = strings.TrimSuffix(base, ext)
			break
		}
	}
	i := strings.LastIndexByte(base, '.')
	if i <= 0 || i == len(base)-1 {
		return base, ""
	}
	return base[:i], base[i+1:]
}

// NewPage builds a Page for a source path.
func NewPage(path string) (Page, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Page{}, err
	}
	name, sec := ParseFilename(abs)
	return Page{Path: abs, Name: name, Section: sec}, nil
}

// Options controls how man(1) is invoked.
type Options struct {
	ManPath  string   // -M, exported as $MANPATH
	Locale   string   // -L
	Sections string   // -S, colon or comma separated
	All      bool     // -a
	Extra    []string // anything else to hand to man -w
	// Stderr receives man's own complaints from Locate ("No manual entry
	// for …"); nil means os.Stderr.
	Stderr io.Writer
}

func (o Options) env() []string {
	env := os.Environ()
	if o.ManPath != "" {
		env = append(env, "MANPATH="+o.ManPath)
	}
	return env
}

// IsSection reports whether a command-line word looks like a manual
// section, the same test man uses to decide that "girl 3 printf" means
// section 3.
func IsSection(s string) bool {
	return sectionRE.MatchString(s)
}

var sectionRE = regexp.MustCompile(`^([0-9][a-zA-Z0-9+]*|n|l|p|o|x)$`)

// Locate asks man -w where the requested pages live. Errors for pages that
// don't exist are written by man straight to stderr, like man itself does;
// the returned bool reports whether any were missing.
func Locate(ctx context.Context, args []string, o Options) ([]Page, bool, error) {
	cmdArgs := []string{"-w"}
	if o.All {
		cmdArgs = append(cmdArgs, "-a")
	}
	if o.Locale != "" {
		cmdArgs = append(cmdArgs, "-L", o.Locale)
	}
	if o.Sections != "" {
		cmdArgs = append(cmdArgs, "-S", o.Sections)
	}
	cmdArgs = append(cmdArgs, o.Extra...)
	cmdArgs = append(cmdArgs, "--")
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.CommandContext(ctx, "man", cmdArgs...)
	cmd.Env = o.env()
	cmd.Stderr = o.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	out, err := cmd.Output()
	missing := false
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return nil, false, fmt.Errorf("running man: %w", err)
		}
		missing = true
	}
	var pages []Page
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		p, err := NewPage(line)
		if err != nil {
			return nil, missing, err
		}
		pages = append(pages, p)
	}
	return pages, missing, nil
}

// Width is the line width pages are rendered at before being sent to the
// model. Keeping it fixed keeps content hashes stable across terminals.
const Width = 80

// Text renders a man page source file to plain text.
func Text(ctx context.Context, path string, o Options) (string, error) {
	cmd := exec.CommandContext(ctx, "man", "--no-hyphenation", "--no-justification", "-l", path)
	env := append(o.env(),
		fmt.Sprintf("MANWIDTH=%d", Width),
		"MANPAGER=cat", "PAGER=cat", "MANOPT=", "MAN_KEEP_FORMATTING=",
		"GROFF_NO_SGR=1",
	)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// The first line is the useful one ("can't open …"); the rest is
		// the formatter pipeline falling over.
		msg, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("rendering %s: %s", path, msg)
	}
	text := CleanText(string(out))
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("rendering %s: man produced no output", path)
	}
	return text, nil
}

var titleRE = regexp.MustCompile(`^\S+\([^)\s]+\)$`)

// HeaderTitle pulls the page's own title, such as "TRUE(1)", from the first
// line of rendered text. It can differ from the file name: Debian ships
// true(1) as gnutrue.1.gz, for instance.
func HeaderTitle(text string) string {
	first, _, _ := strings.Cut(strings.TrimLeft(text, "\n"), "\n")
	fields := strings.Fields(first)
	if len(fields) >= 2 && fields[0] == fields[len(fields)-1] && titleRE.MatchString(fields[0]) {
		return fields[0]
	}
	return ""
}

var sgrRE = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// CleanText strips any overstrike or escape-sequence formatting that slipped
// through and trims trailing whitespace.
func CleanText(s string) string {
	s = sgrRE.ReplaceAllString(s, "")
	if strings.ContainsRune(s, '\b') {
		var b strings.Builder
		rs := []rune(s)
		for i := 0; i < len(rs); i++ {
			if i+1 < len(rs) && rs[i+1] == '\b' {
				i++ // drop the char and the backspace; keep what follows
				continue
			}
			b.WriteRune(rs[i])
		}
		s = b.String()
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n") + "\n"
}

// ManPath returns the directories man searches, in order.
func ManPath(ctx context.Context, o Options) ([]string, error) {
	cmd := exec.CommandContext(ctx, "manpath", "-q")
	cmd.Env = o.env()
	out, err := cmd.Output()
	if err != nil {
		if mp := os.Getenv("MANPATH"); o.ManPath == "" && mp != "" {
			return filepath.SplitList(mp), nil
		}
		return nil, fmt.Errorf("running manpath: %w", err)
	}
	var dirs []string
	for _, d := range filepath.SplitList(strings.TrimSpace(string(out))) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
}

// SectionCount is how many places SectionRank has.
const SectionCount = 13

// sectionOrder is man-db's default search order; Enumerate sorts by it so
// bulk runs do the most useful sections first.
var sectionOrder = []string{"1", "n", "l", "8", "3", "0", "2", "5", "4", "9", "6", "7"}

// Chapter is the part of the manual a section belongs to: for man's
// numbered sections, the number (3 for 3, 3p and 3ssl); any other section
// (n, l, or a series of documents such as rfc) is its own.
func Chapter(sec string) string {
	if sec != "" && sec[0] >= '0' && sec[0] <= '9' {
		return sec[:1]
	}
	return sec
}

// ListLess orders page names as listings do: ignoring case (then by it),
// numbers by value.
func ListLess(a, b string) bool {
	if la, lb := strings.ToLower(a), strings.ToLower(b); la != lb {
		return NameLess(la, lb)
	}
	return a < b
}

// ListingLess orders a section listing's pages, as the website's listings
// (and the repository's, for the app) have them: by name ignoring case,
// numbers by value, then by section.
func ListingLess(nameA, secA, nameB, secB string) bool {
	if a, b := strings.ToLower(nameA), strings.ToLower(nameB); a != b {
		return NameLess(a, b)
	}
	return secA < secB
}

// Letter is the group a name is listed under: its first letter, or for a
// number (an RFC's), its thousand.
func Letter(name string) string {
	if n, err := strconv.Atoi(name); err == nil && n >= 0 {
		if n < 1000 {
			return "1–999"
		}
		return fmt.Sprintf("%ds", n/1000*1000)
	}
	if r := []rune(name); len(r) > 0 && unicode.IsLetter(r[0]) && r[0] < 0x80 {
		return strings.ToUpper(string(r[0]))
	}
	return "#"
}

// NameLess orders page names: numbers (as series of documents have) first,
// by value (fewer leading zeros first), then anything else as text.
func NameLess(a, b string) bool {
	na, nb := isNumber(a), isNumber(b)
	switch {
	case na != nb:
		// Numbers first, so it's an order (by value or as text alone, 10
		// < 1z < 9 < 10 goes round).
		return na
	case na:
		ta, tb := strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(ta) != len(tb) {
			return len(ta) < len(tb)
		}
		if ta != tb {
			return ta < tb
		}
		return len(a) < len(b)
	}
	return a < b
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// IsSeries reports whether a section is a series of documents with
// numbers for names (rfc), rather than a section of the manual: it doesn't
// start with a digit and is longer than a letter.
func IsSeries(sec string) bool {
	return len(sec) > 1 && (sec[0] < '0' || sec[0] > '9')
}

// SectionRank is where a section comes in man's search order.
func SectionRank(sec string) int { return sectionRank(sec) }

func sectionRank(sec string) int {
	for i, s := range sectionOrder {
		if strings.HasPrefix(sec, s) {
			return i
		}
	}
	return len(sectionOrder)
}

// SectionMatches reports whether sec is selected by a list of wanted
// sections. "3" selects "3", "3p", "3ssl" and so on, as with man -S.
func SectionMatches(sec string, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, w := range wanted {
		if w != "" && strings.HasPrefix(sec, w) {
			return true
		}
	}
	return false
}

// Enumerate lists every man page in the given directories (the top-level
// manN directories only; translations are skipped). Where the same name and
// section appear in several directories the first wins, as with man.
func Enumerate(dirs []string, sections []string) ([]Page, error) {
	seen := map[string]bool{}
	var pages []Page
	for _, root := range dirs {
		subs, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, sub := range subs {
			if !strings.HasPrefix(sub.Name(), "man") || len(sub.Name()) < 4 {
				continue
			}
			dir := filepath.Join(root, sub.Name())
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
				continue
			}
			files, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, f := range files {
				path := filepath.Join(dir, f.Name())
				fi, err := os.Stat(path) // follows symlinks; drops dangling ones
				if err != nil || !fi.Mode().IsRegular() {
					continue
				}
				name, sec := ParseFilename(path)
				if sec == "" {
					sec = strings.TrimPrefix(sub.Name(), "man")
				}
				if !SectionMatches(sec, sections) {
					continue
				}
				key := name + "\x00" + sec
				if seen[key] {
					continue
				}
				seen[key] = true
				abs, err := filepath.Abs(path)
				if err != nil {
					return nil, err
				}
				pages = append(pages, Page{Path: abs, Name: name, Section: sec})
			}
		}
	}
	sort.SliceStable(pages, func(i, j int) bool {
		ri, rj := sectionRank(pages[i].Section), sectionRank(pages[j].Section)
		if ri != rj {
			return ri < rj
		}
		if pages[i].Section != pages[j].Section {
			return pages[i].Section < pages[j].Section
		}
		return pages[i].Name < pages[j].Name
	})
	return pages, nil
}

var whatisRE = regexp.MustCompile(`^(\S+) \(([^)]+)\)\s+- (.*)$`)

// Descriptions returns the one-line description of every page in the
// whatis database (via man -k), keyed by DescKey.
func Descriptions(ctx context.Context, o Options) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "man", "-k", ".")
	cmd.Env = o.env()
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("running man -k: %w", err)
	}
	descs := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if m := whatisRE.FindStringSubmatch(sc.Text()); m != nil {
			descs[DescKey(m[1], m[2])] = strings.TrimSpace(m[3])
		}
	}
	return descs, nil
}

// DescKey is the map key Descriptions uses for a page.
func DescKey(name, section string) string { return name + "\x00" + section }
