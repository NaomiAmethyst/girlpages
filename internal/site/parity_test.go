// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/NaomiAmethyst/girlpages/internal/llm"
	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/web"
)

// The browser's copies of the manual's conventions (manual.js) and the
// site's addresses (urls.js) agree with the Go they mirror.
func TestJSParity(t *testing.T) {
	names := []string{"ls", "Ls", "LS", "[", "0desktop", ".hidden", "git-commit", "a+b", "x:y", "100", "9", "0009", "9000",
		"10000", "SSL_new", "émile", "_under", "#hash", "zz", "a b", "", "1999", "999"}
	secs := []string{"1", "3", "3ssl", "3pm", "8", "n", "rfc", "1p", "l", "9", "6x"}

	in, _ := json.Marshal(map[string]any{"names": names, "secs": secs})
	got := node(t, fmt.Sprintf(`
const m = await import(%q);
const u = await import(%q);
const { names, secs } = %s;
const out = { letter: {}, chapter: {}, series: {}, sectionName: {}, order: [], page: {}, list: {}, browse: {}, variant: {} };
for (const v of ["girl", "man", "boy", "femboy", "lolcat", "pirate", "eli5", "woman", "witch", "bofh", "other", "constructor"]) out.variant[v] = m.variantLabelOf(v);
for (const n of names) out.letter[n] = m.letterOf(n);
for (const s of secs) {
  out.chapter[s] = m.chapter(s);
  out.series[s] = m.isSeries(s);
  out.sectionName[s] = m.sectionName(s);
  for (const n of names) {
    if (n) out.page[s + " " + n] = u.pageURL("/g/", n, s);
    if (n) out.browse[s + " " + n] = u.browseURL("/g/", { page: n, sec: s, src: "debian/trixie" });
  }
  out.list[s] = [u.listingURL("/g/", s, "", 1), u.listingURL("/g/", m.chapter(s), s, 3)];
}
const order = names.map((n, i) => [n, secs[i %% secs.length]]);
order.sort((a, b) => { const x = a[0].toLowerCase(), y = b[0].toLowerCase(); return x !== y ? m.nameCmp(x, y) : (a[1] < b[1] ? -1 : a[1] > b[1] ? 1 : 0); });
out.order = order;
console.log(JSON.stringify(out));`, staticFile(t, "manual.js"), staticFile(t, "urls.js"), in))
	var js struct {
		Letter, Chapter map[string]string
		Series          map[string]bool
		SectionName     map[string][2]string
		Order           [][2]string
		Page, Browse    map[string]string
		List            map[string][2]string
		Variant         map[string]string
	}
	if err := json.Unmarshal(got, &js); err != nil {
		t.Fatalf("%v\n%s", err, got)
	}

	for v, label := range js.Variant {
		if want := variantLabel(v); label != want {
			t.Errorf("variantLabelOf(%q) = %q, Go says %q", v, label, want)
		}
	}
	for _, v := range llm.Variants() {
		if _, ok := variantLabels[v.Name]; !ok {
			t.Errorf("no label for the %s variant", v.Name)
		}
	}
	b := &builder{o: Options{Base: "/g/"}}
	for _, n := range names {
		if want := manpage.Letter(n); js.Letter[n] != want {
			t.Errorf("letterOf(%q) = %q, Go says %q", n, js.Letter[n], want)
		}
	}
	for _, s := range secs {
		if want := manpage.Chapter(s); js.Chapter[s] != want {
			t.Errorf("chapter(%q) = %q, Go says %q", s, js.Chapter[s], want)
		}
		if want := manpage.IsSeries(s); js.Series[s] != want {
			t.Errorf("isSeries(%q) = %v, Go says %v", s, js.Series[s], want)
		}
		name, emoji := web.SectionName(s)
		if js.SectionName[s] != [2]string{name, emoji} {
			t.Errorf("sectionName(%q) = %v, Go says %s %s", s, js.SectionName[s], name, emoji)
		}
		for _, n := range names {
			if n == "" {
				continue
			}
			if want := b.pageURL(n, s); js.Page[s+" "+n] != want {
				t.Errorf("pageURL(%q, %q) = %q, Go says %q", n, s, js.Page[s+" "+n], want)
			}
			if want := b.browseURL("debian/trixie", s, n); js.Browse[s+" "+n] != want {
				t.Errorf("browseURL(%q, %q) = %q, Go says %q", n, s, js.Browse[s+" "+n], want)
			}
		}
		want := [2]string{b.listURL(s, ""), listPageURL(b.listURL(manpage.Chapter(s), s), 3)}
		if manpage.IsSeries(s) {
			want[1] = js.List[s][1] // a series has no sections
		}
		if js.List[s] != want {
			t.Errorf("listingURL(%q) = %v, Go says %v", s, js.List[s], want)
		}
	}
	order := make([][2]string, len(names))
	for i, n := range names {
		order[i] = [2]string{n, secs[i%len(secs)]}
	}
	sort.SliceStable(order, func(i, j int) bool {
		return manpage.ListingLess(order[i][0], order[i][1], order[j][0], order[j][1])
	})
	if fmt.Sprint(js.Order) != fmt.Sprint(order) {
		t.Errorf("listing order:\n js %v\n go %v", js.Order, order)
	}
}
