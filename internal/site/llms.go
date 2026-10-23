// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
	"github.com/NaomiAmethyst/girlpages/internal/web"
)

// FormatDoc is where the repository's format is described.
const FormatDoc = "https://github.com/NaomiAmethyst/girlpages/blob/main/doc/repo-format.md"

// llmsTxt writes llms.txt (see https://llmstxt.org): what the site is,
// for language models and the tools they use, and where to read it:
// pages' addresses, the sections' and packages' listings, and the
// repository with every page's text.
func (b *builder) llmsTxt(pages []repo.PageRef) error {
	girly := 0
	count := map[string]int{}
	for _, p := range pages {
		count[mainKey(p.Pointer.Section)]++
		if p.Girlified() {
			girly++
		}
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return sectionLess(keys[i], keys[j]) })
	var srcs []string
	for _, e := range b.m.Sources {
		if e.Release == "" {
			srcs = append(srcs, e.Distro)
		}
	}
	// Absolute addresses, if the site's is known.
	u := func(path string) string {
		if a := b.abs(path); a != "" {
			return a
		}
		return path
	}

	var t strings.Builder
	t.WriteString("# girlpages\n\n")
	fmt.Fprintf(&t, "> Unix manual pages, rewritten by a language model in the voice of a Gen Z girl with every detail kept: %s, %d of them girlified so far",
		pluralOf(len(pages), "page"), girly)
	if len(srcs) > 0 {
		fmt.Fprintf(&t, ", from %s", strings.Join(srcs, ", "))
	}
	fmt.Fprintf(&t, ". Updated %s.\n\n", b.m.Generated.Format(time.DateOnly))
	t.WriteString("The girly versions were written by a language model, so for anything that matters (exact option semantics, security advice), check the original.\n\n")
	fmt.Fprintf(&t, "Each page is at %s (for example %s: the girly version, or the original if it isn't girlified yet); its original is at %s, shown by the site's JavaScript. ",
		u(b.o.Base+"girl/<section>/<name>/"), u(b.pageURL("ls", "1")), u(b.o.Base+"man/<section>/<name>/"))
	fmt.Fprintf(&t, "Every page's text, original and girly, every version, is in the repository at %s as JSON (its format is described below); it's for programs, not crawling, so robots.txt asks crawlers to keep out of it.\n\n",
		u(b.o.Base+"repo/"))

	t.WriteString("## Sections\n\n")
	for _, k := range keys {
		name, _ := web.SectionName(k)
		label := "Section " + k + ": " + name
		if manpage.IsSeries(k) {
			label = name
		}
		fmt.Fprintf(&t, "- [%s](%s): %s, listed A to Z\n", label, u(b.listURL(k, "")), pluralOf(count[k], "page"))
	}
	if b.packageLists != nil {
		t.WriteString("\n## Packages\n\n")
		fmt.Fprintf(&t, "- [Every package](%s): the packages pages come from, A to Z, each linking to its pages at %s\n",
			u(b.packagesURL("")), u(b.o.Base+"package/<name>/"))
		for _, e := range b.m.Sources {
			if e.Release == "" {
				fmt.Fprintf(&t, "- [Packages in %s](%s)\n", e.Distro, u(b.packagesURL(e.Distro)))
			}
		}
	}
	t.WriteString("\n## Optional\n\n")
	fmt.Fprintf(&t, "- [Repository format](%s): how the repository's files fit together, to read pages' text from %s\n", FormatDoc, u(b.o.Base+"repo/"))
	fmt.Fprintf(&t, "- [Repository manifest](%s): what the repository has, and its indexes\n", u(b.o.Base+"repo/manifest.json"))
	t.WriteString("- [girlpages on GitHub](https://github.com/NaomiAmethyst/girlpages): the software, with `girl` and `girlc` for reading these pages in a terminal\n")
	return b.write(filepath.Join(b.o.Out, "llms.txt"), []byte(t.String()))
}
