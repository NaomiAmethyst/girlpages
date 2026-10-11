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
    const e = new Error(`${url}: ${r.status}`);
    e.status = r.status;
    throw e;
  }
  return r.text();
}

// escapeName matches repo.EscapeName: how a page name or section appears in
// a repository path.
export function escapeName(s) {
  const bytes = new TextEncoder().encode(s);
  let out = "";
  for (let i = 0; i < bytes.length; i++) {
    const c = bytes[i];
    const ch = String.fromCharCode(c);
    const ok = /[A-Za-z0-9_+\-.,:@]/.test(ch) && c < 0x80;
    if (!ok || (i === 0 && ch === ".")) out += "%" + c.toString(16).toUpperCase().padStart(2, "0");
    else out += ch;
  }
  return out;
}
// enc is how a name or section appears in a repository URL: escaped as a
// file name, then percent-encoded, as the browse app writes paths.
const enc = (s) => encodeURIComponent(escapeName(s));

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
  }

  // get returns the parsed JSON at path (relative to the repository).
  async get(path) {
    const text = IMMUTABLE.test(path) ? await this.immutable(path) : await this.mutable(path);
    return JSON.parse(text);
  }

  async immutable(path) {
    const hit = await this.store.get(path);
    if (hit !== null) return hit;
    const text = await this.fetchText(this.repo + path);
    await this.store.put(path, text);
    return text;
  }

  // mutable files come from the network when it's there (and quick, if
  // there's a copy to fall back on), else the store.
  async mutable(path) {
    const hit = await this.store.get(path);
    const init = { cache: "no-cache" };
    if (hit !== null && typeof AbortSignal !== "undefined" && AbortSignal.timeout) init.signal = AbortSignal.timeout(NETWORK_TIMEOUT);
    let text;
    try {
      text = await this.fetchText(this.repo + path, init);
    } catch (e) {
      if (hit !== null && e.status === undefined) return hit; // offline: use what we have
      throw e;
    }
    await this.store.put(path, text);
    if (path === "manifest.json") this.sawManifest(text);
    return text;
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
    for (const [kind, sha] of [["words", ix.words], ["grams", ix.grams], ["docs", ix.docs]]) {
      if (!sha) continue;
      const path = `index/${kind}/manifest-${sha}.json`;
      live.add(path);
      const text = await this.store.get(path);
      if (text === null) continue; // so none of its shards are stored either
      const m = JSON.parse(text);
      const shas = kind === "docs" ? m.shards : m.shards.map(s => s.sha256);
      for (const s of shas) live.add(`index/${kind}/shard-${s.slice(0, 2)}/${s}.json`);
    }
    for (const e of manifest.sections || []) live.add(`section/${enc(e.section)}-${e.sha256}.json`);
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
    await this.mutable("manifest.json");
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
  async save(key, { src = "", progress = () => {} } = {}) {
    const m = await this.get("manifest.json");
    const exact = key.length > 1;
    const entries = (m.sections || []).filter(e => exact ? e.section === key : e.section[0] === key[0]);
    const pages = [];
    for (const e of entries) {
      const si = await this.get(`section/${enc(e.section)}-${e.sha256}.json`);
      for (const p of si.pages) {
        if (src && !(p.sources || []).includes(src)) continue;
        pages.push({ name: p.name, sec: si.section, page: p.page, source: (src && p.other && p.other[src]) || "" });
      }
    }
    let done = 0, failed = 0, next = 0;
    progress(0, pages.length);
    const one = async (p) => {
      await this.mutable(`page/name-${enc(p.sec)}/${enc(p.name)}.json`);
      const l = await this.get(`page/page-${p.page.slice(0, 2)}/${p.page}.json`);
      const source = p.source || l.current;
      const ver = (l.versions || []).find(v => v.source === source);
      if (!ver) return;
      const vs = ver.variants || {};
      const which = vs.girl ? "girl" : "man";
      if (vs[which]) await this.get(`contents/${source.slice(0, 2)}/${source}/${which}-${vs[which].sha256}.json`);
    };
    const worker = async () => {
      while (next < pages.length) {
        const p = pages[next++];
        try {
          await one(p);
        } catch (e) {
          if (e instanceof TypeError) throw e; // offline: stop
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

  // saveSearch fetches the whole search index, so any search works offline.
  // progress(done, total) is called as files arrive.
  async saveSearch({ progress = () => {} } = {}) {
    const m = await this.get("manifest.json");
    const ix = m.index || {};
    const files = [];
    for (const [kind, sha] of [["words", ix.words], ["grams", ix.grams], ["docs", ix.docs]]) {
      const im = await this.get(`index/${kind}/manifest-${sha}.json`);
      const shas = kind === "docs" ? im.shards : im.shards.map(s => s.sha256);
      for (const s of shas) files.push(`index/${kind}/shard-${s.slice(0, 2)}/${s}.json`);
    }
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
    meta.saved = { ...(meta.saved || {}), [SEARCH]: { generated: m.generated, pages: files.length, failed: 0 } };
    this.meta.save(meta);
    return { pages: files.length, failed: 0 };
  }

  // saved says whether a section has been saved, and from which publish.
  saved(key, src = "") {
    return (this.meta.load().saved || {})[savedKey(key, src)] || null;
  }
}

const savedKey = (key, src) => (src ? src + " " : "") + key;
// SEARCH is what's saved when the search index is: SaveButton's key for it.
export const SEARCH = "search";

// saveButton makes a button save a section for offline reading, showing how
// it's going and whether it's done.
export function saveButton(button, cache, key, src = "") {
  const what = key === SEARCH ? "search " : "";
  const label = async () => {
    const s = cache.saved(key, src);
    if (!s) return `⬇️ save ${what}for offline`;
    let current = "";
    try {
      current = (await cache.get("manifest.json")).generated;
    } catch {
      return `✅ ${what}saved for offline`;
    }
    return s.generated === current ? `✅ ${what}saved for offline` : "🔄 update offline copy";
  };
  label().then(t => { button.textContent = t; });
  button.addEventListener("click", async () => {
    if (button.disabled) return;
    button.disabled = true;
    navigator.storage?.persist?.().catch(() => {});
    try {
      const progress = (done, total) => { button.textContent = `⏳ saving ${done}/${total}…`; };
      const { pages, failed } = key === SEARCH ? await cache.saveSearch({ progress }) : await cache.save(key, { src, progress });
      button.textContent = failed ? `💔 ${failed} of ${pages} didn't save · try again` : await label();
    } catch {
      button.textContent = "📵 can't save while offline";
    }
    button.disabled = false;
  });
}

function idle(fn) {
  if (typeof requestIdleCallback === "function") requestIdleCallback(fn, { timeout: 10000 });
  else setTimeout(fn, 1000);
}

// One cache per repository per page.
const repoCaches = new Map();
export function repoCache(repo) {
  if (!repoCaches.has(repo)) repoCaches.set(repo, new RepoCache(repo));
  return repoCaches.get(repo);
}

// forgetLocalStorage removes the files earlier versions kept in
// localStorage.
function forgetLocalStorage() {
  try {
    const s = globalThis.localStorage;
    for (let i = s.length - 1; i >= 0; i--) {
      const k = s.key(i);
      if (k && k.startsWith(PREFIX + "file:")) s.removeItem(k);
    }
  } catch {
    // nothing to forget
  }
}

// boot runs on every page: it registers the service worker, starts
// watching the repository for updates, and wires up save buttons.
function boot() {
  const root = document.documentElement;
  const base = root.dataset.base, repo = root.dataset.repo;
  if (!base) return;
  if ("serviceWorker" in navigator && location.protocol !== "file:") {
    navigator.serviceWorker.register(base + "sw.js", { scope: base }).catch(() => {});
  }
  forgetLocalStorage();
  if (!repo) return;
  const cache = repoCache(repo);
  cache.watch();
  if (typeof globalThis.caches !== "undefined") {
    for (const b of document.querySelectorAll("button[data-save-section], button[data-save-search]")) {
      b.hidden = false;
      saveButton(b, cache, b.dataset.saveSection || SEARCH);
    }
  }
}

if (typeof document !== "undefined") boot();
