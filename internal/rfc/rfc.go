// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package rfc reads RFCs: the RFC Editor's index of them, and their plain
// text, cleaned of what it has for paper (page breaks, running headers and
// footers) and cut into sections as a repository's text variant.
package rfc

import (
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// Section is the repository section RFCs go in.
const Section = "rfc"

// Indent and Width are where an RFC's text starts and the last column
// it's filled to, for laying it out again (render.HTMLOptions).
const (
	Indent = 3
	Width  = 72
)

// Entry is an RFC in the index.
type Entry struct {
	Number      int
	Title       string
	Month, Year string
	Status      string
	Formats     []string
	Obsoletes   []int
	ObsoletedBy []int
	Updates     []int
	UpdatedBy   []int
}

// Name is the RFC's name in the repository: its number.
func (e Entry) Name() string { return strconv.Itoa(e.Number) }

// HasText reports whether the RFC Editor has it as plain text.
func (e Entry) HasText() bool {
	for _, f := range e.Formats {
		if f == "TXT" || f == "ASCII" {
			return true
		}
	}
	return false
}

var months = map[string]string{"January": "01", "February": "02", "March": "03", "April": "04", "May": "05", "June": "06",
	"July": "07", "August": "08", "September": "09", "October": "10", "November": "11", "December": "12"}

// Date is when it was published, as 2022-06.
func (e Entry) Date() string {
	if m, ok := months[e.Month]; ok {
		return e.Year + "-" + m
	}
	return e.Year
}

// Package is where it comes from, for its listing.
func (e Entry) Package() repo.Package {
	return repo.Package{Distro: "ietf", Name: "rfc" + e.Name(), Version: e.Date()}
}

type xmlIDs struct {
	IDs []string `xml:"doc-id"`
}

type xmlEntry struct {
	DocID string `xml:"doc-id"`
	Title string `xml:"title"`
	Date  struct {
		Month string `xml:"month"`
		Year  string `xml:"year"`
	} `xml:"date"`
	Formats     []string `xml:"format>file-format"`
	Status      string   `xml:"current-status"`
	Obsoletes   xmlIDs   `xml:"obsoletes"`
	ObsoletedBy xmlIDs   `xml:"obsoleted-by"`
	Updates     xmlIDs   `xml:"updates"`
	UpdatedBy   xmlIDs   `xml:"updated-by"`
}

// numbers keeps the RFCs among doc-ids ("RFC0791"; not "BCP0014").
func numbers(ids xmlIDs) []int {
	var out []int
	for _, id := range ids.IDs {
		if n, ok := number(id); ok {
			out = append(out, n)
		}
	}
	return out
}

func number(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "RFC")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && n > 0
}

// ParseIndex reads rfc-index.xml.
func ParseIndex(r io.Reader) ([]Entry, error) {
	d := xml.NewDecoder(r)
	var out []Entry
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, fmt.Errorf("rfc-index.xml: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "rfc-entry" {
			continue
		}
		var x xmlEntry
		if err := d.DecodeElement(&x, &se); err != nil {
			return out, fmt.Errorf("rfc-index.xml: %w", err)
		}
		n, ok := number(x.DocID)
		if !ok {
			continue
		}
		out = append(out, Entry{Number: n, Title: strings.Join(strings.Fields(x.Title), " "),
			Month: x.Date.Month, Year: x.Date.Year, Status: x.Status, Formats: x.Formats,
			Obsoletes: numbers(x.Obsoletes), ObsoletedBy: numbers(x.ObsoletedBy),
			Updates: numbers(x.Updates), UpdatedBy: numbers(x.UpdatedBy)})
	}
}

// sectionName reports whether a line could be a running header's name
// for the section a page is in: a short title, not a sentence or code.
func sectionName(l string) bool {
	t := strings.TrimSpace(l)
	if t == "" || len(t) > 40 || strings.ContainsAny(t[len(t)-1:], ".,;:") {
		return false
	}
	return unicode.IsLetter([]rune(t)[0])
}

