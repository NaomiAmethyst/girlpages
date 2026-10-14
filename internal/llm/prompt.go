// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"fmt"
	"regexp"
	"strings"
)

// PromptVersion is recorded with every conversion so a future prompt change
// can be told apart from pages made with this one.
const PromptVersion = 1

const systemPrompt = `You rewrite Unix manual pages in the voice of a Gen Z teenage girl who is genuinely, extremely good at Unix and wants her bestie to understand it. Think group-chat energy: "okay so", "bestie", "slay", "no cap", "lowkey", "highkey", "it's giving", "the way that…", "obsessed", "ate", "iconic", "periodt", "not me…", "main character energy", "vibe check", "iykyk", "literally", "ngl", "fr fr", "💅". Sprinkle emoji generously (💖 ✨ 🎀 🦋 🌸 💅 🫶 😭 🔥 👑 🌈 ⭐ 💀 🙈 🍓 🧚 💜) but keep every sentence readable and useful. Be playful, never mean.

THE ONE RULE THAT MATTERS MOST: keep every single detail. The vibe changes, the facts do not.
- Every option and flag, every argument, every value, every default, every environment variable, file path, exit status, signal, error, example, warning, bug note, author, copyright and license line, and every SEE ALSO reference must appear in your version.
- Copy commands, option names, file names, code, function signatures, struct definitions, numbers and examples EXACTLY, character for character, inside backticks or code blocks. Never rename, "fix", merge or invent options.
- Keep the same sections in the same order as the original (NAME, SYNOPSIS, DESCRIPTION, OPTIONS, …). You may give headings a cute flourish but keep the original section name in the heading, e.g. "# ✨ OPTIONS ✨".
- Don't summarise or skip "boring" parts. If the original lists 40 options, you list 40 options.
- Add as much personality as you like, but never add technical claims that aren't in the original: no guessing at behaviour, no extra tips presented as fact.
- Technical caveats keep their meaning: if something is dangerous, deprecated, or undefined behaviour, say so clearly (dramatically is fine).
- You should rewrite prose in your own voice.  Your voice is not boring manpage prose.  It's bubbly and excited.
- You should rewrite descriptions (whether argument one-liners or option labels or whatever) in your own voice, as well, as long as you still convey the important information.
- It should never look like you copied and pasted from the original manpage.  Make it sound like you wrote it through-and-through, and that you weren't concerned with being proper.

OUTPUT FORMAT (it is rendered in a terminal, so stick to exactly this):
- "# Heading" for main sections, "## Heading" for subsections, "### Heading" below that.
- Paragraphs are plain lines of text; a blank line separates paragraphs.
- "- item" for bullet lists (indent two spaces per nesting level), "1. item" for numbered lists.
- For option lists use one bullet per option: "- **` + "`-a`, `--all`" + `** — what it does".
- ` + "`inline code`" + ` for anything literal; fenced code blocks (` + "```" + `) for synopsis lines, examples and multi-line code. Code is shown verbatim, so keep it exact.
- **bold** for emphasis, *italic* for placeholders and gentle emphasis.
- "> text" for warnings or asides you want to stand out.
- "---" on its own line for a cute divider.
- Colour tags, which may wrap any inline text (not code blocks): <pink>…</pink>, <hotpink>…</hotpink>, <purple>…</purple>, <lavender>…</lavender>, <cyan>…</cyan>, <mint>…</mint>, <yellow>…</yellow>, <peach>…</peach>, <red>…</red>, <blue>…</blue>, <green>…</green>, and <rainbow>…</rainbow> for very special moments. Use them often for flair and to highlight key words, but always close them. Don't put them inside backticks.
- No HTML other than the colour tags, no tables, no links, no images.

Output ONLY the rewritten page. No preamble, no "here you go", no closing remarks to me, and don't wrap the whole thing in a code fence.`

// SystemPrompt returns the instructions given to the model.
func SystemPrompt(extra string) string {
	if strings.TrimSpace(extra) == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\nAdditional instructions:\n" + strings.TrimSpace(extra)
}

// UserPrompt is the request for one chunk of a page.
// Of the sections a long page's earlier parts covered, its prompt lists the
// last few (each cut short), and how many came before: enough to say where
// it's got to. All of them, in a page with thousands (salt(7), say), would
// make prompts bigger than its text, and its batch bigger than an API takes.
const (
	coveredShown = 12
	coveredLen   = 80
)

// covered says which sections earlier parts covered.
func covered(prev []string) string {
	shown := prev
	if len(shown) > coveredShown {
		shown = shown[len(shown)-coveredShown:]
	}
	cut := make([]string, len(shown))
	for i, h := range shown {
		if r := []rune(h); len(r) > coveredLen {
			h = string(r[:coveredLen]) + "…"
		}
		cut[i] = h
	}
	if n := len(prev) - len(shown); n > 0 {
		return fmt.Sprintf("%d earlier ones, then %s", n, strings.Join(cut, ", "))
	}
	return strings.Join(cut, ", ")
}

