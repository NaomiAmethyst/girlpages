// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package render

import (
	"strings"
	"testing"
)

func TestHTML(t *testing.T) {
	md := strings.Join([]string{
		"# ✨ NAME ✨",
		"",
		"`ls` - list <pink>stuff</pink> <script>alert(x)</script> & more",
		"",
		"## options",
		"- **`-a`** — all",
		"  - nested *item*",
		"- second",
		"1. first",
		"2. second",
		"",
		"```",
		"ls <dir> && echo hi",
		"```",
		"> careful",
		"---",
		"see ls(1), printf(3) and https://example.com/a_b. <rainbow>yay</rainbow>",
		"# ✨ NAME ✨",
	}, "\n")
	link := func(name, sec string) string { return "/man/" + sec + "/" + name }
	out, toc := HTML(md, HTMLOptions{Link: link})

	for _, want := range []string{
		`<h2 id="name">✨ NAME ✨</h2>`,
		`<code>ls</code> - list <span class="c-pink">stuff</span> &lt;script&gt;alert(x)&lt;/script&gt; &amp; more`,
		`<h3 id="options">options</h3>`,
		"<ul>\n<li><strong><code>-a</code></strong> — all<ul>\n<li>nested <em>item</em></li>\n</ul>\n</li>\n<li>second</li>\n</ul>\n<ol>\n<li value=\"1\">first</li>\n<li value=\"2\">second</li>\n</ol>",
		"<pre><code>ls &lt;dir&gt; &amp;&amp; echo hi</code></pre>",
		"<blockquote>careful</blockquote>",
		`<hr class="sparkle">`,
		`<a href="/man/1/ls">ls(1)</a>`,
		`<a href="/man/3/printf">printf(3)</a>`,
		`<a href="https://example.com/a_b">https://example.com/a_b</a>.`,
		`<span class="rainbow">yay</span>`,
		`<h2 id="name-2">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "<script>") {
		t.Error("unescaped script tag")
	}
	if len(toc) != 3 || toc[0].ID != "name" || toc[1].Level != 2 || toc[2].ID != "name-2" {
		t.Errorf("toc = %+v", toc)
	}

	// Without a link function, references stay text.
	out, _ = HTML("see ls(1)", HTMLOptions{})
	if strings.Contains(out, "<a") {
		t.Errorf("unexpected link: %s", out)
	}
}

func TestHTMLLooseList(t *testing.T) {
	out, _ := HTML("- one\n\n- two\n\npara", HTMLOptions{})
	want := "<ul>\n<li>one</li>\n<li>two</li>\n</ul>\n<p>para</p>\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

// A name can start with a digit: 7z(1) is 7z's, not z's.
func TestLinkNamesWithDigits(t *testing.T) {
	link := func(name, sec string) string { return "/man/" + sec + "/" + name }
	got, _ := HTML("see 7z(1), 2048(1) and 0desktop(1).\n", HTMLOptions{Link: link})
	for _, want := range []string{`<a href="/man/1/7z">7z(1)</a>`, `<a href="/man/1/2048">2048(1)</a>`, `<a href="/man/1/0desktop">0desktop(1)</a>`} {
		if !strings.Contains(got, want) {
			t.Errorf("no %s in %s", want, got)
		}
	}
	if strings.Contains(got, `"/man/1/z"`) {
		t.Errorf("z(1) inside 7z(1): %s", got)
	}
}