var (
	// fieldsRE separates a running header's fields.
	fieldsRE = regexp.MustCompile(`\s{2,}`)
	// A footer has the page number at the end of the line ("Postel
	// [Page 3]"), or on even pages of the oldest RFCs, at the start
	// ("[page 2]   Postel"), in any case, in roman numerals for a preface.
	footerRE = regexp.MustCompile(`(?i)\[page\s+[0-9ivxlc]+\]\s*$|^\s*\[page\s+[0-9ivxlc]+\](\s|$)`)
	digitsRE = regexp.MustCompile(`[0-9]+`)
)

// Clean strips what an RFC's text has for paper: a byte order mark,
// carriage returns, trailing spaces, and if it's paginated, page breaks
// and the running headers and footers around them (rejoining a paragraph
// that runs over a break). Leading and trailing blank lines go too.
func Clean(text string) string {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "")
	pages := strings.Split(text, "\f")
	var lines [][]string
	for _, p := range pages {
		ls := strings.Split(p, "\n")
		for i := range ls {
			ls[i] = strings.TrimRight(ls[i], " \t")
		}
		lines = append(lines, trimBlank(ls))
	}
	if len(lines) > 1 {
		// Running headers: lines that start more than one page, the same
		// but for numbers (dates, page numbers), and the order of their
		// fields (the oldest RFCs swap sides on even pages: "User Datagram
		// Protocol   RFC 768", then "RFC 768   User Datagram Protocol").
		norm := func(l string) string {
			fields := fieldsRE.Split(strings.TrimSpace(digitsRE.ReplaceAllString(l, "#")), -1)
			sort.Strings(fields)
			return strings.Join(fields, "|")
		}
		seen := map[string]int{}
		for _, ls := range lines[1:] {
			for _, l := range headerBlock(ls) {
				seen[norm(l)]++
			}
		}
		for i, ls := range lines {
			// A footer ends the page.
			if n := len(ls); n > 0 && footerRE.MatchString(ls[n-1]) {
				ls = trimBlank(ls[:n-1])
			}
			if i > 0 {
				// The header's lines go, as far as a blank line after them.
				header := false
				for len(ls) > 0 {
					if strings.TrimSpace(ls[0]) == "" {
						if header {
							break
						}
					} else if seen[norm(ls[0])] > 1 {
						header = true
					} else {
						break
					}
					ls = ls[1:]
				}
				// Some name the section the page is in under the header
				// (RFC 768's "Fields"): a title right under it, alone.
				if header && len(ls) > 1 && strings.TrimSpace(ls[0]) != "" && strings.TrimSpace(ls[1]) == "" && sectionName(ls[0]) {
					ls = ls[1:]
				}
			}
			lines[i] = trimBlank(ls)
		}
	}
	var out []string
	for _, ls := range lines {
		if len(ls) == 0 {
			continue
		}
		if n := len(out); n > 0 {
			// A new page goes on from the last, unless that ended a
			// paragraph: a sentence ended, or the indentation changes.
			last := out[n-1]
			if strings.HasSuffix(last, ".") || strings.HasSuffix(last, ":") || indent(last) != indent(ls[0]) {
				out = append(out, "")
			}
		}
		out = append(out, ls...)
	}
	return strings.Join(trimBlank(out), "\n") + "\n"
}

// headerBlock is where a page's running header can be: its first lines,
// up to three, as far as a blank line after them.
func headerBlock(ls []string) []string {
	var out []string
	for _, l := range ls {
		if strings.TrimSpace(l) == "" {
			if len(out) > 0 {
				break
			}
			continue
		}
		out = append(out, l)
		if len(out) == 3 {
			break
		}
	}
	return out
}

func trimBlank(ls []string) []string {
	for len(ls) > 0 && strings.TrimSpace(ls[0]) == "" {
		ls = ls[1:]
	}
	for len(ls) > 0 && strings.TrimSpace(ls[len(ls)-1]) == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}

