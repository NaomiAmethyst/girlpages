// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages search: queries a repository's sharded index straight from the
// browser, fetching only the shards a query needs. This is the same
// algorithm as Searcher in internal/repo/search.go; keep them in step.

import { repoCache } from "./cache.js";
import { shardsOf, rows, namePath } from "./format.js";
import { browseURL, pageURL } from "./urls.js";
import { chapter } from "./manual.js";
import { sourceNav } from "./widgets.js";

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
        const [words, grams, names, docs] = await Promise.all([
          this.get(`index/words/manifest-${ix.words}.json`),
          this.get(`index/grams/manifest-${ix.grams}.json`),
          this.get(`index/names/manifest-${ix.names}.json`),
          this.get(`index/docs/manifest-${ix.docs}.json`),
        ]);
        this.words = shardsOf(words);
        this.grams = shardsOf(grams);
        this.names = shardsOf(names);
        this.docs = shardsOf(docs);
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
    if (!Object.hasOwn(sh.words, w)) return [];
    const flat = sh.words[w], out = [];
    for (let i = 0; i + 1 < flat.length; i += 2) out.push([flat[i], flat[i + 1]]);
    return out;
  }

  // tableShard is the page table's shard (names or docs) holding id, read
  // as {first, rows}.
  async tableShard(kind, id) {
    const r = (kind === "names" ? this.names : this.docs).find(x => x.first <= id && id <= x.last);
    if (!r) throw new Error(`search index has no page ${id}`);
    const key = kind + ":" + r.sha256;
    if (!this.tables) this.tables = new Map();
    if (!this.tables.has(key)) {
      const p = this.shard(kind, r.sha256).then(sh => ({ first: sh.first, rows: rows(sh[kind]) }));
      p.catch(() => this.tables.delete(key));
      this.tables.set(key, p);
    }
    return this.tables.get(key);
  }

  // name returns [name, section] for a doc id.
  async name(id) {
    const t = await this.tableShard("names", id);
    const r = t.rows[id - t.first];
    return r ? [r.name, r.section] : ["", ""];
  }

  // docOf returns [description, sources] for a doc id.
  async docOf(id) {
    const t = await this.tableShard("docs", id);
    const r = t.rows[id - t.first];
    return r ? [r.description, r.sources] : ["", ""];
  }

  // doc returns [name, section, description, sources] for a doc id.
  async doc(id) {
    const [[name, section], [description, sources]] = await Promise.all([this.name(id), this.docOf(id)]);
    return [name, section, description, sources];
  }

  // prefetchNames starts fetching every shard of the page table's names
  // (a few, about a megabyte each, cached after), so that a search's
  // results can be named as soon as they're found.
  async prefetchNames() {
    await this.load();
    await Promise.all(this.names.map(r => this.tableShard("names", r.first).catch(() => {})));
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
    const ranked = await this.rank(query);
    if (!source && !accept) {
      const top = limit > 0 ? ranked.slice(0, limit) : ranked;
      return Promise.all(top.map(async ([id, score]) => hit(await this.doc(id), score)));
    }
    return (await this.filtered(ranked, 0, limit, source, accept)).hits;
  }

  // filtered walks down the ranking from start, a batch at a time, for up
  // to limit hits from source that accept (if given) takes, resolving to
  // {hits, next}: where to go on from (-1 at the end).
  async filtered(ranked, start, limit, source = "", accept = null) {
    const hits = [];
    let i = start;
    for (; i < ranked.length && i < MAX_FILTERED && (limit <= 0 || hits.length < limit); i += 64) {
      const batch = ranked.slice(i, Math.min(i + 64, MAX_FILTERED));
      const docs = await Promise.all(batch.map(([id]) => this.doc(id)));
      const ok = await Promise.all(docs.map(d => (!source || d[3].split(" ").includes(source)) && (!accept || accept(hit(d, 0)))));
      docs.forEach((d, j) => {
        if ((limit <= 0 || hits.length < limit) && ok[j]) hits.push(hit(d, batch[j][1]));
      });
    }
    return { hits, next: i < ranked.length && i < MAX_FILTERED ? i : -1 };
  }

  // rank finds the pages for a query, best first, as [doc id, score]
  // pairs: everything but what doc says about each.
  async rank(query) {
    this.missedTypos = false;
    await this.load();
    const terms = [...new Set(seriesTerms(tokens(query), this.manifest))];
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
    return [...res].sort((a, b) => b[1] - a[1] || a[0] - b[0]);
  }
}

