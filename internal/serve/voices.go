// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package serve

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// ShellPath is the app's shell: what a page in a variant other than
// girly is sent (pirate/1/ls/, man/1/ls/), and anything else in one, for
// the app to show.
const ShellPath = "static/shell.html"

// siteInfo is what the site's manifest says that addresses depend on:
// its variants (each a top-level directory: girl/1/ls/, pirate/1/ls/) and
// its series (rfc), whose documents and listings once had addresses of
// their own (rfc/9110/, rfc/), now girl/rfc/9110/ and section/rfc/.
type siteInfo struct {
	voices, series map[string]bool
}

// siteInfos reads it from a source's repo/manifest.json, again when that
// changes (looked at no more than every few seconds).
type siteInfos struct {
	mu      sync.Mutex
	key     string
	checked time.Time
	info    siteInfo
}

func (s *siteInfos) get(src Source) siteInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.checked) < 5*time.Second && s.key != "" {
		return s.info
	}
	s.checked = time.Now()
	f, ok := src.Find("repo/manifest.json")
	if !ok {
		return s.info
	}
	defer f.Close()
	key := f.ETag("") + f.ModTime().String()
	if key == s.key {
		return s.info
	}
	r, err := f.Open("")
	if err != nil {
		return s.info
	}
	if c, ok := r.(io.Closer); ok {
		defer c.Close()
	}
	var m struct {
		Variants map[string]int `json:"variants"`
		Sections []struct {
			Section string `json:"section"`
		} `json:"sections"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 64<<20)).Decode(&m); err != nil {
		return s.info
	}
	info := siteInfo{voices: map[string]bool{}, series: map[string]bool{}}
	for v := range m.Variants {
		if validVoice(v) {
			info.voices[v] = true
		}
	}
	for _, e := range m.Sections {
		// A series is a word, not a number (as manpage.IsSeries).
		if len(e.Section) > 1 && (e.Section[0] < '0' || e.Section[0] > '9') && validVoice(e.Section) {
			info.series[e.Section] = true
		}
	}
	s.key, s.info = key, info
	return info
}

// reserved are the site's top-level directories (and files) that aren't
// variants, as site.Reserved.
var reserved = map[string]bool{"section": true, "packages": true, "package": true, "search": true, "browse": true, "offline": true, "static": true,
	"repo": true, "src": true, "404.html": true, "index.html": true, "sw.js": true, "sitemap.xml": true,
	"robots.txt": true, "llms.txt": true, "manifest.webmanifest": true}

func validVoice(v string) bool {
	if v == "" || len(v) > 32 || reserved[v] || v[0] < 'a' || v[0] > 'z' {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// route says what to do with an address that isn't a file: serve the
// shell (a voice's), redirect it (a series' old address), or neither.
func (i siteInfo) route(rel string) (shell bool, redirect string) {
	first, rest, _ := strings.Cut(rel, "/")
	switch {
	case i.series[first] && !i.voices[first]:
		rest = strings.Trim(rest, "/")
		switch {
		case rest == "":
			return false, "section/" + first + "/"
		case strings.HasPrefix(rest, "page/"):
			return false, "section/" + first + "/" + rest + "/"
		case !strings.Contains(rest, "/"):
			return false, "girl/" + first + "/" + rest + "/"
		}
	case i.voices[first]:
		return true, ""
	}
	return false, ""
}
