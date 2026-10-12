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
// browseURL is a view in the browse app, which lives in the home page's
// hash: #/[<source>/]<sec>/<name>[/<version>][/<variant>][/mono] for a page
// (the latest version, or as a source has it, unless a version is given:
// an upstream version number, or the start of a source hash), and
// #/[<source>/]section/<key>[/<sec>] for a listing, where a source is a
// distro or distro/release.
export function browseURL(base, { src = "", section = "", sub = "", page = "", sec = "", version = "", variant = "", mono = false, at = "" } = {}) {
  const segs = src ? src.split("/") : [];
  if (page) {
    segs.push(sec, page);
    if (version) segs.push(version);
    if (variant) segs.push(variant);
    if (mono) segs.push("mono");
    if (at) segs.push(at);
  } else if (section) {
    segs.push("section", section);
    if (sub) segs.push(sub);
  }
  return base + "#/" + segs.map(encodeURIComponent).join("/");
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
    this.stale = new Set(); // mutable files given from the store, not yet refreshed
    this.inflight = new Map(); // refreshes under way, by path
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
      if (hit !== null && e.status === undefined) return hit; // offline: use what we have
      throw e;
    }
  }

  async fetchMutable(path, quick) {
    const init = { cache: "no-cache" };
    if (quick && typeof AbortSignal !== "undefined" && AbortSignal.timeout) init.signal = AbortSignal.timeout(NETWORK_TIMEOUT);
    const text = await this.fetchText(this.repo + path, init);
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
  async save(key, { src = "", progress = () => {} } = {}) {
    const m = JSON.parse(await this.mutable("manifest.json", { fresh: true }));
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
    const one = (p) => this.keepPage(p.name, p.sec, { page: p.page, source: p.source });
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

  // keepPage fetches what's needed to read a page offline: its pointer,
  // listing, and the girly variant of a version (the latest, unless source
  // says), or the original where there's no girly one. page, the listing's
  // hash, saves asking the pointer.
  async keepPage(name, sec, { page = "", source = "" } = {}) {
    const ptr = JSON.parse(await this.mutable(`page/name-${enc(sec)}/${enc(name)}.json`, { fresh: true }));
    page = page || ptr.sha256;
    const l = await this.get(`page/page-${page.slice(0, 2)}/${page}.json`);
    source = source || l.current;
    const ver = (l.versions || []).find(v => v.source === source);
    if (!ver) return;
    const vs = ver.variants || {};
    const which = vs.girl ? "girl" : "man";
    if (vs[which]) await this.get(`contents/${source.slice(0, 2)}/${source}/${which}-${vs[which].sha256}.json`);
  }

  // keepListing fetches the section indexes a section's listing needs.
  async keepListing(key) {
    const m = await this.get("manifest.json");
    const entries = (m.sections || []).filter(e => key.length > 1 ? e.section === key : e.section[0] === key[0]);
    await Promise.all(entries.map(e => this.get(`section/${enc(e.section)}-${e.sha256}.json`)));
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
      const shards = kind === "docs" ? im.shards.map((sha, i) => ({ sha256: sha, bytes: (im.bytes || [])[i] || 0 })) : im.shards;
      for (const s of shards) out.push({ path: `index/${kind}/shard-${s.sha256.slice(0, 2)}/${s.sha256}.json`, bytes: s.bytes || 0 });
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
  async saveSearch(part, { progress = () => {} } = {}) {
    const m = JSON.parse(await this.mutable("manifest.json", { fresh: true }));
    // Search needs every part's manifest (they're small) to start.
    const ix = m.index || {};
    await Promise.all(["words", "grams", "docs"].map(k => this.get(`index/${k}/manifest-${ix[k]}.json`)));
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
      const ptr = await this.store.get(`page/name-${enc(sec)}/${enc(name)}.json`);
      if (ptr === null) return false;
      const sha = JSON.parse(ptr).sha256;
      const lt = await this.store.get(`page/page-${sha.slice(0, 2)}/${sha}.json`);
      if (lt === null) return false;
      const l = JSON.parse(lt);
      const source = (src && l.sources && l.sources[src]) || l.current;
      const ver = (l.versions || []).find(v => v.source === source);
      const vs = (ver && ver.variants) || {};
      const which = vs.girl ? "girl" : "man";
      return !!vs[which] && await this.store.get(`contents/${source.slice(0, 2)}/${source}/${which}-${vs[which].sha256}.json`) !== null;
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
const SEARCH_PARTS = { words: ["words", "docs"], typos: ["grams"] };

// saveButton makes a button save a section for offline reading, showing how
// it's going and whether it's done.
export function saveButton(button, cache, key, src = "") {
  const part = key.startsWith("search:") ? key.slice(7) : "";
  const what = { words: "search", typos: "typo matching" }[part] || "";
  let size = "";
  const label = async () => {
    const s = cache.saved(key, src);
    const saved = what ? `✅ ${what} saved offline` : "✅ saved for offline";
    if (!s) return what ? `⬇️ ${what} offline${size}` : "⬇️ save for offline";
    let current = "";
    try {
      current = (await cache.get("manifest.json")).generated;
    } catch {
      return saved;
    }
    return s.generated === current ? saved : `🔄 update offline ${what || "copy"}`;
  };
  label().then(t => { button.textContent = t; });
  if (part) {
    cache.searchSize(part).then(n => {
      if (n) size = ` · ${megabytes(n)}`;
      return label();
    }).then(t => { if (!button.disabled) button.textContent = t; }).catch(() => {});
  }
  button.addEventListener("click", async () => {
    if (button.disabled) return;
    button.disabled = true;
    navigator.storage?.persist?.().catch(() => {});
    try {
      const progress = (done, total) => { button.textContent = `⏳ saving ${done}/${total}…`; };
      const { pages, failed } = part ? await cache.saveSearch(part, { progress }) : await cache.save(key, { src, progress });
      button.textContent = failed ? `💔 ${failed} of ${pages} didn't save · try again` : await label();
      button.dispatchEvent(new CustomEvent("saved", { bubbles: true }));
    } catch {
      button.textContent = "📵 can't save while offline";
    }
    button.disabled = false;
  });
}

function megabytes(n) {
  return n < 1e6 ? `${Math.max(1, Math.round(n / 1e3))} kB` : `${(n / 1e6).toFixed(n < 1e7 ? 1 : 0)} MB`;
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

// startApp lets any page become the browse app (static/app.js) without
// loading another. Following a link to a route (#/…) shows it in place,
// and once the app has loaded (in idle time), so does following any link
// within the site, or searching: from then on the static pages are only
// what the first visit, search engines, and browsers without JavaScript
// see. History leads back. A link's files are fetched as soon as it's
// pointed at or touched.
function startApp(base) {
  const isRoute = (h) => /^#\/./.test(h);
  let loading = null, app = null;
  const load = () => (loading ||= import(new URL(base + "static/app.js", location.href).href).then(m => {
    app = m;
    // Its links are followed in place now; prerendering them is waste.
    for (const s of document.querySelectorAll('script[type="speculationrules"]')) s.remove();
    return m;
  }));
  const open = () => load().then(m => m.start());
  const go = (href) => {
    // Remember where this was, and whether it's the page's own (static)
    // view, to come back to.
    const own = !document.documentElement.classList.contains("routed");
    history.replaceState({ ...(history.state || {}), y: scrollY, own }, "");
    history.pushState({}, "", href);
    open();
  };
  // A link the app shows, from an event on it, or null: routes always,
  // anything else once the app has loaded (but not links within a page).
  const appLink = (e) => {
    const a = e.target.closest?.("a[href]");
    if (!a || a.target || a.hasAttribute("download")) return null;
    const u = new URL(a.href, location.href);
    if (u.origin !== location.origin) return null;
    if (u.pathname === base && isRoute(u.hash)) return u;
    if (u.hash && u.pathname === location.pathname && u.search === location.search) return null;
    return app && app.handles(u.href) ? u : null;
  };
  if (location.pathname === base || location.pathname === base + "browse/") open();
  document.addEventListener("click", (e) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const u = appLink(e);
    if (!u) return;
    e.preventDefault();
    go(u.href);
  });
  document.addEventListener("submit", (e) => {
    const f = e.target;
    if (!app || e.defaultPrevented || f.method.toLowerCase() !== "get") return;
    const u = new URL(f.action, location.href);
    u.search = new URLSearchParams(new FormData(f)).toString();
    if (!app.handles(u.href)) return;
    e.preventDefault();
    f.querySelector("input")?.blur();
    go(u.href);
  });
  const warmed = new Set();
  const warm = (e) => {
    const u = appLink(e);
    if (!u || warmed.has(u.href)) return;
    warmed.add(u.href);
    load().then(m => m.prefetch(u.href)).catch(() => {});
  };
  for (const ev of ["pointerover", "touchstart", "focusin"]) document.addEventListener(ev, warm, { passive: true });
  idle(() => load().catch(() => {}));
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
  startApp(base);
  const cache = repoCache(repo);
  cache.watch();
  // A static page read (or listing seen) is kept, so the browse app can
  // show it offline.
  const page = document.querySelector("[data-keep-page]");
  if (page) idle(() => cache.keepPage(page.dataset.keepPage, page.dataset.keepSection).catch(() => {}));
  const list = document.querySelector("button[data-save-section]");
  if (list) idle(() => cache.keepListing(list.dataset.saveSection).catch(() => {}));
  if (typeof globalThis.caches !== "undefined") {
    for (const b of document.querySelectorAll("button[data-save-section], button[data-save-search]")) {
      b.hidden = false;
      saveButton(b, cache, b.dataset.saveSection || "search:" + b.dataset.saveSearch);
    }
  }
}

if (typeof document !== "undefined") boot();