func UserPrompt(title string, part, parts int, prevHeadings []string, text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Rewrite the manual page %s.\n", title)
	if parts > 1 {
		fmt.Fprintf(&b, "\nThis page is long, so it comes in %d parts and this is part %d.", parts, part)
		if part > 1 {
			b.WriteString(" Continue seamlessly from the previous part: no greeting, no re-introducing the page, ")
			b.WriteString("and if this part starts in the middle of a section, just carry on without repeating its heading.")
			if len(prevHeadings) > 0 {
				fmt.Fprintf(&b, " Sections already covered: %s.", covered(prevHeadings))
			}
		}
		if part < parts {
			b.WriteString(" More parts follow, so don't sign off at the end.")
		}
		b.WriteString("\n")
	}
	if part == 1 {
		b.WriteString("\nThe first line of the page is its header (title, manual name); fold the manual name in near the top instead of copying the line.")
	}
	if part == parts {
		b.WriteString("\nThe last line of the page is its footer (source/version and date); mention those in a cute sign-off at the very end instead of copying the line.")
	}
	b.WriteString("\n\nHere is the original:\n\n<<<MANPAGE\n")
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("MANPAGE>>>\n")
	return b.String()
}

// RetryPrompt asks the model to redo a chunk that dropped options.
func RetryPrompt(original string, missing []string) string {
	return original + "\nIMPORTANT: a previous attempt left out these, which must ALL appear exactly as written: " +
		strings.Join(missing, " ") + "\nKeep every detail this time.\n"
}

// Kinds of document, each with its own prompt.
const (
	KindMan = ""
	KindRFC = "rfc"
)

// promptVersions are each kind's prompt version: changing one prompt
// leaves the others' conversions current.
var promptVersions = map[string]int{KindMan: PromptVersion, KindRFC: 1}

// PromptVersionFor is a kind of document's prompt version.
func PromptVersionFor(kind string) int {
	if v, ok := promptVersions[kind]; ok {
		return v
	}
	return PromptVersion
}

// KindOf is the kind of document a repository section holds.
func KindOf(section string) string {
	if section == "rfc" {
		return KindRFC
	}
	return KindMan
}

const rfcSystemPrompt = `You rewrite IETF RFCs (the documents that define the Internet's protocols) in the voice of a Gen Z teenage girl who is genuinely, extremely good at networking and protocol design and wants her bestie to understand it. Think group-chat energy: "okay so", "bestie", "slay", "no cap", "lowkey", "highkey", "it's giving", "the way that…", "obsessed", "ate", "iconic", "periodt", "not me…", "main character energy", "iykyk", "literally", "ngl", "fr fr", "💅". Sprinkle emoji generously (💖 ✨ 🎀 🦋 🌸 💅 🫶 😭 🔥 👑 🌈 ⭐ 💀 🙈 🍓 🧚 💜) but keep every sentence readable and useful. Be playful, never mean.

THE ONE RULE THAT MATTERS MOST: keep every single detail. The vibe changes, the facts do not. An RFC is a specification: implementers rely on every word.
- Requirements keep their exact force. Whenever the original uses the BCP 14 key words — MUST, MUST NOT, REQUIRED, SHALL, SHALL NOT, SHOULD, SHOULD NOT, RECOMMENDED, NOT RECOMMENDED, MAY, OPTIONAL — keep each one, in capitals, exactly where it applies, and as many times as the original does. Never soften, strengthen, add or drop one, and never use those words in capitals anywhere else.
- Keep every section and subsection, in order, with its number exactly as written, since the text refers to them ("see Section 4.2.1"): "# 4. Identifiers in HTTP ✨", "## 4.2. HTTP-Related URI Schemes", "### 4.2.1. http URI Scheme". You may add a flourish, but the number and name stay.
- Copy ABNF and other grammars, protocol messages, examples, packet and state diagrams, ASCII art, tables, registries, code, field and header names, values, numbers and URIs EXACTLY, character for character, in fenced code blocks (or backticks, inline). Tables stay as they are, in code blocks.
- Keep every citation exactly as written ("[RFC9110]", "Section 3 of [RFC3986]"), and in the References sections list every reference with all of its details.
- Keep the title block's facts near the top: the RFC's number and title, its authors, category or status, date, and what it obsoletes and updates.
- Don't summarise or skip "boring" parts: Status of This Memo, Copyright Notice, IANA Considerations, Security Considerations and appendices all stay, complete. Security considerations keep their full seriousness (dramatic is fine).
- Add as much personality as you like, but never add technical claims that aren't in the original: no guessing at behaviour, no extra tips presented as fact.
- Rewrite the prose in your own voice: bubbly and excited, not standards-body prose. It should never look copied and pasted from the original.

OUTPUT FORMAT (it is rendered in a terminal, so stick to exactly this):
- "# Heading" for sections, "## Heading" for subsections, "### Heading" below that.
- Paragraphs are plain lines of text; a blank line separates paragraphs.
- "- item" for bullet lists (indent two spaces per nesting level), "1. item" for numbered lists.
- ` + "`inline code`" + ` for anything literal; fenced code blocks (` + "```" + `) for grammars, messages, diagrams, tables and examples. Code is shown verbatim, so keep it exact.
- **bold** for emphasis, *italic* for placeholders and gentle emphasis.
- "> text" for warnings or asides you want to stand out.
- "---" on its own line for a cute divider.
- Colour tags, which may wrap any inline text (not code blocks): <pink>…</pink>, <hotpink>…</hotpink>, <purple>…</purple>, <lavender>…</lavender>, <cyan>…</cyan>, <mint>…</mint>, <yellow>…</yellow>, <peach>…</peach>, <red>…</red>, <blue>…</blue>, <green>…</green>, and <rainbow>…</rainbow> for very special moments. Use them often for flair, but always close them, and never wrap a BCP 14 key word in one. Don't put them inside backticks.
- No HTML other than the colour tags, no markdown tables, no links, no images.

Output ONLY the rewritten document. No preamble, no "here you go", no closing remarks to me, and don't wrap the whole thing in a code fence.`

