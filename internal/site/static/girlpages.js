// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages in the browser: a localStorage cache for repository files, a
// background check for newly published repositories that clears out what
// they no longer use, and the service worker that makes the site an app.

const PREFIX = "girlpages:";
// Files named after their own SHA-256 never change.
const IMMUTABLE = /(^|\/|-)[0-9a-f]{64}\.json$/;
// How often to look for a newly published repository.
const CHECK_EVERY = 30 * 60 * 1000;
// Bump lastUsed at most this often, to spare writes.
const TOUCH_EVERY = 60 * 60 * 1000;
// Entries for other repositories that haven't been used in this long go.
const FORGET_OTHERS = 30 * 24 * 60 * 60 * 1000;

// usableStorage returns localStorage if it works (it doesn't in some
// private modes, or outside a browser).
function usableStorage() {
  try {
    const s = globalThis.localStorage;
    if (!s) return null;
    s.setItem(PREFIX + "probe", "1");
    s.removeItem(PREFIX + "probe");
    return s;
  } catch {
    return null;
  }
}

async function fetchText(url, init) {
  const r = await fetch(url, init);
  if (!r.ok) {
    const e = new Error(`${url}: ${r.status}`);
    e.status = r.status;
    throw e;
  }
  return r.text();
}

// RepoCache fetches repository files through localStorage. Entries are
// stored as {u: last used (ms), d: the file's text}.
export class RepoCache {
  constructor(repo, { storage = usableStorage(), fetch: f = fetchText, now = () => Date.now() } = {}) {
    this.repo = repo.endsWith("/") ? repo : repo + "/";
    this.storage = storage;
    this.fetchText = f;
    this.now = now;
    this.key = PREFIX + "file:" + this.repo;
    this.metaKey = PREFIX + "meta:" + this.repo;
    this.onNewManifest = null; // set by watch()
  }

  // get returns the parsed JSON at path (relative to the repository).
  async get(path) {
    const text = IMMUTABLE.test(path) ? await this.immutable(path) : await this.mutable(path);
    return JSON.parse(text);
  }

  async immutable(path) {
    const hit = this.read(path);
    if (hit !== null) return hit;
    const text = await this.fetchText(this.repo + path);
    this.write(path, text);
    return text;
  }

  // mutable files come from the network when it's there, else the cache.
  async mutable(path) {
    let text;
    try {
      text = await this.fetchText(this.repo + path, { cache: "no-cache" });
    } catch (e) {
      const hit = this.read(path);
      if (hit !== null && e.status === undefined) return hit; // offline: use what we have
      throw e;
    }
    this.write(path, text);
    if (path === "manifest.json") this.sawManifest(text);
    return text;
  }

  read(path) {
    if (!this.storage) return null;
    let raw;
    try {
      raw = this.storage.getItem(this.key + path);
    } catch {
      return null;
    }
    if (raw === null) return null;
    try {
      const e = JSON.parse(raw);
      if (this.now() - e.u > TOUCH_EVERY) this.put(this.key + path, { u: this.now(), d: e.d });
      return e.d;
    } catch {
      this.storage.removeItem(this.key + path);
      return null;
    }
  }

  write(path, text) {
    if (this.storage) this.put(this.key + path, { u: this.now(), d: text });
  }

  // put stores an entry, evicting the least recently used ones if storage
  // is full. Giving up quietly is fine: it's only a cache.
  put(key, entry) {
    const value = JSON.stringify(entry);
    for (let tries = 0; tries < 50; tries++) {
      try {
        this.storage.setItem(key, value);
        return true;
      } catch {
        if (!this.evict(value.length)) return false;
      }
    }
    return false;
  }

  // evict removes the oldest girlpages files until about want bytes are
  // freed, reporting whether it removed anything.
  evict(want) {
    const entries = [];
    for (const k of this.keys()) {
      if (!k.startsWith(PREFIX + "file:")) continue;
      try {
        const raw = this.storage.getItem(k);
        entries.push({ k, u: JSON.parse(raw).u, n: raw.length });
      } catch {
        entries.push({ k, u: 0, n: 0 });
      }
    }
    if (!entries.length) return false;
    entries.sort((a, b) => a.u - b.u);
    let freed = 0;
    for (const e of entries) {
      this.storage.removeItem(e.k);
      freed += e.n;
      if (freed >= want * 2) break;
    }
    return true;
  }

