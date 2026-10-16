// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package render

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// OriginalFixture reads testdata/original.json, a page laid out as man
// lays one out, with one of everything the reflowing has to recognise.
func originalFixture(t *testing.T) (header, footer string, sections []OriginalSection) {
	t.Helper()
	b, err := os.ReadFile("testdata/original.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Header, Footer string
		Sections       []OriginalSection
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d.Header, d.Footer, d.Sections
}

func TestOriginalHTML(t *testing.T) {
	header, footer, sections := originalFixture(t)
	got, toc := OriginalHTML(header, footer, sections, HTMLOptions{Link: func(n, s string) string { return "/man/" + s + "/" + n + "/" }})
	for _, want := range []string{
		"<p class=\"manhead\">General Commands Manual</p>\n",
		"<h2 id=\"name\">NAME</h2>\n<p>frob - frobnicate the bar</p>\n",
		// The synopsis is boxed, blank line and all.
		"<pre><code>frob [-a] [-l] [--color[=WHEN]] [file...]\n\nfrob --help</code></pre>",
		// Filled text reflows, with links.
		"<p>The quick brown fox jumps over the lazy dog while the cat watches from the window, wondering",
		`as described in <a href="/man/1/frob/">frob(1)</a> and at <a href="https://example.org/frob">https://example.org/frob</a> for more.</p>`,
		// Lines broken by hand stay broken.
		"<p>Assume tab stops at each COLS instead of eight<br>\nMay also be specified",
		// An indented example is boxed, as one block, keeping its shape.
		"<p>For example:</p>\n<pre><code>frob -a\n\n{\n    &#34;key&#34;: [1, 2],\n}</code></pre>",
		"<h3 id=\"subsection-here\">Subsection here</h3>",
		// Options: tags above, beside, and just beside their descriptions.
		"<dt>-a, --all</dt>\n<dd>\n<p>Do not ignore entries",
		"<dt>-l</dt>\n<dd>\n<p>use a long listing format</p>",
		"<dt>--help</dt>\n<dd>\n<p>Show help.</p>",
		"<dt>--color[=WHEN]</dt>\n<dd>\n<p>Colorize the output;",
		"<p>A second paragraph of the same description, which should stay inside the same definition rather than escape.</p>\n<pre><code>$ frob --color=never</code></pre>\n</dd>\n</dl>",
		"<ul>\n<li>\n<p>first point, which is long enough that it has to wrap onto the next line of the page</p>\n</li>\n<li>\n<p>second point</p>\n</li>\n</ul>\n<ol>\n<li value=\"1\">\n<p>numbered one</p>",
		"<p class=\"manhead\">frob 1.0 · 2026-01-01 · FROB(1)</p>\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "<dl>") != 1 {
		t.Errorf("options should be one list:\n%s", got)
	}
	var ids []string
	for _, h := range toc {
		ids = append(ids, h.ID)
	}
	if s := strings.Join(ids, " "); s != "name synopsis description subsection-here options notes" {
		t.Errorf("toc: %s", s)
	}
}

func TestOriginalHTMLHostile(t *testing.T) {
	got, _ := OriginalHTML("<b>X</b>  Y  <b>X</b>", "", []OriginalSection{
		{Heading: "<script>", Body: "       <img src=x onerror=alert(1)> \x1b[31mred\n"},
	}, HTMLOptions{})
	if strings.Contains(got, "<script") || strings.Contains(got, "<img") || strings.Contains(got, "\x1b") {
		t.Errorf("not escaped: %s", got)
	}
}

// An RFC's layout: text at 3, filled to 72, subsections at the margin.
func TestOriginalHTMLRFC(t *testing.T) {
	body := "\n   " + strings.Repeat("word ", 13) + "words\n   go on and on, as RFC 2119 says they MUST.\n\n" +
		"1.1.  Requirements Language\n\n   The key words in this document are to be interpreted as described in\n   BCP 14.\n\n" +
		"      +--------+\n      | header |\n      +--------+\n"
	got, toc := OriginalHTML("", "", []OriginalSection{{Heading: "1.  Introduction", Body: body}},
		HTMLOptions{Indent: 3, Width: 72, Link: func(n, s string) string { return "/" + s + "/" + n + "/" }})
	for _, want := range []string{
		`<p>word word word word word word word word word word word word word words go on and on, as <a href="/rfc/2119/">RFC 2119</a> says they MUST.</p>`,
		`<h3 id="1-1-requirements-language">1.1.  Requirements Language</h3>`,
		"<p>The key words in this document are to be interpreted as described in BCP 14.</p>",
		"<pre><code>+--------+\n| header |\n+--------+</code></pre>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if len(toc) != 2 || toc[1].Level != 2 {
		t.Errorf("toc: %+v", toc)
	}
}

// rfcSections reads testdata/rfc.json: an RFC's text as the repository
// has it (after its title block), with the things in RFC 1459 and others
// that were taken for headings (a diagram whose edge sticks out left of
// the text, a grammar at the margin, a table of contents numbered like a
// list, a page's footer) and the headings that are, numbered every way
// RFCs number them.
func rfcSections(t *testing.T) []OriginalSection {
	t.Helper()
	b, err := os.ReadFile("testdata/rfc.json")
	if err != nil {
		t.Fatal(err)
	}
	var out []OriginalSection
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOriginalRFCHeadings(t *testing.T) {
	h, toc := OriginalHTML("", "", rfcSections(t), HTMLOptions{Indent: 3, Width: 72})
	var subs []string
	for _, x := range toc {
		if x.Level == 2 {
			subs = append(subs, x.Text)
		}
	}
	want := []string{"1.1 Servers", "2.3.1  Message format in 'pseudo' BNF", "4.3. Name server internals",
		"4.3.1. Queries and responses", "2.16. Minimal VMTP Implementation",
		"3.2  Alternatives                               Rule1 / Rule2", "3.9  L2C (URL to URC):", "Format", "Fields"}
	if strings.Join(subs, "\n") != strings.Join(want, "\n") {
		t.Errorf("subsections:\n  %q\nwant\n  %q", subs, want)
	}
	if strings.Contains(h, "<ol") {
		t.Error("the table of contents was taken for a list")
	}
	// The diagram is one block, its edge and all.
	if !strings.Contains(h, "<pre><code>                  [ Server 1 ]\n                   /       \\\n[ Server 7 ] [ Server 8 ]   [ Server 10 ]</code></pre>") {
		t.Errorf("the diagram was split:\n%s", h)
	}
	for _, code := range []string{"&lt;message&gt;  ::=", "Cheriton"} {
		if !strings.Contains(h, "<pre><code>"+code) {
			t.Errorf("%s isn't code:\n%s", code, h)
		}
	}
	// Old RFCs: written at the margin, and justified.
	for _, para := range []string{
		"<p>This User Datagram Protocol (UDP) is defined to make available a datagram mode of packet-switched computer communication in the underlying protocol.</p>",
		"<p>Source Port is an optional field, when meaningful, it indicates the port of the sending process, and may be assumed to be the port to which a reply should be addressed.</p>",
		"<p>The fields are as follows.</p>",
		"<p>Length is the length in octets of this user datagram including this header and the data.</p>",
	} {
		if !strings.Contains(h, para) {
			t.Errorf("no %s in:\n%s", para, h)
		}
	}
	if !strings.Contains(h, "<pre><code> 0      7 8") {
		t.Errorf("the old RFC's diagram isn't code:\n%s", h)
	}
	// References are a list, two lines or three, not drawings.
	for _, ref := range []string{"<dt>[CONDISP]</dt>", "<dt>[HOSTS]</dt>"} {
		if !strings.Contains(h, ref) {
			t.Errorf("no %s in:\n%s", ref, h)
		}
	}
	if strings.Contains(h, "------------") {
		t.Errorf("a heading's underline was kept:\n%s", h)
	}
	if !strings.Contains(h, "\n&lt;!ELEMENT BODY") {
		t.Errorf("<!ELEMENT isn't code:\n%s", h)
	}

	// In man's rendering of a page, a line left of the text is a
	// subsection (.SS), whatever it says.
	_, toc = OriginalHTML("", "", []OriginalSection{{Heading: "FUNCTIONS", Body: "\n" +
		"   check_dependencies_opts($meta, $phase, $type)\n       Checks them.\n"}}, HTMLOptions{})
	if len(toc) != 2 || toc[1].Text != "check_dependencies_opts($meta, $phase, $type)" {
		t.Errorf("man subsection: %v", toc)
	}
}
