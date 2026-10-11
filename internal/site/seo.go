// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"encoding/json"
	"html/template"
	"strings"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
	"github.com/NaomiAmethyst/girlpages/internal/web"
)

// Structured data (schema.org JSON-LD) for search engines. Values that
// need absolute URLs are left out when the site's URL isn't known.

type ld map[string]any

// ldJS encodes structured data for a <script type="application/ld+json">.
// encoding/json escapes <, > and &, so nothing in it can end the script.
func ldJS(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return template.JS(b) // safe: see above
}

// set adds key if value isn't empty.
func (m ld) set(key string, value any) ld {
	switch v := value.(type) {
	case string:
		if v == "" {
			return m
		}
	case nil:
		return m
	}
	m[key] = value
	return m
}

func (b *builder) ldSite() ld {
	return ld{"@type": "WebSite", "name": "girlpages"}.set("url", b.abs(b.o.Base))
}

func (b *builder) ldPublisher() ld {
	p := ld{"@type": "Organization", "name": "girlpages"}.set("url", b.abs(b.o.Base))
	if logo := b.abs(b.o.Base + "static/icon-512.png"); logo != "" {
		p["logo"] = ld{"@type": "ImageObject", "url": logo, "width": 512, "height": 512}
	}
	return p
}

// ldCrumbs is a breadcrumb trail of (name, path) pairs.
func (b *builder) ldCrumbs(trail ...[2]string) ld {
	var items []ld
	for i, c := range trail {
		items = append(items, ld{"@type": "ListItem", "position": i + 1, "name": c[0]}.set("item", b.abs(c[1])))
	}
	return ld{"@type": "BreadcrumbList", "itemListElement": items}
}

func (b *builder) ldWebSite(v *view) template.JS {
	site := b.ldSite()
	site["@context"] = "https://schema.org"
	site["description"] = v.Description
	site["inLanguage"] = "en"
	site["publisher"] = b.ldPublisher()
	if u := b.abs(b.o.Base + "search/"); u != "" {
		site["potentialAction"] = ld{
			"@type":       "SearchAction",
			"target":      ld{"@type": "EntryPoint", "urlTemplate": u + "?q={search_term_string}"},
			"query-input": "required name=search_term_string",
		}
	}
	return ldJS(site)
}

func (b *builder) ldCollection(v *view, u string, items []item) template.JS {
	// The listing itself links every page; the count is enough here.
	page := ld{"@type": "CollectionPage", "name": v.Heading, "description": v.Description, "inLanguage": "en",
		"isPartOf":   b.ldSite(),
		"mainEntity": ld{"@type": "ItemList", "numberOfItems": len(items)},
	}.set("url", b.abs(u))
	return ldJS(ld{"@context": "https://schema.org", "@graph": []ld{page,
		b.ldCrumbs([2]string{"girlpages", b.o.Base}, [2]string{v.Heading, u})}})
}

func (b *builder) ldArticle(ver *repo.Version, pv *pageView, v *view, modified time.Time) template.JS {
	secName, _ := web.SectionName(mainKey(pv.Section))
	about := ld{"@type": "SoftwareApplication", "name": pv.Name, "operatingSystem": "Linux, Unix",
		"applicationCategory": "DeveloperApplication"}.set("softwareVersion", pv.Version)
	keywords := []string{pv.Name, pv.Name + "(" + pv.Section + ")", "man page", "manual", strings.ToLower(secName)}
	for _, src := range sourcesIn(ver) {
		keywords = append(keywords, src.Label())
	}
	art := ld{
		"@type":            "TechArticle",
		"headline":         pv.Title + ": " + pv.Description,
		"name":             pv.Name + "(" + pv.Section + ")",
		"description":      v.Description,
		"inLanguage":       "en",
		"about":            about,
		"keywords":         strings.Join(keywords, ", "),
		"articleSection":   secName,
		"publisher":        b.ldPublisher(),
		"author":           b.ldPublisher(),
		"isPartOf":         b.ldSite(),
		"isBasedOn":        ld{"@type": "CreativeWork", "name": "the " + pv.Name + "(" + pv.Section + ") manual page"},
		"proficiencyLevel": "Beginner",
	}.set("url", b.abs(pv.URL)).set("image", b.abs(b.o.Base+"static/icon-512.png"))
	if u := b.abs(pv.URL); u != "" {
		art["mainEntityOfPage"] = ld{"@type": "WebPage", "@id": u}
	}
	if !ver.Added.IsZero() {
		art["datePublished"] = ver.Added.UTC().Format(time.RFC3339)
	}
	if !modified.IsZero() {
		art["dateModified"] = modified.UTC().Format(time.RFC3339)
	}
	if pv.Model != "" {
		art["contributor"] = ld{"@type": "SoftwareApplication", "name": pv.Model, "applicationCategory": "Large language model"}
	}
	return ldJS(ld{"@context": "https://schema.org", "@graph": []ld{art, b.ldCrumbs(
		[2]string{"girlpages", b.o.Base},
		[2]string{"section " + pv.Section, pv.SectionURL},
		[2]string{pv.Name + "(" + pv.Section + ")", pv.URL},
	)}})
}