func indent(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

var (
	numberedRE = regexp.MustCompile(`^([0-9]+)\.\s+(\S.*)$`)
	// Some number them without the dot (1 Introduction), at the margin.
	undottedRE = regexp.MustCompile(`^([0-9]+)\s+([A-Z].*)$`)
	subRE      = regexp.MustCompile(`^([0-9]+|[A-Z])\.[0-9]`)
	appendixRE = regexp.MustCompile(`^(Appendix|APPENDIX)\s+[A-Z]\b|^[A-Z]\.\s+\S`)
	leadersRE  = regexp.MustCompile(`\.\s?\.\s?\.`)
)

// named are the unnumbered parts of an RFC, as headings (lower case).
var named = map[string]bool{}

func init() {
	for _, n := range []string{"abstract", "status of this memo", "copyright notice", "table of contents",
		"introduction", "acknowledgements", "acknowledgments", "acknowledgement", "acknowledgment", "contributors",
		"authors' addresses", "author's address", "authors’ addresses", "author’s address", "editors' addresses",
		"editor's address", "index", "references", "normative references", "informative references", "bibliography",
		"full copyright statement", "intellectual property", "intellectual property statement",
		"security considerations", "iana considerations", "privacy considerations", "glossary", "terminology",
		"requirements language", "conventions", "conventions used in this document", "notational conventions",
		"acknowledgements and contributors", "contributors and acknowledgements", "preface", "foreword",
		"overview", "summary", "conclusion", "conclusions", "appendix", "appendices", "acronyms", "definitions"} {
		named[n] = true
	}
}

// isHeading reports whether line i starts a top-level section: at the
// margin, after a blank line, and numbered ("4.  Identifiers", but not
// "4.1.  …"), an appendix, or one of the named parts ("Abstract",
// "Authors' Addresses"). Lines of a table of contents (with dot leaders)
// aren't, nor other words at the margin (old RFCs' glossaries).
func isHeading(lines []string, i int) bool {
	l := lines[i]
	if l == "" || (i > 0 && strings.TrimSpace(lines[i-1]) != "") || leadersRE.MatchString(l) {
		return false
	}
	margin := l[0] != ' '
	if !margin {
		// Old RFCs centre them, alone between blank lines.
		if indent(l) < 10 || (i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "") {
			return false
		}
		l = strings.TrimSpace(l)
	}
	if subRE.MatchString(l) {
		return false
	}
	// A heading is a title, not a definition run on after it ("1. MUST
	// This word…").
	title := func(t string) bool { return len(t) < 72 && !strings.Contains(t, "   ") }
	if m := numberedRE.FindStringSubmatch(l); m != nil {
		return title(m[2])
	}
	if m := undottedRE.FindStringSubmatch(l); m != nil && margin {
		return title(m[2])
	}
	if appendixRE.MatchString(l) {
		return true
	}
	return named[strings.ToLower(strings.TrimRight(strings.TrimSpace(l), ":"))]
}

// Variant makes an RFC's cleaned text the repository's text variant: the
// title block, then each top-level section under its heading.
func Variant(source string, number int, text string) *repo.Variant {
	v := &repo.Variant{Format: repo.Format, Variant: repo.VariantMan, Source: source,
		Title: "RFC " + strconv.Itoa(number), Syntax: repo.SyntaxText}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	// The title block is everything before the first heading (its lines
	// at the margin are spaced into columns, or have colons, so they're
	// not taken for headings).
	var cur repo.Section
	var body strings.Builder
	flush := func() {
		cur.Body = body.String()
		if cur.Heading != "" || strings.TrimSpace(cur.Body) != "" {
			v.Sections = append(v.Sections, cur)
		}
		body.Reset()
	}
	for i, l := range lines {
		if isHeading(lines, i) {
			flush()
			cur = repo.Section{Heading: strings.TrimSpace(l)}
			continue
		}
		body.WriteString(l)
		body.WriteByte('\n')
	}
	flush()
	return v
}
