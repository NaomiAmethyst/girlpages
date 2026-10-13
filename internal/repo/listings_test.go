// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestListings(t *testing.T) {
	old := listingChunk
	listingChunk = 10
	defer func() { listingChunk = old }()
	s := testStore(t)
	// A second section in chapter 3, so it has sub-listings.
	man := ManVariant(Hash([]byte("SSL_new")), "SSL_NEW(3ssl)\\n\\nNAME\\n       SSL_new - create a new SSL structure\\n")
	mi, err := s.PutVariant(man)
	if err != nil {
		t.Fatal(err)
	}
	err = s.UpdateListing("SSL_new", "3ssl", func(l *Listing) error {
		l.Versions = append(l.Versions, Version{Source: man.Source, Title: man.Title, Description: "create a new SSL structure",
			Added: time.Unix(1700000000, 0).UTC(), Variants: map[string]VariantInfo{VariantMan: mi}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pages, _ := s.AllPages()
	m, err := s.Publish(pages)
	if err != nil {
		t.Fatal(err)
	}
	var lm ListingsManifest
	if err := s.Read(IndexManifestPath("listings", m.Index.Listings), &lm); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range lm.Listings {
		got = append(got, fmt.Sprintf("%s/%s:%d", r.Key, r.Sub, r.Count))
	}
	if strings.Join(got, " ") != "1/:6 3/:32 3/3:31 3/3ssl:1" {
		t.Fatalf("listings: %v", got)
	}

	r, _ := lm.Listings.Find("3", "")
	var sl SectionListing
	if err := s.Read(ShardPath("listings", r.SHA256), &sl); err != nil {
		t.Fatal(err)
	}
	if sl.Count != 32 || len(sl.Chunks) != 4 {
		t.Fatalf("chapter 3: %+v", sl)
	}
	if fmt.Sprint(sl.Letters) != "[{F 1} {P 4} {S 4}]" {
		t.Errorf("letters: %v", sl.Letters)
	}
	var names []string
	for i, c := range sl.Chunks {
		var ch ListingPagesChunk
		if err := s.Read(ShardPath("listings", c.SHA256), &ch); err != nil {
			t.Fatal(err)
		}
		if ch.Pages[0].Name != c.First || (i == 0) != (c.Prev == "") {
			t.Errorf("chunk %d: first %q prev %q, starts %q", i+1, c.First, c.Prev, ch.Pages[0].Name)
		}
		for _, p := range ch.Pages {
			names = append(names, p.Name+"("+p.Section+")")
			if p.Name == "printf" && (!p.Girl || p.Description != "formatted output conversion") {
				t.Errorf("printf(3): %+v", p)
			}
		}
	}
	if len(names) != 32 || names[0] != "filler00(3)" || names[30] != "printf(3)" || names[31] != "SSL_new(3ssl)" {
		t.Errorf("chapter 3 pages: %v", names)
	}

	// Collected garbage keeps them.
	if _, err := s.GC(false); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(ShardPath("listings", sl.Chunks[3].SHA256), &ListingPagesChunk{}); err != nil {
		t.Errorf("after GC: %v", err)
	}
}
