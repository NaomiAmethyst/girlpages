// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages search: queries a repository's sharded index straight from the
// browser, fetching only the shards a query needs. This is the same
// algorithm as Searcher in internal/repo/search.go; keep them in step.

import { repoCache, errorKind } from "./cache.js";
import { shardsOf, rows, namePath, listingPath, variantPath } from "./format.js";
import { browseURL, pageURL } from "./urls.js";
import { chapter } from "./manual.js";
import { sourceNav } from "./widgets.js";
import { html } from "./html.js";

const PREFIX_WEIGHT = 0.5;
const PREFIX_CAP = 100; // as in search.go
const FUZZY_WEIGHT = 0.3;
const MAX_PREFIX_WORDS = 50;
const MAX_FUZZY_WORDS = 5;
// In ngrams mode (see search.go), words a query word is inside of.
const INFIX_WEIGHT = PREFIX_WEIGHT;
const MAX_INFIX_WORDS = 100;

// MAX_POSTINGS is how many pages the index lists per word (repo's
// maxPostings): a word listing fewer is listed for every page with it.
const MAX_POSTINGS = 400;
// PACKAGE_PREFIX starts a package's key in the words index, as in Go.
const PACKAGE_PREFIX = "pkg:";
// STOPWORDS are left out of the index, as in Go's index.go.
const STOPWORDS = new Set(`a an and are as at be but by for from has have if in into is it its
  no not of on or so such that the their then there these they this to was were will with you your
  can may must should which when where who also only than other any all each more most some`.split(/\s+/));

// parseQuery is repo.ParseQuery: {words, phrases, packages}. Phrases are in
// double quotes (an unclosed one runs to the end), as their tokens; a
// package is pkg:name or package:name (or distro/name).
export function parseQuery(q) {
  const phrases = [], packages = [];
  q = q.replace(/"([^"]*)("|$)/g, (_, p) => {
    const ts = tokens(p);
    if (ts.length) phrases.push(ts);
    return " ";
  });
  q = q.replace(/(^|\s)(?:pkg|package):(\S+)/gi, (_m, _sp, k) => {
    k = k.toLowerCase();
    if (!packages.includes(k)) packages.push(k);
    return " ";
  });
  return { words: q.trim(), phrases, packages };
}

// hasPhrase is repo.HasPhrase: whether text's tokens have phrase's in a
// row, words man broke across lines with a hyphen found either way.
export function hasPhrase(text, phrase) {
  if (!phrase.length) return true;
  if (inRow(tokens(text), phrase)) return true;
  const joined = text.replace(/(\p{L})-\n\s*(\p{L})/gu, "$1$2");
  return joined !== text && inRow(tokens(joined), phrase);
}

function inRow(words, phrase) {
  outer: for (let i = 0; i + phrase.length <= words.length; i++) {
    for (let j = 0; j < phrase.length; j++) if (words[i + j] !== phrase[j]) continue outer;
    return true;
  }
  return false;
}

// phraseCandidates is repo's: given each searchable phrase word's postings
// ([id, score] pairs), the pages the phrases could be in, scored, and
// whether that's partial (every word too common to list every page). A
// null set means any page could be.
export function phraseCandidates(lists) {
  if (!lists.length) return { pages: null, partial: false };
  const score = new Map(), count = new Map(), inAll = new Map();
  let complete = 0;
  for (const ps of lists) {
    const whole = ps.length < MAX_POSTINGS;
    if (whole) complete++;
    for (const [id, sc] of ps) {
      score.set(id, (score.get(id) || 0) + sc);
      count.set(id, (count.get(id) || 0) + 1);
      if (whole) inAll.set(id, (inAll.get(id) || 0) + 1);
    }
  }
  const pages = new Map();
  if (complete) {
    for (const [id, n] of inAll) if (n === complete) pages.set(id, score.get(id));
    return { pages, partial: false };
  }
  for (const [id, n] of count) if (n === lists.length) pages.set(id, score.get(id));
  if (!pages.size) {
    for (const [id, n] of count) pages.set(id, score.get(id) * n / lists.length);
  }
  return { pages, partial: true };
}

