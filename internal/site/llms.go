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
	count := map[string]int{}
	for _, p := range pages {
		count[mainKey(p.Pointer.Section)]++
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
	fmt.Fprintf(&t, "> Unix manual pages, rewritten by a language model in the voice of a Gen Z girl with every detail kept: %s",
		pluralOf(len(pages), "page"))
	if len(srcs) > 0 {
		fmt.Fprintf(&t, ", from %s", strings.Join(srcs, ", "))
	}
	fmt.Fprintf(&t, ". Updated %s.\n\n", b.m.Generated.Format(time.DateOnly))
	t.WriteString("The girly versions were written by a language model, so for anything that matters (exact option semantics, security advice), check the original.\n\n")
	site := strings.TrimSuffix(u(b.o.Base), "/")
	fmt.Fprintf(&t, "Each page is at %s (for example %s), girly; its original is at %s, but shown by the site's JavaScript. ",
		u(b.o.Base+"girl/<section>/<name>/"), u(b.pageURL("ls", "1")), u(b.o.Base+"man/<section>/<name>/"))
	fmt.Fprintf(&t, "Every page's text, girly and original, is also in the repository at %s, as JSON: for reading a page at a time, not crawling (robots.txt asks crawlers to keep out). ", u(b.o.Base+"repo/"))
	t.WriteString("This shell function (it needs curl and jq) prints a page as Markdown, from its address or its section and name, girly or the original (`man`):\n\n")
	t.WriteString("```sh\n" + strings.ReplaceAll(girlpageSh, "SITE", site) + "```\n\n")
	fmt.Fprintf(&t, "For example `girlpage %s`, or `girlpage 1 ls man` for the original. ", u(b.pageURL("ls", "1")))
	t.WriteString("Its requests say they're from it in their User-Agent; we'd like it (but don't require it) if you added your own, the agent or tool you are and its version, by setting `GIRLPAGES_AGENT` (`GIRLPAGES_AGENT=\"ExampleBot/1.0\" girlpage 1 ls`), or in your own requests' User-Agent if you fetch pages yourself. ")
	t.WriteString("A name or section with characters other than letters, digits and `_+-.,:@` (or starting with a dot) has them escaped as `%XX` in the repository's paths, as its format (below) describes.\n\n")
	fmt.Fprintf(&t, "For more than a page now and then, girlc is a more robust thin client for the repository, with more to it: it takes pages as man does (`printf(3)`, `rfc 9110`, a name alone in man's section order) or by address, escaping names itself; searches (`-k`); reads other versions and variants (`--versions`, `--page-version`, `--variant man`); and prints Markdown (`girlc --markdown %s`), saying who it is in its User-Agent too (`GIRLPAGES_AGENT`). ", u(b.pageURL("ls", "1")))
	t.WriteString("It's one static binary, with no dependencies: in each release's packages (deb, rpm, apk, Arch) at https://github.com/NaomiAmethyst/girlpages/releases, or as an image, `docker run --rm ghcr.io/naomiamethyst/girlc --markdown ls`.\n\n")
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

// girlpageSh is a shell function printing a page as Markdown from the
// repository at SITE/repo/: by its address on the site, or its section and
// name (and a variant: girl by default, or man for the original). The
// girly markup is Markdown, but for colour tags, which it drops.
const girlpageSh = `girlpage() {  # girlpage SITE/girl/1/ls/  |  girlpage 1 ls [girl|man]
  local s="${GIRLPAGES:-SITE}" sec name v=girl p src w sha c=(curl -fsS -A "girlpage (+SITE/llms.txt)${GIRLPAGES_AGENT:+ $GIRLPAGES_AGENT}")
  if [ $# -eq 1 ]; then IFS=/ read -r v sec name _ <<<"${1#"$s"/}"; else sec=$1 name=$2 v=${3:-girl}; fi
  p=$("${c[@]}" "$s/repo/page/name-$sec/$name.json" | jq -r .sha256)
  [ -n "$p" ] || { echo "girlpage: no page $name($sec)" >&2; return 1; }
  read -r src w sha < <("${c[@]}" "$s/repo/page/page-${p:0:2}/$p.json" | jq -r --arg v "$v" '.current as $c
    | .versions[] | select(.source == $c) | .variants as $vs
    | ([$v, "girl", "man"] | map(select($vs[.])) | first) as $w | "\($c) \($w) \($vs[$w].sha256)"')
  [ -n "$sha" ] || { echo "girlpage: no text for $name($sec)" >&2; return 1; }
  "${c[@]}" "$s/repo/contents/${src:0:2}/$src/$w-$sha.json" | jq -r '(if .syntax == "text" then "## " else "# " end) as $h
    | .sections[] | (if .heading != "" then "\($h)\(.heading)\n" else "" end) + .body' |
    sed -E 's#</?(pink|hotpink|purple|lavender|cyan|mint|yellow|peach|red|blue|green|rainbow)>##g; s#</?bold>#**#g; s#</?italic>#*#g'
}
`
