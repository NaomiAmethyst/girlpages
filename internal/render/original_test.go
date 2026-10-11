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
