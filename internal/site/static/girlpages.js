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
// segmentName matches site.SegmentName: the directory a name gets.
export function segmentName(s) {
  return s.startsWith(".") ? escapeName(s) : s;
}

// chapter matches manpage.Chapter: the part of the manual a section belongs
// to (3 for 3ssl); a section not starting with a digit is its own.
export const chapter = (sec) => (/^[0-9]/.test(sec) ? sec[0] : sec);

// isSeries matches manpage.IsSeries: a series of documents (rfc), not a
// section of the manual.
export const isSeries = (sec) => sec.length > 1 && !/^[0-9]/.test(sec);

const seg = (s) => encodeURIComponent(segmentName(s));

// pageURL is a page's static address: man/<sec>/<name>/, or
// <series>/<name>/.
export function pageURL(base, name, sec) {
  return isSeries(sec) ? `${base}${seg(sec)}/${seg(name)}/` : `${base}man/${seg(sec)}/${seg(name)}/`;
}

// listingURL is page n of a static listing: a chapter's (section/3/, for
// sub ""), one of its sections' (section/3/3ssl/, or section/3/3/ for just
// 3), or a series' (rfc/).
export function listingURL(base, key, sub = "", n = 1) {
  const u = isSeries(key) ? `${base}${seg(key)}/`
    : `${base}section/${seg(key)}/${sub ? seg(sub) + "/" : ""}`;
  return n > 1 ? `${u}page/${n}/` : u;
}

// Indexes are compact: lists of records are tables ({keys, data}, the
// fields named once and each record's values in turn), and hashes in them
// are in basE91 (with ' for "), as repo.Encode91 writes them.
const B91 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#$%&()*+,./:;<=>?@[]^_`{|}~'";
const B91D = new Map([...B91].map((c, i) => [c, i]));

// decode91 decodes basE91 to bytes.
export function decode91(str) {
  const out = [];
  let queue = 0, nbits = 0, v = -1;
  for (const ch of str) {
    const c = B91D.get(ch);
    if (c === undefined) throw new Error(`${JSON.stringify(str)} isn't basE91`);
    if (v < 0) {
      v = c;
      continue;
    }
    v += c * 91;
    queue |= v << nbits;
    nbits += (v & 8191) > 88 ? 13 : 14;
    while (nbits > 7) {
      out.push(queue & 255);
      queue >>>= 8;
      nbits -= 8;
    }
    v = -1;
  }
  if (v >= 0) out.push((queue | (v << nbits)) & 255);
  return out;
}

// unhash reads a hash in basE91 as hex.
export const unhash = (str) => (str ? decode91(str).map(b => b.toString(16).padStart(2, "0")).join("") : "");

// rows reads a table as records, decoding the fields named in hashes (and
// in hashMaps, objects whose values are hashes).
export function rows(table, { hashes = [], hashMaps = [] } = {}) {
  const keys = (table && table.keys) || [], data = (table && table.data) || [], out = [];
  for (let i = 0; keys.length && i + keys.length <= data.length; i += keys.length) {
    const r = {};
    keys.forEach((k, j) => { r[k] = data[i + j]; });
    for (const h of hashes) if (r[h]) r[h] = unhash(r[h]);
    for (const h of hashMaps) {
      if (!r[h]) continue;
      const m = {};
      for (const [k, v] of Object.entries(r[h])) m[k] = unhash(v);
      r[h] = m;
    }
    out.push(r);
  }
  return out;
}

// indexPagesOf reads a chapter index's (or shard's) pages (see
// repo.IndexPages): each {name, section, title, description, page,
// version, versions, sources, variants, girl}, the last three lists.
export function indexPagesOf(t) {
  if (!t || !t.keys) return [];
  const bits = (m, vals) => {
    const out = [];
    for (let i = 0; m > 0; i++, m = Math.floor(m / 2)) if (m % 2) out.push(vals[i]);
    return out;
  };
  return rows(t, { hashes: ["page"] }).map(r => ({
    ...r, section: t.sections[r.section], version: t.versions[r.version] || "",
    sources: bits(r.sources, t.sources), variants: bits(r.variants, t.variants), girl: bits(r.girl, t.sources),
  }));
}
// chapterShardsOf reads a chapter index's shards: {sha256, bytes, count, first, last, prev}.
export const chapterShardsOf = (ci) => rows(ci.shards, { hashes: ["sha256"] });
// chapterFile is a chapter's index, from its manifest entry.
export const chapterFile = (e) => `section/${enc(e.chapter)}-${e.sha256}.json`;
// shardsOf reads a manifest's (or section index's) shards.
export const shardsOf = (m) => rows(m.shards, { hashes: ["sha256"] });

