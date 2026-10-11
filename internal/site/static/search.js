// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages search: queries a repository's sharded index straight from the
// browser, fetching only the shards a query needs. This is the same
// algorithm as Searcher in internal/repo/search.go; keep them in step.

import { repoCache, escapeName, browseURL } from "./girlpages.js";

export { escapeName };

const PREFIX_WEIGHT = 0.5;
const PREFIX_CAP = 100; // as in search.go
const FUZZY_WEIGHT = 0.3;
const MAX_PREFIX_WORDS = 50;
const MAX_FUZZY_WORDS = 5;

// Compare strings by code point, which is how Go orders the index.
export function cmp(a, b) {
  const x = [...a], y = [...b];
  for (let i = 0; i < x.length && i < y.length; i++) {
    const d = x[i].codePointAt(0) - y[i].codePointAt(0);
    if (d) return d < 0 ? -1 : 1;
  }
  return x.length - y.length < 0 ? -1 : x.length > y.length ? 1 : 0;
}

export function tokens(s) {
  const out = [];
  for (const m of s.matchAll(/[\p{L}\p{Nd}_]+/gu)) {
    const n = [...m[0]].length;
    if (n >= 2 && n <= 40) out.push(m[0].toLowerCase());
  }
  return out;
}

export function grams(w) {
  const rs = [..."^" + w + "$"];
  const seen = new Set();
  for (let i = 0; i + 3 <= rs.length; i++) seen.add(rs.slice(i, i + 3).join(""));
  return [...seen];
}

// editDistance is optimal string alignment distance, as in Go.
export function editDistance(a, b) {
  const x = [...a], y = [...b];
  const d = [];
  for (let i = 0; i <= x.length; i++) { d.push(new Array(y.length + 1).fill(0)); d[i][0] = i; }
  for (let j = 0; j <= y.length; j++) d[0][j] = j;
  for (let i = 1; i <= x.length; i++) {
    for (let j = 1; j <= y.length; j++) {
      const cost = x[i - 1] === y[j - 1] ? 0 : 1;
      d[i][j] = Math.min(d[i - 1][j] + 1, d[i][j - 1] + 1, d[i - 1][j - 1] + cost);
      if (i > 1 && j > 1 && x[i - 1] === y[j - 2] && x[i - 2] === y[j - 1]) d[i][j] = Math.min(d[i][j], d[i - 2][j - 2] + 1);
    }
  }
  return d[x.length][y.length];
}

// queryGrams matches the Go function: grams plus two start grams that
// catch a typo in the first letters.
export function queryGrams(t) {
  const gs = grams(t), rs = [...t];
  if (rs.length >= 3) gs.push("^" + rs[0] + rs[2], "^" + rs[1] + rs[0]);
  return [...new Set(gs)];
}

export function maxTypos(w) {
  const n = [...w].length;
  return n <= 2 ? 0 : n <= 5 ? 1 : 2;
}

// segmentName matches site.SegmentName: the directory a name gets.
export function segmentName(s) {
  return s.startsWith(".") ? escapeName(s) : s;
}

function findRange(ranges, key) {
  let lo = 0, hi = ranges.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (cmp(ranges[mid].last, key) >= 0) hi = mid; else lo = mid + 1;
  }
  if (lo < ranges.length && cmp(ranges[lo].first, key) <= 0) return ranges[lo];
  return null;
}

export class Searcher {
  // cache is anything with get(path) returning parsed JSON; by default the
  // page's localStorage cache for repo.
  constructor(repo, cache = repoCache(repo)) {
    this.cache = cache;
    this.shards = new Map();
    this.loaded = null;
  }

  get(path) { return this.cache.get(path); }

  load() {
    if (!this.loaded) {
      this.loaded = (async () => {
        this.manifest = await this.get("manifest.json");
        const ix = this.manifest.index;
        const [words, grams, docs] = await Promise.all([
          this.get(`index/words/manifest-${ix.words}.json`),
          this.get(`index/grams/manifest-${ix.grams}.json`),
          this.get(`index/docs/manifest-${ix.docs}.json`),
        ]);
        this.words = words.shards;
        this.grams = grams.shards;
        this.docs = docs;
      })();
      this.loaded.catch(() => { this.loaded = null; });
    }
    return this.loaded;
  }

  shard(kind, sha) {
    if (!this.shards.has(sha)) {
      const p = this.get(`index/${kind}/shard-${sha.slice(0, 2)}/${sha}.json`);
      p.catch(() => this.shards.delete(sha));
      this.shards.set(sha, p);
    }
    return this.shards.get(sha);
  }