  keys() {
    const out = [];
    for (let i = 0; i < this.storage.length; i++) {
      const k = this.storage.key(i);
      if (k && k.startsWith(PREFIX)) out.push(k);
    }
    return out;
  }

  meta() {
    try {
      return JSON.parse(this.storage.getItem(this.metaKey)) || {};
    } catch {
      return {};
    }
  }

  setMeta(m) {
    try {
      this.storage.setItem(this.metaKey, JSON.stringify(m));
    } catch {
      // Storage full of files; the next check will try again.
    }
  }

  // sawManifest notes a manifest fetched from the network and, if it's not
  // the one seen last time, has the cache collect garbage.
  sawManifest(text) {
    if (!this.storage) return;
    const m = this.meta();
    m.checked = this.now();
    let id;
    try {
      const ix = JSON.parse(text).index;
      id = [ix.words, ix.grams, ix.docs].join(",");
    } catch {
      return;
    }
    if (m.manifest === id) {
      this.setMeta(m);
      return;
    }
    m.manifest = id;
    this.setMeta(m);
    if (this.onNewManifest) this.onNewManifest(JSON.parse(text));
  }

  // gc removes every cached file the manifest doesn't use, and entries for
  // other repositories that have gone unused for a month. It returns how
  // many entries it removed.
  async gc(manifest) {
    if (!this.storage) return 0;
    const ix = manifest.index;
    const live = new Set(["manifest.json"]);
    for (const [kind, sha] of [["words", ix.words], ["grams", ix.grams], ["docs", ix.docs]]) {
      const path = `index/${kind}/manifest-${sha}.json`;
      live.add(path);
      const m = await this.get(path);
      const shas = kind === "docs" ? m.shards : m.shards.map(s => s.sha256);
      for (const s of shas) live.add(`index/${kind}/shard-${s.slice(0, 2)}/${s}.json`);
    }
    let removed = 0;
    for (const k of this.keys()) {
      if (k.startsWith(this.key)) {
        if (!live.has(k.slice(this.key.length))) {
          this.storage.removeItem(k);
          removed++;
        }
      } else if (k.startsWith(PREFIX + "file:")) {
        try {
          if (this.now() - JSON.parse(this.storage.getItem(k)).u < FORGET_OTHERS) continue;
        } catch {
          // unreadable: remove it
        }
        this.storage.removeItem(k);
        removed++;
      }
    }
    return removed;
  }

  // check fetches manifest.json if it hasn't been checked for a while,
  // which collects garbage if it's new.
  async check(force = false) {
    if (!this.storage) return;
    if (!force && this.now() - (this.meta().checked || 0) < CHECK_EVERY) return;
    await this.mutable("manifest.json");
  }

  // watch checks for new manifests now and then while the page is open,
  // collecting garbage in idle time when one turns up.
  watch() {
    if (!this.storage) return;
    this.onNewManifest = (m) => idle(() => this.gc(m).catch(() => {}));
    const tick = () => {
      if (document.visibilityState === "visible") idle(() => this.check().catch(() => {}));
    };
    tick();
    setInterval(tick, CHECK_EVERY);
    document.addEventListener("visibilitychange", tick);
  }
}

function idle(fn) {
  if (typeof requestIdleCallback === "function") requestIdleCallback(fn, { timeout: 10000 });
  else setTimeout(fn, 1000);
}

// One cache per repository per page.
const caches = new Map();
export function repoCache(repo) {
  if (!caches.has(repo)) caches.set(repo, new RepoCache(repo));
  return caches.get(repo);
}

// boot runs on every page: it registers the service worker and starts
// watching the repository for updates.
function boot() {
  const root = document.documentElement;
  const base = root.dataset.base, repo = root.dataset.repo;
  if (!base) return;
  if ("serviceWorker" in navigator && location.protocol !== "file:") {
    navigator.serviceWorker.register(base + "sw.js", { scope: base }).catch(() => {});
  }
  if (repo) repoCache(repo).watch();
}

if (typeof document !== "undefined") boot();