const MAX_FILTERED = 2000;

// seriesTerms is repo.SeriesTerms: "rfc 9110" is searched as rfc9110.
function seriesTerms(terms, m) {
  const series = new Set((m && m.sections || []).map(e => e.section.toLowerCase()).filter(s => s.length > 1 && !/^[0-9]/.test(s)));
  const out = [];
  for (let i = 0; i < terms.length; i++) {
    if (series.has(terms[i]) && i + 1 < terms.length && /^[0-9]+$/.test(terms[i + 1])) {
      out.push(terms[i] + terms[i + 1].replace(/^0+/, ""));
      i++;
      continue;
    }
    out.push(terms[i]);
  }
  return out;
}

function hit(d, score) {
  return { name: d[0], section: d[1], description: d[2], sources: d[3] ? d[3].split(" ") : [], score };
}

function addBest(m, id, score) {
  if (!m.has(id) || score > m.get(id)) m.set(id, score);
}

// directPage is the page a query names exactly, if there's one: "2 open",
// "open(2)", "rfc 9110" (a section, or a chapter's sections in order, and
// a name). Search goes straight there.
export async function directPage(query, searcher) {
  const q = query.trim();
  let m = /^(\S+)\s+(\S+)$/.exec(q), sec, name;
  if (m) [sec, name] = [m[1].toLowerCase(), m[2]];
  else if ((m = /^([^\s()]+)\(([^\s()]+)\)$/.exec(q))) [name, sec] = [m[1], m[2].toLowerCase()];
  else return null;
  await searcher.load();
  const secs = (searcher.manifest.sections || []).map(e => e.section);
  const tries = secs.filter(x => x === sec);
  if (chapter(sec) === sec) tries.push(...secs.filter(x => x !== sec && chapter(x) === sec));
  for (const x of tries) {
    try {
      await searcher.cache.get(namePath(name, x));
      return { name, sec: x };
    } catch (e) {
      if (e.status !== 404) return null;
    }
  }
  return null;
}

// searchers keeps one Searcher per repository, so the index files a search
// has loaded serve the next.
const searchers = new Map();
export function searcher(repo) {
  if (!searchers.has(repo)) searchers.set(repo, new Searcher(repo));
  return searchers.get(repo);
}