  // postings returns [doc id, score] pairs for a word.
  async postings(w) {
    const r = findRange(this.words, w);
    if (!r) return [];
    const sh = await this.shard("words", r.sha256);
    return Object.hasOwn(sh.words, w) ? sh.words[w] : [];
  }

  // doc returns [name, section, description] for a doc id.
  async doc(id) {
    const size = this.docs.size, i = Math.floor(id / size);
    if (size < 1 || id < 0 || i >= this.docs.shards.length) throw new Error(`search index has no page ${id}`);
    const sh = await this.shard("docs", this.docs.shards[i]);
    const d = sh.docs[id % size];
    if (!d) throw new Error(`search index has no page ${id}`);
    return d;
  }

  async prefixWords(p) {
    if ([...p].length < 2) return [];
    const out = [];
    for (const r of this.words) {
      if (cmp(r.last, p) < 0 || (!r.first.startsWith(p) && cmp(r.first, p) > 0)) continue;
      const sh = await this.shard("words", r.sha256);
      const keys = Object.keys(sh.words).filter(w => w !== p && w.startsWith(p)).sort(cmp);
      out.push(...keys);
      if (out.length >= MAX_PREFIX_WORDS) return out.slice(0, MAX_PREFIX_WORDS);
    }
    return out;
  }

  async fuzzyWords(t) {
    const maxD = maxTypos(t);
    if (!maxD) return [];
    const pages = new Map();
    await Promise.all(queryGrams(t).map(async g => {
      const r = findRange(this.grams, g);
      if (!r) return;
      let sh;
      try {
        sh = await this.shard("grams", r.sha256);
      } catch (e) {
        // Offline without typo matching saved: search without it.
        if (!(e instanceof TypeError)) throw e;
        this.missedTypos = true;
        return;
      }
      if (!Object.hasOwn(sh.grams, g)) return;
      const ws = sh.grams[g], ns = sh.pages[g] || [];
      ws.forEach((w, i) => { if (i < ns.length) pages.set(w, ns[i]); });
    }));
    const cs = [];
    for (const [w, n] of pages) {
      if (w === t) continue;
      const d = editDistance(t, w);
      if (d <= maxD) cs.push({ w, d, n });
    }
    cs.sort((a, b) => a.d - b.d || b.n - a.n || cmp(a.w, b.w));
    return cs.slice(0, MAX_FUZZY_WORDS).map(c => c.w);
  }

  async term(t) {
    const out = new Map();
    const exact = await this.postings(t);
    for (const [id, sc] of exact) addBest(out, id, sc);
    for (const w of await this.prefixWords(t)) {
      for (const [id, sc] of await this.postings(w)) addBest(out, id, Math.min(sc, PREFIX_CAP) * PREFIX_WEIGHT);
    }
    if (!exact.length) {
      for (const w of await this.fuzzyWords(t)) {
        for (const [id, sc] of await this.postings(w)) addBest(out, id, sc * FUZZY_WEIGHT);
      }
    }
    return out;
  }

  // search returns up to limit hits, best first:
  // {name, section, description, sources, score}. source, if given, keeps
  // to pages from that source ("debian", "debian/trixie"), and accept, if
  // given, to hits it resolves true for.
  async search(query, limit = 50, source = "", accept = null) {
    this.missedTypos = false;
    await this.load();
    const terms = [...new Set(tokens(query))];
    const whole = query.trim().toLowerCase();
    if (!terms.length && !whole) return [];
    const perTerm = await Promise.all(terms.map(t => this.term(t)));
    const bonus = new Map();
    if (whole && !/[ \t]/.test(whole)) {
      for (const [id, sc] of await this.postings(whole)) addBest(bonus, id, sc);
    }
    const combine = (all) => {
      const out = new Map(bonus);
      const seen = new Set();
      for (const pt of perTerm) {
        for (const id of pt.keys()) {
          if (seen.has(id)) continue;
          seen.add(id);
          let total = 0, n = 0;
          for (const other of perTerm) {
            if (other.has(id)) { total += other.get(id); n++; }
          }
          if (all && n < perTerm.length) continue;
          out.set(id, total + (bonus.get(id) || 0));
        }
      }
      return out;
    };
    let res = combine(true);
    if (res.size === bonus.size) res = combine(false);
    const ranked = [...res].sort((a, b) => b[1] - a[1] || a[0] - b[0]);
    if (!source && !accept) {
      const top = limit > 0 ? ranked.slice(0, limit) : ranked;
      return Promise.all(top.map(async ([id, score]) => hit(await this.doc(id), score)));
    }
    // Within a source: look further down the ranking, a batch at a time.
    const hits = [];
    for (let i = 0; i < ranked.length && i < MAX_FILTERED && (limit <= 0 || hits.length < limit); i += 64) {
      const batch = ranked.slice(i, Math.min(i + 64, MAX_FILTERED));
      const docs = await Promise.all(batch.map(([id]) => this.doc(id)));
      const ok = await Promise.all(docs.map(d => (!source || d[3].split(" ").includes(source)) && (!accept || accept(hit(d, 0)))));
      docs.forEach((d, j) => {
        if ((limit <= 0 || hits.length < limit) && ok[j]) hits.push(hit(d, batch[j][1]));
      });
    }
    return hits;
  }
}

