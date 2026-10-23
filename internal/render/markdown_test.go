// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package render

import "testing"

func TestMarkdown(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"# ✨ NAME ✨\n\n<pink>pink</pink> and <Rainbow>yay</rainbow>\n", "# ✨ NAME ✨\n\npink and yay\n"},
		{"<bold>big</bold>, <italic>soft</italic>, <kbd>Ctrl</kbd>", "**big**, *soft*, <kbd>Ctrl</kbd>"},
		{"a <bold>long\nline</bold> b", "a **long\nline** b"},
		{"the `<pink>` tag, and \\<pink>", "the `<pink>` tag, and \\<pink>"},
		{"```\n<pink>code</pink>\n```\n<pink>not</pink>", "```\n<pink>code</pink>\n```\nnot"},
		{"~~~sh\n<bold>x</bold>\n~~~\n", "~~~sh\n<bold>x</bold>\n~~~\n"},
		{"a stray <bold> stays", "a stray <bold> stays"},
		{"", ""},
	} {
		if got := Markdown(c.in); got != c.want {
			t.Errorf("Markdown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
