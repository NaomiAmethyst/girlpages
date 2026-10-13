// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The repository cache says what kind of failure a file that couldn't be
// read is: offline, missing, or broken.
func TestRepoErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repo/manifest.json":
			fmt.Fprint(w, `{"pages": 1}`)
		case "/repo/bad.json":
			fmt.Fprint(w, `{not json`)
		case "/repo/boom.json":
			http.Error(w, "oops", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got := node(t, fmt.Sprintf(`
const { RepoCache, MemoryStore, errorKind } = await import(%q);
let down = false;
const fetchText = async (url, init) => {
  if (down) throw new TypeError("fetch failed");
  const r = await fetch(url, init);
  if (!r.ok) { const e = new Error(url + ": " + r.status); e.status = r.status; throw e; }
  return r.text();
};
const c = new RepoCache(%q, { store: new MemoryStore(), fetch: fetchText, storage: null });
const kind = async (path) => { try { await c.get(path); return "ok"; } catch (e) { return errorKind(e) + (e.status ? " " + e.status : ""); } };
const out = [await kind("manifest.json"), await kind("nope.json"), await kind("bad.json"), await kind("boom.json")];
down = true;
out.push(await kind("manifest.json")); // kept: given from the store
out.push(await kind("other.json"));
out.push(errorKind(new TypeError("x.y is undefined")));
console.log(out.join(","));`, staticFile(t, "cache.js"), srv.URL+"/repo/"))
	if want := "ok,missing 404,broken,broken 500,ok,offline,broken"; strings.TrimSpace(string(got)) != want {
		t.Errorf("kinds: %s, want %s", got, want)
	}
}