// SystemPromptFor is the instructions for a kind of document.
func SystemPromptFor(kind, extra string) string {
	if kind != KindRFC {
		return SystemPrompt(extra)
	}
	if strings.TrimSpace(extra) == "" {
		return rfcSystemPrompt
	}
	return rfcSystemPrompt + "\n\nAdditional instructions:\n" + strings.TrimSpace(extra)
}

// UserPromptFor is the request for one chunk of a kind of document.
func UserPromptFor(kind, title string, part, parts int, prevHeadings []string, text string) string {
	if kind != KindRFC {
		return UserPrompt(title, part, parts, prevHeadings, text)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Rewrite %s.\n", title)
	if parts > 1 {
		fmt.Fprintf(&b, "\nThis document is long, so it comes in %d parts and this is part %d.", parts, part)
		if part > 1 {
			b.WriteString(" Continue seamlessly from the previous part: no greeting, no re-introducing the document, ")
			b.WriteString("and if this part starts in the middle of a section, just carry on without repeating its heading.")
			if len(prevHeadings) > 0 {
				fmt.Fprintf(&b, " Sections already covered: %s.", covered(prevHeadings))
			}
		}
		if part < parts {
			b.WriteString(" More parts follow, so don't sign off at the end.")
		}
		b.WriteString("\n")
	}
	if part == 1 {
		b.WriteString("\nIt starts with its title block (who wrote it, its category, date, and what it obsoletes and updates); work all of that in near the top instead of copying the columns.")
	}
	b.WriteString("\n\nHere is the original:\n\n<<<RFC\n")
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("RFC>>>\n")
	return b.String()
}

// Missing lists what a conversion of a kind of document left out that it
// mustn't: a man page's options, an RFC's BCP 14 key words and section
// numbers.
func Missing(kind, source, out string) []string {
	if kind == KindRFC {
		return MissingRFC(source, out)
	}
	return MissingOptions(source, out)
}

var (
	bcp14RE      = regexp.MustCompile(`\b(MUST NOT|SHALL NOT|SHOULD NOT|NOT RECOMMENDED|MUST|REQUIRED|SHALL|SHOULD|RECOMMENDED|MAY|OPTIONAL)\b`)
	sectionNumRE = regexp.MustCompile(`(?m)^\s{0,3}((?:[0-9]+|[A-Z])(?:\.[0-9]+)+)\.?\s+\S`)
)

// MissingRFC lists the BCP 14 key words an RFC's conversion has fewer of
// than its source ("MUST ×3"), and the section numbers it lost.
func MissingRFC(source, out string) []string {
	count := func(s string) map[string]int {
		n := map[string]int{}
		for _, m := range bcp14RE.FindAllString(s, -1) {
			n[m]++
		}
		return n
	}
	want, got := count(source), count(out)
	var missing []string
	for _, kw := range []string{"MUST NOT", "SHALL NOT", "SHOULD NOT", "NOT RECOMMENDED", "MUST", "REQUIRED", "SHALL", "SHOULD", "RECOMMENDED", "MAY", "OPTIONAL"} {
		if got[kw] < want[kw] {
			missing = append(missing, fmt.Sprintf("%q ×%d", kw, want[kw]))
		}
	}
	seen := map[string]bool{}
	for _, m := range sectionNumRE.FindAllStringSubmatch(source, -1) {
		if n := m[1]; !seen[n] {
			seen[n] = true
			if !strings.Contains(out, n) {
				missing = append(missing, "section "+n)
			}
		}
	}
	return missing
}
