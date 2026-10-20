// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The girl variant's prompts are exactly what they were before there were
// variants, so its conversions (and their cached parts) stay current.
func TestGirlPromptsUnchanged(t *testing.T) {
	for file, got := range map[string]string{
		"girl.man.txt": SystemPromptFor(KindMan, ""),
		"girl.rfc.txt": SystemPromptFor(KindRFC, ""),
		"girl.man.user.txt": UserPromptFor(KindMan, "LS(1)", 1, 1, nil, "text\n") + "\n----\n" +
			UserPromptFor(KindMan, "LS(1)", 2, 3, []string{"NAME"}, "text\n"),
		"girl.rfc.user.txt": UserPromptFor(KindRFC, "RFC 1", 1, 2, nil, "text\n"),
	} {
		want, err := os.ReadFile("testdata/prompts/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s changed:\n%s", file, got)
		}
	}
	if SystemPromptVariant(KindMan, "", "") != SystemPromptVariant(KindMan, VariantGirl, "") {
		t.Error(`"" isn't girl`)
	}
}

func TestVariantPrompts(t *testing.T) {
	names := regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	families := map[string]int{}
	for _, v := range Variants() {
		if !names.MatchString(v.Name) || v.Summary == "" || v.Persona == "" || v.Version < 1 || v.RFCVersion < 1 {
			t.Errorf("%s: incomplete: %+v", v.Name, v)
		}
		if _, ok := familyRules[v.Family]; !ok {
			t.Errorf("%s: no family %q", v.Name, v.Family)
		}
		families[v.Family]++
		for _, kind := range []string{KindMan, KindRFC} {
			s := SystemPromptVariant(kind, v.Name, "be extra")
			if strings.ContainsAny(s, "{}") && regexp.MustCompile(`\{[a-z]+\}`).MatchString(s) {
				t.Errorf("%s %q: unfilled phrase: %s", v.Name, kind, regexp.MustCompile(`\{[a-z]+\}`).FindString(s))
			}
			for _, must := range []string{"THE ONE RULE", "OUTPUT FORMAT", "EXACTLY", "Output ONLY", "Additional instructions:\nbe extra"} {
				if !strings.Contains(s, must) {
					t.Errorf("%s %q: no %q", v.Name, kind, must)
				}
			}
			if kind == KindRFC && !strings.Contains(s, "BCP 14") {
				t.Errorf("%s: RFC prompt doesn't keep BCP 14 key words", v.Name)
			}
			if kind == KindMan && (strings.Contains(s, "RFC") || !strings.Contains(s, "Unix manual pages")) {
				t.Errorf("%s: man prompt is about RFCs", v.Name)
			}
			if kind == KindRFC && strings.Contains(s, "Unix manual pages") {
				t.Errorf("%s: RFC prompt is about man pages", v.Name)
			}
			u := UserPromptVariant(kind, v.Name, "X(1)", 1, 1, nil, "x")
			if kind == KindMan && !strings.Contains(u, "sign-off") && !strings.Contains(u, "farewell") && !strings.Contains(u, "blessing") {
				t.Errorf("%s: no sign-off asked for: %s", v.Name, u)
			}
		}
	}
	if len(Variants()) < 9 || Variants()[0].Name != VariantGirl {
		t.Errorf("variants: %v", Variants())
	}
	if _, err := LookupVariant("nope"); err == nil {
		t.Error("nope is a variant")
	}
	if PromptVersionVariant(KindMan, VariantGirl) != PromptVersion || PromptVersionFor(KindRFC) != 1 {
		t.Error("girl's prompt versions changed")
	}
}
