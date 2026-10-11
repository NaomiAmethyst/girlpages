// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package render

import (
	"regexp"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestInline(t *testing.T) {
	cases := []struct {
		in   string
		want []seg
	}{
		{"plain", []seg{{"plain", style{}}}},
		{"a **b** c", []seg{{"a ", style{}}, {"b", style{bold: true}}, {" c", style{}}}},
		{"use `-a` now", []seg{{"use ", style{}}, {"-a", style{code: true}}, {" now", style{}}}},
		{"``a `b` c``", []seg{{"a `b` c", style{code: true}}}},
		{"<pink>hi</pink>!", []seg{{"hi", style{color: "pink"}}, {"!", style{}}}},
		{"<pink>a <mint>b</mint> c</pink>", []seg{
			{"a ", style{color: "pink"}}, {"b", style{color: "mint"}}, {" c", style{color: "pink"}},
		}},
		{"an *em* word", []seg{{"an ", style{}}, {"em", style{italic: true}}, {" word", style{}}}},
		// Globs and arithmetic aren't emphasis.
		{"files *.c and 2*3", []seg{{"files *.c and 2*3", style{}}}},
		{"lone ** stars", []seg{{"lone ** stars", style{}}}},
		// Unknown tags stay as text.
		{"<file> and <b>", []seg{{"<file> and <b>", style{}}}},
		{`\*not em\*`, []seg{{"*not em*", style{}}}},
		{"`<pink>` literal", []seg{{"<pink>", style{code: true}}, {" literal", style{}}}},
	}
	for _, c := range cases {
		got := parseInline(c.in, style{})
		if len(got) != len(c.want) {
			t.Errorf("%q: got %+v, want %+v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: seg %d = %+v, want %+v", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestBlocks(t *testing.T) {
	md := "# ✨ NAME ✨\n\n`ls` - list stuff\nand more\n\n- **`-a`** — all\n  continued\n  - nested\n1. first\n\n```\ncode  here\n  indented\n```\n\n> careful\n> bestie\n\n---\n"
	bl := parseBlocks(md)
	var kinds []blockKind
	for _, b := range bl {
		kinds = append(kinds, b.kind)
	}
	want := []blockKind{bHeading, bBlank, bPara, bBlank, bList, bList, bList, bBlank, bCode, bBlank, bQuote, bBlank, bRule, bBlank}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
	if bl[2].text != "`ls` - list stuff and more" {
		t.Errorf("para = %q", bl[2].text)
	}
	if bl[4].text != "**`-a`** — all continued" || bl[4].level != 0 {
		t.Errorf("item = %+v", bl[4])
	}
	if bl[5].level != 1 || bl[6].marker != "1." {
		t.Errorf("nested = %+v, numbered = %+v", bl[5], bl[6])
	}
	if strings.Join(bl[8].lines, "|") != "code  here|  indented" {
		t.Errorf("code = %q", bl[8].lines)
	}
	if bl[10].text != "careful bestie" {
		t.Errorf("quote = %q", bl[10].text)
	}
}

func TestRenderWidth(t *testing.T) {
	md := "# DESCRIPTION 💅\n\n" + strings.Repeat("okay so this is <pink>lowkey</pink> iconic ✨ and `--flag` ate ", 20) +
		"\n\n- " + strings.Repeat("bullet words 🦋 ", 30) + "\n"
	for _, mode := range []ColorMode{NoColor, Color16, Color256, TrueColor} {
		out := Render(md, Options{Width: 60, Color: mode, Title: "LS(1)", Footer: "girlpages test"})
		for _, l := range strings.Split(out, "\n") {
			if w := runewidth.StringWidth(ansiRE.ReplaceAllString(l, "")); w > 60 {
				t.Errorf("mode %d: line too wide (%d): %q", mode, w, l)
			}
		}
		if mode == NoColor && strings.Contains(out, "\x1b") {
			t.Error("NoColor output has escape codes")
		}
		if mode != NoColor && !strings.Contains(out, "\x1b[") {
			t.Errorf("mode %d output has no colour", mode)
		}
		if strings.Contains(out, "<pink>") || strings.Contains(out, "**") {
			t.Errorf("markup leaked: %q", out)
		}
	}
}

func TestRenderPlainLayout(t *testing.T) {
	out := Render("# NAME\n\n`true` — do nothing\n\n```\ntrue [args]\n```\n", Options{Width: 80, Color: NoColor})
	want := "NAME\n\n       true — do nothing\n\n         true [args]\n"
	if out != want {
		t.Errorf("got\n%q\nwant\n%q", out, want)
	}
}

func TestHeader(t *testing.T) {
	out := Render("hi", Options{Width: 80, Color: NoColor, Title: "LS(1)", Footer: "girlpages"})
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "LS(1)") || !strings.HasSuffix(lines[0], "LS(1)") ||
		runewidth.StringWidth(lines[0]) != 80 {
		t.Errorf("header = %q", lines[0])
	}
}

func TestStripControls(t *testing.T) {
	evil := "# hi\x1b]0;pwned\x07\n\nok \x1b[2Jbestie\u009b31m\ttab\r\n"
	out := Render(evil, Options{Width: 80, Color: NoColor, Title: "X\x1b[1m(1)"})
	if strings.ContainsAny(out, "\x1b\x07\r\u009b") {
		t.Errorf("control characters got through: %q", out)
	}
	if !strings.Contains(out, "ok [2Jbestie31m") {
		t.Errorf("text went missing: %q", out)
	}
}