const MAX_FILTERED = 2000;

function hit(d, score) {
  return { name: d[0], section: d[1], description: d[2], sources: d[3] ? d[3].split(" ") : [], score };
}

function addBest(m, id, score) {
  if (!m.has(id) || score > m.get(id)) m.set(id, score);
}

// The search page: ?q=words, and &src=debian/trixie to keep to a source.
async function page() {
  const box = document.getElementById("search");
  if (!box) return;
  const status = document.getElementById("search-status");
  const list = document.getElementById("search-results");
  const base = box.dataset.base;
  const params = new URLSearchParams(location.search);
  const q = params.get("q") || "", src = params.get("src") || "";
  // Only pages saved for offline: asked for, or by default when offline.
  const savedOnly = params.has("saved") ? params.get("saved") === "1" : navigator.onLine === false;
  const toggle = document.getElementById("search-saved");
  if (toggle && "caches" in globalThis) {
    const u = new URLSearchParams(params);
    u.set("saved", savedOnly ? "0" : "1");
    toggle.href = `${base}search/?${u}`;
    toggle.textContent = "📴 only saved pages";
    toggle.classList.toggle("on", savedOnly);
    toggle.hidden = false;
  }
  for (const input of document.querySelectorAll('input[name="q"]')) input.value = q;
  const s = new Searcher(box.dataset.repo);
  try {
    await s.load();
    const chips = document.getElementById("search-sources");
    const link = (key, label) => {
      const a = document.createElement("a");
      a.className = "pill" + (key === src ? " on" : "");
      const u = new URLSearchParams({ q });
      if (key) u.set("src", key);
      a.href = `${base}search/?${u}`;
      a.textContent = label;
      return a;
    };
    if (chips && s.manifest.sources && s.manifest.sources.length) {
      chips.append(link("", "✨ everywhere"));
      for (const e of s.manifest.sources) {
        chips.append(link(e.release ? `${e.distro}/${e.release}` : e.distro, e.release ? "· " + e.release : e.distro));
      }
    }
  } catch {
    // the search itself will say what's wrong
  }
  if (!q.trim()) return;
  const where = src ? ` in ${src.replace("/", " ")}` : "";
  document.title = `${q}${where} · search · girlpages 💅`;
  status.textContent = "looking… 🔎";
  try {
    const accept = savedOnly ? (h) => s.cache.hasPage(h.name, h.section, src) : null;
    const hits = await s.search(q, 100, src, accept);
    list.replaceChildren();
    const saved = savedOnly ? " saved" : "";
    const typos = s.missedTypos ? " (typo matching isn't saved, so typos won't be caught)" : "";
    if (!hits.length) {
      status.textContent = `nothing${saved} for “${q}”${where}, bestie 😭 try another word?${typos}`;
      return;
    }
    status.textContent = `${hits.length === 100 ? "the top 100" : hits.length}${saved} page${hits.length === 1 ? "" : "s"} for “${q}”${where} ✨${typos}`;
    for (const h of hits) {
      const li = document.createElement("li");
      const a = document.createElement("a");
      a.href = src
        ? browseURL(base, { page: h.name, sec: h.section, src })
        : `${base}man/${encodeURIComponent(segmentName(h.section))}/${encodeURIComponent(segmentName(h.name))}/`;
      const name = document.createElement("span");
      name.className = "name";
      name.textContent = h.name;
      const sec = document.createElement("span");
      sec.className = "sec";
      sec.textContent = `(${h.section})`;
      a.append(name, sec);
      const desc = document.createElement("span");
      desc.className = "desc";
      desc.textContent = h.description;
      li.append(a, desc);
      list.append(li);
    }
  } catch (e) {
    // fetch fails with a TypeError when there's no network at all.
    status.textContent = e instanceof TypeError && e.status === undefined
      ? "you're offline 📵 and this search needs bits that aren't saved yet. ones you've done before still work!"
      : `search broke 💔 ${e.message}`;
  }
}

if (typeof document !== "undefined") page();