// Search modes, as search.go's: words, or ngrams (parts of words too).
export const MODES = ["words", "ngrams"];

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
  // repository's cache (see cache.js).
  constructor(repo, cache = repoCache(repo)) {
    this.cache = cache;
    this.shards = new Map();
    /** @type {Promise<void> | null} */
    this.loaded = null;
    // What load reads: the manifest, and the index's manifests' shards.
    /** @type {any} */
    this.manifest = null;
    /** @type {any[]} */
    this.words = [];
    /** @type {any[]} */
    this.grams = [];
    /** @type {any[]} */
    this.names = [];
    /** @type {any[]} */
    this.docs = [];
    // Words the index lists only some pages for (see repo.BuiltIndex.Common).
    /** @type {string[]} */
    this.wordsCommon = [];
    // What the last rank left to check, and how it went (see rank).
    /** @type {string[][]} */
    this.phrases = [];
    this.partial = false;
    this.tooCommon = false;
    this.missedPhrases = false;
    this.missedTypos = false;
    // The page table's shards, as they're read.
    /** @type {Map<string, Promise<{first: number, rows: any[]}>>} */
    this.tables = new Map();
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
        this.wordsCommon = words.common || [];
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
    const had = this.tables.get(key);
    if (had) return had;
    const p = this.shard(kind, r.sha256).then(sh => ({ first: sh.first, rows: rows(sh[kind]) }));
    p.catch(() => this.tables.delete(key));
    this.tables.set(key, p);
    return p;
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
      const keys = Object.keys(sh.words).filter(w => w !== p && w.startsWith(p) && !w.startsWith(PACKAGE_PREFIX)).sort(cmp);
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
        if (errorKind(e) !== "offline") throw e;
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

  // innerGrams is a word's trigrams but those marking its start and end.
  static innerGrams(t) {
    const rs = [...t], out = new Set();
    for (let i = 0; i + 3 <= rs.length; i++) out.add(rs.slice(i, i + 3).join(""));
    return [...out];
  }

  // infixWords lists indexed words (of those the trigram index has) that t
  // is inside of, but isn't, those in the most pages first, as in Go.
  async infixWords(t) {
    const gs = Searcher.innerGrams(t);
    if (!gs.length) return [];
    /** @type {Map<string, number> | null} */
    let cands = null;
    for (const g of gs) {
      const r = findRange(this.grams, g);
      if (!r) return [];
      let sh;
      try {
        sh = await this.shard("grams", r.sha256);
      } catch (e) {
        // Offline without typo matching saved: whole words only.
        if (errorKind(e) !== "offline") throw e;
        this.missedTypos = true;
        return [];
      }
      const ws = Object.hasOwn(sh.grams, g) ? sh.grams[g] : [], ns = sh.pages[g] || [];
      /** @type {Map<string, number>} */
      const next = new Map();
      ws.forEach((w, i) => { if (!cands || cands.has(w)) next.set(w, i < ns.length ? ns[i] : 0); });
      cands = next;
      if (!cands.size) return [];
    }
    const cs = [...(cands || [])].filter(([w]) => w !== t && w.includes(t));
    cs.sort((a, b) => b[1] - a[1] || cmp(a[0], b[0]));
    return cs.slice(0, MAX_INFIX_WORDS).map(c => c[0]);
  }

  async term(t, mode = "words") {
    const out = new Map();
    const exact = await this.postings(t);
    for (const [id, sc] of exact) addBest(out, id, sc);
    for (const w of await this.prefixWords(t)) {
      for (const [id, sc] of await this.postings(w)) addBest(out, id, Math.min(sc, PREFIX_CAP) * PREFIX_WEIGHT);
    }
    if (mode === "ngrams" && [...t].length >= 3) {
      const lt = [...t].length;
      for (const w of await this.infixWords(t)) {
        // The more of the word it is, the better, as in Go.
        const fit = lt / [...w].length;
        for (const [id, sc] of await this.postings(w)) addBest(out, id, sc * INFIX_WEIGHT * fit);
      }
      return out;
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
  async search(query, limit = 50, source = "", accept = null, mode = "words") {
    const ranked = await this.rank(query, mode);
    accept = this.withPhrases(accept);
    if (!source && !accept) {
      const top = limit > 0 ? ranked.slice(0, limit) : ranked;
      return Promise.all(top.map(async ([id, score]) => hit(await this.doc(id), score)));
    }
    return (await this.filtered(ranked, 0, limit, source, accept, this.maxChecks())).hits;
  }

  // filtered walks down the ranking from start, a batch at a time, for up
  // to limit hits from source that accept (if given) takes, resolving to
  // {hits, next}: where to go on from (-1 at the end).
  /**
   * @param {any[]} ranked @param {number} start @param {number} limit @param {string} [source]
   * @param {((h: any) => boolean | Promise<boolean>) | null} [accept] @param {number} [max]
   */
  async filtered(ranked, start, limit, source = "", accept = null, max = MAX_FILTERED) {
    const hits = [];
    let i = start;
    for (; i < ranked.length && i < max && (limit <= 0 || hits.length < limit); i += 64) {
      const batch = ranked.slice(i, Math.min(i + 64, max));
      const docs = await Promise.all(batch.map(([id]) => this.doc(id)));
      const ok = await Promise.all(docs.map(d => (!source || d[3].split(" ").includes(source)) && (!accept || accept(hit(d, 0)))));
      docs.forEach((d, j) => {
        if ((limit <= 0 || hits.length < limit) && ok[j]) hits.push(hit(d, batch[j][1]));
      });
    }
    return { hits, next: i < ranked.length && i < max ? i : -1 };
  }

  // maxChecks is how far down the last ranking filtered should look: not
  // so far, when each page's text is to be read for a phrase.
  maxChecks() { return this.phrases.length ? MAX_PHRASE_CHECKS : MAX_FILTERED; }

  // rank scores pages for a query, best first, as [id, score]s; mode is
  // words or ngrams. It leaves what's still to check in this.phrases (the
  // query's phrases, to look for in the pages' text: see withPhrases),
  // and says in this.partial whether they were looked for in only some
  // pages, and in this.tooCommon whether nothing in the query could be.
  async rank(query, mode = "words") {
    this.missedTypos = false;
    await this.load();
    const q = parseQuery(query);
    this.phrases = q.phrases;
    this.partial = false;
    this.tooCommon = false;
    const terms = [...new Set(seriesTerms(tokens(q.words), this.manifest))];
    const whole = q.words.toLowerCase();

    // Pages a phrase could be in, and from the packages asked for (null:
    // any).
    /** @type {Map<number, number> | null} */
    let must = null;
    if (q.phrases.length) {
      const r = await this.phrasePages(q.phrases);
      must = r.pages;
      this.partial = r.partial;
    }
    /** @type {Set<number> | null} */
    let pkgs = null;
    if (q.packages.length) {
      pkgs = new Set();
      for (const k of q.packages) for (const [id] of await this.postings(PACKAGE_PREFIX + k)) pkgs.add(id);
    }
    // A package's pages are all listed, so with one, every page from it
    // can be looked through for the phrases, as in Go.
    const scores = must;
    if (pkgs && this.partial) {
      must = null;
      this.partial = false;
    }
    const allowed = (id) => (!must || must.has(id)) && (!pkgs || pkgs.has(id));
    const extra = (id) => (scores && scores.get(id)) || 0;

    let res = new Map();
    if (!terms.length && !whole) {
      if (must) {
        for (const [id, sc] of must) if (allowed(id)) res.set(id, sc);
      } else if (pkgs) {
        // Just a package: its pages, in the page table's order.
        for (const id of pkgs) res.set(id, extra(id));
      } else {
        this.tooCommon = q.phrases.length > 0;
        return [];
      }
    } else {
      const perTerm = await Promise.all(terms.map(t => this.term(t, mode)));
      const bonus = new Map();
      if (whole && !/[ \t]/.test(whole)) {
        for (const [id, sc] of await this.postings(whole)) if (allowed(id)) addBest(bonus, id, sc);
      }
      const combine = (all) => {
        const out = new Map();
        for (const [id, sc] of bonus) out.set(id, sc + extra(id));
        const seen = new Set();
        for (const pt of perTerm) {
          for (const id of pt.keys()) {
            if (seen.has(id)) continue;
            seen.add(id);
            if (!allowed(id)) continue;
            let total = 0, n = 0;
            for (const other of perTerm) {
              if (other.has(id)) { total += other.get(id); n++; }
            }
            if (all && n < perTerm.length) continue;
            out.set(id, total + (bonus.get(id) || 0) + extra(id));
          }
        }
        return out;
      };
      res = combine(true);
      if (res.size === bonus.size) res = combine(false);
    }
    if (q.phrases.length && res.size > MAX_PHRASE_CHECKS) this.partial = true;
    return [...res].sort((a, b) => b[1] - a[1] || a[0] - b[0]);
  }

  // phrasePages is repo's: the pages phrases could be in, by the words of
  // them the index can say where they are.
  async phrasePages(phrases) {
    const common = new Set(this.wordsCommon || []);
    const words = [...new Set(phrases.flat())].filter(w => !STOPWORDS.has(w) && !common.has(w));
    const lists = await Promise.all(words.map(w => this.postings(w)));
    if (lists.some(ps => !ps.length)) return { pages: new Map(), partial: false };
    return phraseCandidates(lists);
  }

  // hasPhrases reports whether a page's text (any version's) has every
  // phrase. The text is read without keeping it, unless it's kept already.
  async hasPhrases(name, sec, phrases) {
    const ptr = await this.cache.get(namePath(name, sec));
    const l = await this.cache.get(listingPath(ptr.sha256));
    const texts = await Promise.all((l.versions || []).map(async v => {
      const vs = v.variants || {};
      const which = vs.man ? "man" : vs.girl ? "girl" : "";
      if (!which) return "";
      const path = variantPath(v.source, which, vs[which].sha256);
      const doc = this.cache.peek ? await this.cache.peek(path) : await this.cache.get(path);
      return (doc.sections || []).map(s => (s.heading || "") + "\n" + (s.body || "")).join("");
    }));
    const text = texts.join("\n\n");
    return phrases.every(p => hasPhrase(text, p));
  }

  // withPhrases is accept (a hit filter, or null) also keeping to hits
  // with the last ranked query's phrases. A page that can't be read (being
  // offline) is left out, and said in this.missedPhrases.
  withPhrases(accept) {
    const phrases = this.phrases || [];
    if (!phrases.length) return accept;
    this.missedPhrases = false;
    return async (h) => {
      if (accept && !(await accept(h))) return false;
      try {
        return await this.hasPhrases(h.name, h.section, phrases);
      } catch (e) {
        if (errorKind(e) === "offline") this.missedPhrases = true;
        else if (errorKind(e) !== "missing") throw e;
        return false;
      }
    };
  }
}

const MAX_FILTERED = 2000;
// MAX_PHRASE_CHECKS is how many pages' text a phrase is looked for in, as
// repo's maxPhraseChecks.
const MAX_PHRASE_CHECKS = 500;

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
      if (errorKind(e) !== "missing") return null;
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
/**
 * @param {HTMLElement} box @param {URLSearchParams} params
 * @param {{current?: () => boolean, signal?: AbortSignal | null, go?: (url: string) => void}} [opts]
 */
export async function searchPage(box, params, { current = () => true, signal = null, go = (u) => location.replace(u) } = {}) {
  // The page's parts, which the search page (static or the app's) has.
  const $ = (id) => /** @type {HTMLElement} */ (box.querySelector("#" + id));
  const status = $("search-status");
  const list = $("search-results");
  const base = box.dataset.base;
  const q = params.get("q") || "", src = params.get("src") || "";
  // Whole words (and those they start), or parts of words too.
  const mode = params.get("mode") === "ngrams" ? "ngrams" : "words";
  const modes = $("search-mode");
  if (modes) {
    const link = (m) => {
      const u = new URLSearchParams(params);
      u.delete("saved");
      if (m === "words") u.delete("mode");
      else u.set("mode", m);
      return `${base}search/?${u}`;
    };
    modes.innerHTML = String(html`<a class="pill${mode === "words" ? " on" : ""}" href="${link("words")}" title="whole words, and words they start">📝 words</a>
      <a class="pill${mode === "ngrams" ? " on" : ""}" href="${link("ngrams")}" title="words they're anywhere inside of: dir finds mkdir">🧩 parts of words</a>`);
  }
  // The search box at the top keeps to the mode.
  for (const f of /** @type {NodeListOf<HTMLFormElement>} */ (document.querySelectorAll("form.search"))) {
    let m = /** @type {HTMLInputElement | null} */ (f.querySelector('input[name="mode"]'));
    if (mode === "words") { m?.remove(); continue; }
    if (!m) {
      m = document.createElement("input");
      m.type = "hidden";
      m.name = "mode";
      f.append(m);
    }
    m.value = mode;
  }
  // Only pages saved for offline: asked for, or by default when offline.
  const savedOnly = params.has("saved") ? params.get("saved") === "1" : navigator.onLine === false;
  const toggle = /** @type {HTMLAnchorElement | null} */ (box.querySelector("#search-saved"));
  if (toggle && "caches" in globalThis) {
    const u = new URLSearchParams(params);
    u.set("saved", savedOnly ? "0" : "1");
    toggle.href = `${base}search/?${u}`;
    toggle.textContent = "📴 only saved pages";
    toggle.classList.toggle("on", savedOnly);
    toggle.hidden = false;
  }
  for (const input of /** @type {NodeListOf<HTMLInputElement>} */ (document.querySelectorAll('input[name="q"]'))) input.value = q;
  const s = searcher(box.dataset.repo);
  try {
    await s.load();
    const chips = $("search-sources");
    const href = (key) => {
      const u = new URLSearchParams({ q });
      if (key) u.set("src", key);
      if (mode !== "words") u.set("mode", mode);
      return `${base}search/?${u}`;
    };
    if (chips && s.manifest.sources && s.manifest.sources.length) {
      chips.innerHTML = String(sourceNav(s.manifest.sources, { on: src, href, all: "✨ everywhere" }));
    }
  } catch {
    // the search itself will say what's wrong
  }
  if (!q.trim()) return;
  const where = src ? ` in ${src.replace("/", " ")}` : "";
  // The query as it's shown: in quotes, unless it has its own.
  const said = q.includes('"') ? q : `“${q}”`;
  if (current()) document.title = `${q}${where} · search · girlpages 💅`;
  status.textContent = "looking… 🔎";
  const saved = savedOnly ? " saved" : "";
  try {
    const keepSaved = savedOnly ? (h) => s.cache.hasPage(h.name, h.section, src) : null;
    s.prefetchNames().catch(() => {});
    const direct = await directPage(q, s).catch(() => null);
    if (direct && current()) {
      return go(src ? browseURL(base, { page: direct.name, sec: direct.sec, src }) : pageURL(base, direct.name, direct.sec));
    }
    const ranked = await s.rank(q, mode);
    if (!current()) return;
    // Phrases in quotes are looked for in each page's text as it comes.
    const accept = s.withPhrases(keepSaved);
    list.replaceChildren();
    const typos = () => (!s.missedTypos ? "" : mode === "ngrams"
      ? " (typo matching isn't saved, so it's whole words only)" : " (typo matching isn't saved, so typos won't be caught)");
    // What a phrase search couldn't do.
    const notes = () => typos()
      + (s.partial ? " (the words in quotes are in so many pages that only the likeliest were looked through)" : "")
      + (s.missedPhrases ? " (some pages aren't saved, so they couldn't be looked through offline)" : "");
    const none = () => {
      status.textContent = s.tooCommon
        ? `every word in those quotes is in nearly every page, so there's nothing to go on 😭 add a rarer one?`
        : `nothing${saved} for ${said}${where}, bestie 😭 try another word?${notes()}`;
    };
    if (!ranked.length) return none();
    const plural = (n) => `${n.toLocaleString()}${saved} page${n === 1 ? "" : "s"}`;
    // Results show a screenful at a time, each as soon as what it says
    // about itself (in the page table, a shard per 128 pages) arrives,
    // so the first appear long before the rest would have.
    const filter = Boolean(src || accept);
    let shown = 0, next = 0, more = true, busy = false;
    status.textContent = filter ? `looking through ${plural(Math.min(ranked.length, s.maxChecks())).replace(saved, "")} for ${said}${where}… 🔎${notes()}`
      : `${plural(ranked.length)} for ${said}${where} ✨${typos()}`;
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
              /** @type {HTMLElement} */ (li.querySelector(".desc")).textContent = description;
            } catch {
              li.remove();
            }
          }));
          shown += batch.length;
        } else {
          const r = await s.filtered(ranked, next, PAGE, src, accept, s.maxChecks());
          if (!current()) return;
          for (const h of r.hits) fill(row(), h);
          shown += r.hits.length;
          next = r.next;
          more = next >= 0;
          status.textContent = shown ? `${more ? "the top " : ""}${plural(shown)} for ${said}${where} ✨${notes()}` : "";
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
    status.textContent = errorKind(e) === "offline"
      ? "you're offline 📵 and this search needs bits that aren't saved yet. ones you've done before still work!"
      : `search broke 💔 ${e.message}`;
  }
}

// PAGE is how many results show at a time.
const PAGE = 20;

// The static search page runs itself.
if (typeof document !== "undefined") {
  const box = /** @type {HTMLElement | null} */ (document.querySelector(".static-view #search"));
  if (box) searchPage(box, new URLSearchParams(location.search));
}