// searchPage runs the search page in box (the static one, or the app's):
// ?q=words, and &src=debian/trixie to keep to a source. current() says
// whether it's still wanted, and signal, if given, stops it when it isn't;
// go(url) is how it goes straight to a page a query names exactly.
export async function searchPage(box, params, { current = () => true, signal = null, go = (u) => location.replace(u) } = {}) {
  const $ = (id) => box.querySelector("#" + id);
  const status = $("search-status");
  const list = $("search-results");
  const base = box.dataset.base;
  const q = params.get("q") || "", src = params.get("src") || "";
  // Only pages saved for offline: asked for, or by default when offline.
  const savedOnly = params.has("saved") ? params.get("saved") === "1" : navigator.onLine === false;
  const toggle = $("search-saved");
  if (toggle && "caches" in globalThis) {
    const u = new URLSearchParams(params);
    u.set("saved", savedOnly ? "0" : "1");
    toggle.href = `${base}search/?${u}`;
    toggle.textContent = "📴 only saved pages";
    toggle.classList.toggle("on", savedOnly);
    toggle.hidden = false;
  }
  for (const input of document.querySelectorAll('input[name="q"]')) input.value = q;
  const s = searcher(box.dataset.repo);
  try {
    await s.load();
    const chips = $("search-sources");
    const href = (key) => {
      const u = new URLSearchParams({ q });
      if (key) u.set("src", key);
      return `${base}search/?${u}`;
    };
    if (chips && s.manifest.sources && s.manifest.sources.length) {
      chips.innerHTML = sourceNav(s.manifest.sources, { on: src, href, all: "✨ everywhere" });
    }
  } catch {
    // the search itself will say what's wrong
  }
  if (!q.trim()) return;
  const where = src ? ` in ${src.replace("/", " ")}` : "";
  if (current()) document.title = `${q}${where} · search · girlpages 💅`;
  status.textContent = "looking… 🔎";
  const saved = savedOnly ? " saved" : "";
  try {
    const accept = savedOnly ? (h) => s.cache.hasPage(h.name, h.section, src) : null;
    s.prefetchNames().catch(() => {});
    const direct = await directPage(q, s).catch(() => null);
    if (direct && current()) {
      return go(src ? browseURL(base, { page: direct.name, sec: direct.sec, src }) : pageURL(base, direct.name, direct.sec));
    }
    const ranked = await s.rank(q);
    if (!current()) return;
    list.replaceChildren();
    const typos = () => (s.missedTypos ? " (typo matching isn't saved, so typos won't be caught)" : "");
    const none = () => { status.textContent = `nothing${saved} for “${q}”${where}, bestie 😭 try another word?${typos()}`; };
    if (!ranked.length) return none();
    const plural = (n) => `${n.toLocaleString()}${saved} page${n === 1 ? "" : "s"}`;
    // Results show a screenful at a time, each as soon as what it says
    // about itself (in the page table, a shard per 128 pages) arrives,
    // so the first appear long before the rest would have.
    const filter = Boolean(src || accept);
    let shown = 0, next = 0, more = true, busy = false;
    status.textContent = filter ? `looking through ${plural(ranked.length).replace(saved, "")} for “${q}”${where}… 🔎${typos()}`
      : `${plural(ranked.length)} for “${q}”${where} ✨${typos()}`;
    const row = () => {
      const li = document.createElement("li");
      li.className = "loading";
      li.innerHTML = '<a><span class="name">…</span></a><span class="desc"></span>';
      list.append(li);
      return li;
    };
    const named = (li, name, section) => {
      li.className = "";
      const a = li.querySelector("a");
      a.href = src ? browseURL(base, { page: name, sec: section, src }) : pageURL(base, name, section);
      a.querySelector(".name").textContent = name;
      const sec = document.createElement("span");
      sec.className = "sec";
      sec.textContent = `(${section})`;
      a.append(sec);
    };
    const fill = (li, h) => {
      named(li, h.name, h.section);
      li.querySelector(".desc").textContent = h.description;
    };
    const page = async () => {
      if (busy || !more || !current()) return;
      busy = true;
      try {
        if (!filter) {
          const batch = ranked.slice(next, next + PAGE);
          next += batch.length;
          more = next < ranked.length;
          // Each is named as soon as its name's in (the names are being
          // fetched since the search started), and described after.
          await Promise.all(batch.map(async ([id]) => {
            const li = row();
            try {
              const [name, section] = await s.name(id);
              named(li, name, section);
              const [description] = await s.docOf(id);
              li.querySelector(".desc").textContent = description;
            } catch {
              li.remove();
            }
          }));
          shown += batch.length;
        } else {
          const r = await s.filtered(ranked, next, PAGE, src, accept);
          if (!current()) return;
          for (const h of r.hits) fill(row(), h);
          shown += r.hits.length;
          next = r.next;
          more = next >= 0;
          status.textContent = shown ? `${more ? "the top " : ""}${plural(shown)} for “${q}”${where} ✨${typos()}` : "";
          if (!shown && !more) none();
        }
      } finally {
        busy = false;
      }
      if (more && nearEnd()) page();
    };
    const sentinel = document.createElement("div");
    list.after(sentinel);
    const nearEnd = () => sentinel.getBoundingClientRect().top < innerHeight + 800;
    if ("IntersectionObserver" in globalThis) {
      const io = new IntersectionObserver((es) => { if (es.some(e => e.isIntersecting)) page(); }, { rootMargin: "800px 0px" });
      io.observe(sentinel);
      signal?.addEventListener("abort", () => io.disconnect());
    }
    await page();
  } catch (e) {
    // fetch fails with a TypeError when there's no network at all.
    status.textContent = e instanceof TypeError && e.status === undefined
      ? "you're offline 📵 and this search needs bits that aren't saved yet. ones you've done before still work!"
      : `search broke 💔 ${e.message}`;
  }
}

// PAGE is how many results show at a time.
const PAGE = 20;

// The static search page runs itself.
if (typeof document !== "undefined") {
  const box = document.querySelector(".static-view #search");
  if (box) searchPage(box, new URLSearchParams(location.search));
}
