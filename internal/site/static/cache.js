// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The repository, through a cache: files in the Cache API (so it can hold
// whole sections saved for offline reading), and a background check for
// newly published repositories that clears out the index files they no
// longer use.

import { chapter } from "./manual.js";
import { namePath, listingPath, variantPath, shardsOf, chapterShardsOf, chapterFile, sectionShard, indexPagesOf } from "./format.js";

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages in the browser: a cache of repository files (in the Cache API,
// so it can hold whole sections saved for offline reading), a background
// check for newly published repositories that clears out the search index
// files they no longer use, and the service worker that makes the site an
// app. Offline, the service worker sends static pages to the browse app,
// which renders them from this cache.

const PREFIX = "girlpages:";

// Files named after their own SHA-256 never change.
const IMMUTABLE = /(^|\/|-)[0-9a-f]{64}\.json$/;

// How often to look for a newly published repository.
const CHECK_EVERY = 30 * 60 * 1000;

// Requests at once when saving a section.
const SAVE_JOBS = 6;

// How long to wait for a file that changes, when there's a copy to fall
// back on.
const NETWORK_TIMEOUT = 4000;

async function fetchText(url, init) {
  const r = await fetch(url, init);
  if (!r.ok) {
    throw Object.assign(new Error(`${url}: ${r.status}`), { status: r.status });
  }
  return r.text();
}

// A RepoError is a repository file that couldn't be read. Its kind is
// "offline" (there's no network, and no copy kept), "missing" (the
// repository hasn't it: HTTP 404), or "broken" (anything else: another
// HTTP error, or a file that isn't JSON).
export class RepoError extends Error {
  /**
   * @param {"offline" | "missing" | "broken"} kind
   * @param {string} message
   * @param {{status?: number, cause?: unknown}} [opts]
   */
  constructor(kind, message, { status = 0, cause = undefined } = {}) {
    super(message, { cause });
    this.kind = kind;
    this.status = status;
  }
}

// errorKind is what kind of failure an error is: a RepoError's kind, or
// "broken" for anything else (a bug, say: not "offline", which only a
// fetch can say).
export const errorKind = (e) => (e instanceof RepoError ? e.kind : "broken");

// repoError is what a failed fetch of path means: offline if there was no
// answer (a TypeError, from fetch) or none in time, else by its status.
function repoError(e, path) {
  if (e instanceof RepoError) return e;
  const status = (e && e.status) || 0;
  const kind = status ? (status === 404 ? "missing" : "broken")
    : e instanceof TypeError || e?.name === "AbortError" || e?.name === "TimeoutError" ? "offline" : "broken";
  return new RepoError(kind, `${path}: ${e?.message || e}`, { status, cause: e });
}

// CacheStore keeps files in the Cache API, by path. Its methods never
// throw: it's only a cache.
export class CacheStore {
  constructor(name, origin) {
    this.name = name;
    this.origin = origin; // file paths are stored as URLs under it
    this.cache = null;
  }
  async open() {
    if (!this.cache) this.cache = await globalThis.caches.open(this.name);
    return this.cache;
  }
  url(path) { return this.origin + path; }
  async get(path) {
    try {
      const r = await (await this.open()).match(this.url(path));
      return r ? await r.text() : null;
    } catch {
      return null;
    }
  }
  async put(path, text) {
    try {
      await (await this.open()).put(this.url(path), new Response(text, { headers: { "Content-Type": "application/json" } }));
      return true;
    } catch {
      return false; // probably out of space
    }
  }
  async delete(path) {
    try {
      await (await this.open()).delete(this.url(path));
    } catch {
      // gone anyway, or never there
    }
  }
  async paths() {
    try {
      return (await (await this.open()).keys()).map(r => r.url).filter(u => u.startsWith(this.origin)).map(u => u.slice(this.origin.length));
    } catch {
      return [];
    }
  }
}

// MemoryStore is a store for when there's no Cache API (or for tests).
export class MemoryStore {
  constructor(limit = Infinity) { this.m = new Map(); this.limit = limit; }
  size() { let n = 0; for (const v of this.m.values()) n += v.length; return n; }
  async get(path) { return this.m.has(path) ? this.m.get(path) : null; }
  async put(path, text) {
    if (this.size() - (this.m.get(path) || "").length + text.length > this.limit) return false;
    this.m.set(path, text);
    return true;
  }
  async delete(path) { this.m.delete(path); }
  async paths() { return [...this.m.keys()]; }
}

// Meta is a little state kept in localStorage, if there is one: when the
// manifest was last checked, which one it was, and what's been saved.
class Meta {
  constructor(key, storage) {
    this.key = key;
    this.storage = storage;
    this.mem = {};
  }
  load() {
    try {
      return JSON.parse(this.storage.getItem(this.key)) || {};
    } catch {
      return { ...this.mem };
    }
  }
  save(m) {
    this.mem = m;
    try {
      this.storage.setItem(this.key, JSON.stringify(m));
    } catch {
      // no localStorage: it lasts as long as the page
    }
  }
}

function defaultStore(repo) {
  if (typeof globalThis.caches === "undefined") return new MemoryStore();
  const url = new URL(repo, globalThis.location?.href);
  return new CacheStore("girlpages-repo", url.href);
}

// RepoCache fetches repository files through a store. Files named after
// their hash are read from it without asking the network; the rest come
// from the network when it's there, and from the store when it isn't.
export class RepoCache {
  constructor(repo, { store = defaultStore(repo), fetch: f = fetchText, now = () => Date.now(), storage = globalThis.localStorage } = {}) {
    this.repo = repo.endsWith("/") ? repo : repo + "/";
    this.store = store;
    this.fetchText = f;
    this.now = now;
    this.meta = new Meta(PREFIX + "meta:" + this.repo, storage);
    this.onNewManifest = null; // set by watch()
    this.stale = new Set(); // mutable files given from the store, not yet refreshed
    this.inflight = new Map(); // refreshes under way, by path
  }

  // get returns the parsed JSON at path (relative to the repository). It
  // fails with a RepoError.
  async get(path) {
    const text = IMMUTABLE.test(path) ? await this.immutable(path) : await this.mutable(path);
    try {
      return JSON.parse(text);
    } catch (e) {
      throw new RepoError("broken", `${path}: ${e.message}`, { cause: e });
    }
  }

  // fetch fetches a file from the repository, failing with a RepoError.
  async fetch(path, init) {
    try {
      return await this.fetchText(this.repo + path, init);
    } catch (e) {
      throw repoError(e, path);
    }
  }

  async immutable(path) {
    const hit = await this.store.get(path);
    if (hit !== null) return hit;
    const text = await this.fetch(path);
    await this.store.put(path, text);
    return text;
  }

  // peek returns the parsed JSON of a file named after its hash, from the
  // store if it's there, but without keeping it if it isn't: for reading
  // many pages' text once (looking for a phrase), not to fill the store.
  async peek(path) {
    const text = (await this.store.get(path)) ?? await this.fetch(path);
    try {
      return JSON.parse(text);
    } catch (e) {
      throw new RepoError("broken", `${path}: ${e.message}`, { cause: e });
    }
  }

  // mutable files come straight from the store if they're there, while
  // they're refreshed in the background; otherwise from the network.
  // fresh asks the network first (giving up quickly if there's a copy).
  async mutable(path, { fresh = false } = {}) {
    const hit = await this.store.get(path);
    if (hit !== null && !fresh) {
      this.stale.add(path);
      this.refresh(path).catch(() => {});
      return hit;
    }
    try {
      return await this.fetchMutable(path, hit !== null);
    } catch (e) {
      if (hit !== null && errorKind(e) === "offline") return hit; // use what we have
      throw e;
    }
  }

  async fetchMutable(path, quick) {
    const init = { cache: "no-cache" };
    if (quick && typeof AbortSignal !== "undefined" && AbortSignal.timeout) init.signal = AbortSignal.timeout(NETWORK_TIMEOUT);
    const text = await this.fetch(path, init);
    await this.store.put(path, text);
    this.stale.delete(path);
    if (path === "manifest.json") this.sawManifest(text);
    return text;
  }

  // refresh fetches a mutable file again, once at a time.
  refresh(path) {
    if (!this.inflight.has(path)) {
      this.inflight.set(path, this.fetchMutable(path, false).finally(() => this.inflight.delete(path)));
    }
    return this.inflight.get(path);
  }

  // revalidate waits for every mutable file given from the store to be
  // refreshed, reporting whether there were any: for when one of them led
  // to a file that's since gone.
  async revalidate() {
    const paths = [...this.stale];
    await Promise.all(paths.map(p => this.refresh(p).catch(() => {})));
    return paths.length > 0;
  }

  // settled waits for refreshes under way.
  async settled() {
    await Promise.all([...this.inflight.values()].map(p => p.catch(() => {})));
  }

  // sawManifest notes a manifest fetched from the network and, if it's not
  // the one seen last time, has the cache collect garbage.
  sawManifest(text) {
    const m = this.meta.load();
    m.checked = this.now();
    let manifest;
    try {
      manifest = JSON.parse(text);
    } catch {
      return;
    }
    const id = manifest.generated + " " + JSON.stringify(manifest.index);
    const fresh = m.manifest !== id;
    m.manifest = id;
    this.meta.save(m);
    if (fresh && this.onNewManifest) this.onNewManifest(manifest);
  }

  // gc removes the search index files and section indexes the manifest no
  // longer uses: they're big, and superseded by each publish. Everything
  // else is named after its hash or replaced in place, so it stays (pages
  // read or saved are still good for reading offline). It returns how many
  // files it removed.
  async gc(manifest) {
    const live = new Set();
    const ix = manifest.index || {};
    for (const kind of ["words", "grams", "names", "docs"]) {
      const sha = ix[kind];
      if (!sha) continue;
      const path = `index/${kind}/manifest-${sha}.json`;
      live.add(path);
      const text = await this.store.get(path);
      if (text === null) continue; // so none of its shards are stored either
      for (const r of shardsOf(JSON.parse(text))) live.add(`index/${kind}/shard-${r.sha256.slice(0, 2)}/${r.sha256}.json`);
    }
    for (const e of manifest.chapters || []) {
      const path = chapterFile(e);
      live.add(path);
      const text = await this.store.get(path);
      if (text !== null) for (const r of chapterShardsOf(JSON.parse(text))) live.add(sectionShard(r.sha256));
    }
    let removed = 0;
    for (const p of await this.store.paths()) {
      if ((p.startsWith("index/") || (p.startsWith("section/") && IMMUTABLE.test(p))) && !live.has(p)) {
        await this.store.delete(p);
        removed++;
      }
    }
    return removed;
  }

  // check fetches manifest.json if it hasn't been checked for a while,
  // which collects garbage if it's new.
  async check(force = false) {
    if (!force && this.now() - (this.meta.load().checked || 0) < CHECK_EVERY) return;
    await this.mutable("manifest.json", { fresh: true });
  }

  // watch checks for new manifests now and then while the page is open,
  // collecting garbage in idle time when one turns up.
  watch() {
    this.onNewManifest = (m) => idle(() => this.gc(m).catch(() => {}));
    const tick = () => {
      if (document.visibilityState === "visible") idle(() => this.check().catch(() => {}));
    };
    tick();
    setInterval(tick, CHECK_EVERY);
    document.addEventListener("visibilitychange", tick);
  }

  // save fetches everything needed to read a section's pages offline: each
  // page's pointer and listing, and the girly variant of the version shown
  // (the original where there's no girly one). key is a main section ("3",
  // for 3, 3p, 3ssl…) or an exact one; src keeps to the versions a source
  // has. progress(done, total) is called as pages finish. It resolves to
  // {pages, failed}.
  /** @param {string} key @param {{src?: string, progress?: (done: number, total: number) => void}} [opts] */
  async save(key, { src = "", progress = () => {} } = {}) {
    const m = JSON.parse(await this.mutable("manifest.json", { fresh: true }));
    const exact = chapter(key) !== key;
    const e = (m.chapters || []).find(c => c.chapter === chapter(key));
    const pages = [];
    for (const p of e ? await this.chapterPages(e) : []) {
      if ((exact && p.section !== key) || (src && !p.sources.includes(src))) continue;
      pages.push({ name: p.name, sec: p.section, page: p.page });
    }
    let done = 0, failed = 0, next = 0;
    progress(0, pages.length);
    const one = (p) => this.keepPage(p.name, p.sec, { page: p.page, src });
    const worker = async () => {
      while (next < pages.length) {
        const p = pages[next++];
        try {
          await one(p);
        } catch (e) {
          if (errorKind(e) === "offline") throw e; // stop
          failed++;
        }
        progress(++done, pages.length);
      }
    };
    await Promise.all(Array.from({ length: Math.min(SAVE_JOBS, pages.length) }, worker));
    const meta = this.meta.load();
    meta.saved = { ...(meta.saved || {}), [savedKey(key, src)]: { generated: m.generated, pages: pages.length, failed } };
    this.meta.save(meta);
    return { pages: pages.length, failed };
  }

  // keepPage fetches what's needed to read a page offline: its pointer,
  // listing, and the girly variant of a version (the latest, or a source's
  // if src says), or the original where there's no girly one. page, the
  // listing's hash, saves asking the pointer.
  async keepPage(name, sec, { page = "", src = "" } = {}) {
    const ptr = JSON.parse(await this.mutable(namePath(name, sec), { fresh: true }));
    page = page || ptr.sha256;
    const l = await this.get(listingPath(page));
    const source = (src && l.sources && l.sources[src]) || l.current;
    const ver = (l.versions || []).find(v => v.source === source);
    if (!ver) return;
    const vs = ver.variants || {};
    const which = vs.girl ? "girl" : "man";
    if (vs[which]) await this.get(variantPath(source, which, vs[which].sha256));
  }

  // chapterIndex is a chapter's index (see repo.ChapterIndex), from its
  // manifest entry.
  chapterIndex(e) { return this.get(chapterFile(e)); }

  // chapterChunk is chunk n (from 1) of a chapter's index: its own pages
  // for 1, else a shard's.
  async chapterChunk(ci, n) {
    if (n === 1) return indexPagesOf(ci.pages);
    const r = chapterShardsOf(ci)[n - 2];
    return indexPagesOf((await this.get(sectionShard(r.sha256))).pages);
  }

  // chapterPages is every page in a chapter's index.
  async chapterPages(e) {
    const ci = await this.chapterIndex(e);
    const shards = await Promise.all(chapterShardsOf(ci).map(r => this.get(sectionShard(r.sha256))));
    return [...indexPagesOf(ci.pages), ...shards.flatMap(sh => indexPagesOf(sh.pages))];
  }

  // searchFiles lists the search index files a part of search needs
  // offline, with their sizes (0 where the repository doesn't say):
  // "words" for searching (the word index and page table), "typos" for
  // catching typos (the trigram index).
  async searchFiles(part, m) {
    const ix = m.index || {};
    const out = [];
    for (const kind of SEARCH_PARTS[part]) {
      const im = await this.get(`index/${kind}/manifest-${ix[kind]}.json`);
      for (const s of shardsOf(im)) out.push({ path: `index/${kind}/shard-${s.sha256.slice(0, 2)}/${s.sha256}.json`, bytes: s.bytes || 0 });
    }
    return out;
  }

  // searchSize is how many bytes a part of search is, if the repository
  // says.
  async searchSize(part) {
    const files = await this.searchFiles(part, await this.get("manifest.json"));
    return files.reduce((n, f) => n + f.bytes, 0);
  }

  // saveSearch fetches a part of the search index ("words" or "typos"), so
  // searches work offline. progress(done, total) is called as files arrive.
  /** @param {"words" | "typos"} part @param {{progress?: (done: number, total: number) => void}} [opts] */
  async saveSearch(part, { progress = () => {} } = {}) {
    const m = JSON.parse(await this.mutable("manifest.json", { fresh: true }));
    // Search needs every part's manifest (they're small) to start.
    const ix = m.index || {};
    await Promise.all(["words", "grams", "names", "docs"].map(k => this.get(`index/${k}/manifest-${ix[k]}.json`)));
    const files = (await this.searchFiles(part, m)).map(f => f.path);
    let done = 0, next = 0;
    progress(0, files.length);
    const worker = async () => {
      while (next < files.length) {
        await this.immutable(files[next++]);
        progress(++done, files.length);
      }
    };
    await Promise.all(Array.from({ length: Math.min(SAVE_JOBS, files.length) }, worker));
    const meta = this.meta.load();
    meta.saved = { ...(meta.saved || {}), ["search:" + part]: { generated: m.generated, pages: files.length, failed: 0 } };
    this.meta.save(meta);
    return { pages: files.length, failed: 0 };
  }

  // hasPage reports whether a page can be read offline: its pointer,
  // listing and the variant shown (of the latest version, or src's) are
  // all in the store.
  async hasPage(name, sec, src = "") {
    try {
      const ptr = await this.store.get(namePath(name, sec));
      if (ptr === null) return false;
      const sha = JSON.parse(ptr).sha256;
      const lt = await this.store.get(listingPath(sha));
      if (lt === null) return false;
      const l = JSON.parse(lt);
      const source = (src && l.sources && l.sources[src]) || l.current;
      const ver = (l.versions || []).find(v => v.source === source);
      const vs = (ver && ver.variants) || {};
      const which = vs.girl ? "girl" : "man";
      return !!vs[which] && await this.store.get(variantPath(source, which, vs[which].sha256)) !== null;
    } catch {
      return false;
    }
  }

  // saved says whether a section has been saved, and from which publish.
  saved(key, src = "") {
    return (this.meta.load().saved || {})[savedKey(key, src)] || null;
  }
}

const savedKey = (key, src) => (src ? src + " " : "") + key;

// The parts of search that can be saved for offline use, and the index
// files each needs. saveButton takes "search:<part>" as a key.
const SEARCH_PARTS = { words: ["words", "names", "docs"], typos: ["grams"] };

export function idle(fn) {
  if (typeof requestIdleCallback === "function") requestIdleCallback(fn, { timeout: 10000 });
  else setTimeout(fn, 1000);
}

// One cache per repository per page.
const repoCaches = new Map();

export function repoCache(repo) {
  if (!repoCaches.has(repo)) repoCaches.set(repo, new RepoCache(repo));
  return repoCaches.get(repo);
}