const escHTML = (s) => String(s).replace(/[&'<>"]/g, c => ({ "&": "&amp;", "'": "&#39;", "<": "&lt;", ">": "&gt;", '"': "&#34;" })[c]);

// sourceNav is a manifest's sources as chips, as site.sourceChips makes
// them: everything first, then each distribution, with a menu of its
// releases if it has any. on is the source shown ("" for everything), and
// href(key) a source's link ("" for everything).
export function sourceNav(sources, { on = "", href, all = "✨ latest of everything" }) {
  const pill = (key, text) => `<a class="pill${key === on ? " on" : ""}" href="${escHTML(href(key))}">${escHTML(text)}</a>`;
  const groups = new Map();
  for (const s of sources || []) {
    if (!groups.has(s.distro)) groups.set(s.distro, { whole: false, releases: [] });
    const g = groups.get(s.distro);
    if (s.release) g.releases.push(s.release);
    else g.whole = true;
  }
  let out = pill("", all);
  for (const [distro, g] of groups) {
    if (!g.releases.length) {
      out += pill(distro, distro);
      continue;
    }
    const release = on.startsWith(distro + "/") ? on.slice(distro.length + 1) : "";
    const here = on === distro || g.releases.includes(release);
    out += `<details class="source"><summary class="pill${here ? " on" : ""}">${escHTML(release ? `${distro} · ${release}` : distro)}</summary><div class="menu">` +
      (g.whole ? pill(distro, `all of ${distro}`) : "") + g.releases.map(r => pill(`${distro}/${r}`, r)).join("") + "</div></details>";
  }
  return out;
}

// sectionShard is a shard of a long section's index.
export const sectionShard = (sha) => `section/shard-${sha.slice(0, 2)}/${sha}.json`;


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
  // listing, and the girly variant of a version (the latest, or a source's
  // if src says), or the original where there's no girly one. page, the
  // listing's hash, saves asking the pointer.
  async keepPage(name, sec, { page = "", src = "" } = {}) {
    const ptr = JSON.parse(await this.mutable(`page/name-${enc(sec)}/${enc(name)}.json`, { fresh: true }));
    page = page || ptr.sha256;
    const l = await this.get(`page/page-${page.slice(0, 2)}/${page}.json`);
    const source = (src && l.sources && l.sources[src]) || l.current;
    const ver = (l.versions || []).find(v => v.source === source);
    if (!ver) return;
    const vs = ver.variants || {};
    const which = vs.girl ? "girl" : "man";
    if (vs[which]) await this.get(`contents/${source.slice(0, 2)}/${source}/${which}-${vs[which].sha256}.json`);
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
const SEARCH_PARTS = { words: ["words", "names", "docs"], typos: ["grams"] };

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
  // Following a link: the app's router takes it from here (see router.js).
  // If the app can't load, the link's followed as a plain one.
  const go = (href) => load().then(m => m.navigate(href), () => { location.href = href; });
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
  // A source's menu stays on the screen: one under a chip near the edge
  // is moved in.
  document.addEventListener("toggle", (e) => {
    const d = e.target;
    if (!d.matches?.("details.source") || !d.open) return;
    const menu = d.querySelector(".menu");
    menu.style.left = "";
    const r = menu.getBoundingClientRect(), edge = document.documentElement.clientWidth - 8;
    if (r.right > edge) menu.style.left = `${Math.max(8 - r.left, edge - r.right)}px`;
  }, true);
  // An open version switcher closes when anything else is clicked.
  document.addEventListener("click", (e) => {
    for (const d of document.querySelectorAll("details.versions[open], details.source[open]")) if (!d.contains(e.target) || e.target.closest("a")) d.open = false;
  });
  if (!repo) return;
  startApp(base);
  const cache = repoCache(repo);
  cache.watch();
  // A static page read (or listing seen) is kept, so the browse app can
  // show it offline.
  const page = document.querySelector("[data-keep-page]");
  if (page) idle(() => cache.keepPage(page.dataset.keepPage, page.dataset.keepSection).catch(() => {}));
  if (typeof globalThis.caches !== "undefined") {
    for (const b of document.querySelectorAll("button[data-save-section], button[data-save-search]")) {
      b.hidden = false;
      saveButton(b, cache, b.dataset.saveSection || "search:" + b.dataset.saveSearch);
    }
  }
}

if (typeof document !== "undefined") boot();
