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
	return userPrompt(variants[VariantGirl], title, part, parts, prevHeadings, text)
}

func userPrompt(v Variant, title string, part, parts int, prevHeadings []string, text string) string {
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
		b.WriteString("\nThe last line of the page is its footer (source/version and date); mention those in " + v.phrase("signoff", KindMan) + " at the very end instead of copying the line.")
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

// KindOf is the kind of document a repository section holds.
func KindOf(section string) string {
	if section == "rfc" {
		return KindRFC
	}
	return KindMan
}

// UserPromptFor is the request for one chunk of a kind of document.
func UserPromptFor(kind, title string, part, parts int, prevHeadings []string, text string) string {
	return UserPromptVariant(kind, VariantGirl, title, part, parts, prevHeadings, text)
}

// UserPromptVariant is the request for one chunk of a kind of document, in
// a variant.
func UserPromptVariant(kind, variant, title string, part, parts int, prevHeadings []string, text string) string {
	if kind != KindRFC {
		return userPrompt(variantOrGirl(variant), title, part, parts, prevHeadings, text)
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
