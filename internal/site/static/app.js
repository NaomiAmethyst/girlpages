// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The browse app: everything the static site doesn't render itself, read
// straight from the repository, in the home page's hash (see browseURL in
// girlpages.js):
//
//   #/debian/trixie                 a source's home
//   #/section/3, #/section/3/3ssl   a section's listing, or a subsection's
//   #/debian/section/1              …from one source
//   #/1/ls                          a page (its latest version)
//   #/debian/trixie/1/ls            the version a source has, with links that stay in it
//   #/1/ls/9.4                      another version: an upstream version, or a source hash's start
//   #/1/ls/man                      another variant: man is the original; girl is the default
//   #/1/ls/9.4/man/mono             the original as man prints it, not reflowed
//   …/versions                      scrolled to the list of versions
//
// It's also where static pages go when offline. /browse/?page=… (from
// earlier versions) redirects here.

import { repoCache, saveButton, browseURL } from "./girlpages.js";
import { VirtualList } from "./vlist.js";
import { renderHTML, renderOriginalHTML, esc } from "./render.js";
import { escapeName, segmentName } from "./search.js";

const SECTIONS = {
  "1": ["User commands", "💻"], "2": ["System calls", "📞"], "3": ["Library functions", "📚"],
  "4": ["Special files", "🔌"], "5": ["File formats & config", "📝"], "6": ["Games", "🎮"],
  "7": ["Miscellany", "🌈"], "8": ["System administration", "👑"], "9": ["Kernel routines", "🧠"],
  "n": ["Tcl/Tk", "🪶"], "l": ["Local", "🏡"], "o": ["Old", "🕰️"],
};
const sectionName = (k) => SECTIONS[k[0]] || ["Section " + k, "📖"];

const app = document.getElementById("app");
const base = app.dataset.base;
const cache = repoCache(app.dataset.repo);
const enc = (s) => encodeURIComponent(escapeName(s));

// Where things are.
const staticPage = (name, sec) => `${base}man/${encodeURIComponent(segmentName(sec))}/${encodeURIComponent(segmentName(name))}/`;
const browse = (params) => browseURL(base, params);
const sourceLabel = (key) => (key ? key.replace("/", " ") : "everything");

// How variants are offered. Others (made with other prompts) get their name.
function variantLabel(v) {
  return v === "girl" ? "💅 girly" : v === "man" ? "📜 original" : "✨ " + v;
}
// Girly first, the original last, anything else between.
function variantOrder(a, b) {
  const rank = (v) => (v === "girl" ? 0 : v === "man" ? 2 : 1);
  return rank(a) - rank(b) || (a < b ? -1 : a > b ? 1 : 0);
}

// The repository's files.
const manifest = () => cache.get("manifest.json");
async function listing(name, sec) {
  const p = await cache.get(`page/name-${enc(sec)}/${enc(name)}.json`);
  return cache.get(`page/page-${p.sha256.slice(0, 2)}/${p.sha256}.json`);
}
// A variant is named after its own hash (the listing gives it), so it's
// cached forever.
const variant = (source, v, sha) => cache.get(`contents/${source.slice(0, 2)}/${source}/${v}-${sha}.json`);
const sectionIndex = (e) => cache.get(`section/${enc(e.section)}-${e.sha256}.json`);

// variantText reassembles a variant, as Variant.Text does in Go.
function variantText(v) {
  let s = "";
  const framed = v.header || v.footer;
  if (framed) s += (v.header || "") + "\n";
  for (const sec of v.sections) {
    if (sec.heading) s += (v.syntax === "girlpages-markup" ? "# " : "") + sec.heading + "\n";
    s += sec.body;
  }
  if (framed) s += (v.footer || "") + "\n";
  return s;
}

// sourceLabels says where a version came from, as Version.SourceLabels does.
function sourceLabels(v) {
  const out = new Set();
  for (const p of v.packages || []) {
    const s = p.distro || (p.url ? "upstream" : "");
    if (!s) continue;
    out.add(s === "upstream" ? `upstream ${p.name || ""} ${p.version || ""}`.trim() : `${s} ${p.release || ""}`.trim());
  }
  return [...out].sort();
}

function sourceKeys(v) {
  const out = new Set();
  for (const p of v.packages || []) {
    const s = p.distro || (p.url ? "upstream" : "");
    if (s) out.add(s === "upstream" || !p.release ? s : `${s}/${p.release}`);
  }
  return [...out].sort();
}

// h shows a view, unless the address has moved on since it was asked for.
function h(html) {
  if (location.href === routingFor) app.innerHTML = html;
}

function sourceChips(m, on, section) {
  let out = `<a class="pill${on ? "" : " on"}" href="${esc(section ? `${base}section/${encodeURIComponent(section)}/` : base)}">✨ latest of everything</a>`;
  for (const s of m.sources || []) {
    const key = s.release ? `${s.distro}/${s.release}` : s.distro;
    if (section && !(s.sections || {})[section]) continue;
    const label = s.release ? "· " + s.release : s.distro;
    out += `<a class="pill${key === on ? " on" : ""}" href="${esc(browse({ src: key, section }))}">${esc(label)}</a>`;
  }
  return `<nav class="chips sources" aria-label="Browse by source">${out}</nav>`;
}

// everything is the whole repository as a source: the latest version of
// every page.
function everything(m) {
  const sections = {};
  for (const e of m.sections || []) sections[e.section[0]] = (sections[e.section[0]] || 0) + e.pages;
  return { pages: m.pages, sections };
}

async function sourceHome(src) {
  const m = await manifest();
  const s = src ? (m.sources || []).find(x => (x.release ? `${x.distro}/${x.release}` : x.distro) === src) : everything(m);
  if (!s) return oops("there's no source called that here 🤔");
  document.title = `${sourceLabel(src)} · girlpages 💅`;
  const keys = Object.keys(s.sections || {}).sort((a, b) => (/\d/.test(a[0]) !== /\d/.test(b[0]) ? (/\d/.test(a[0]) ? -1 : 1) : a < b ? -1 : 1));
  let cards = "";
  for (const k of keys) {
    const [name, emoji] = sectionName(k);
    cards += `<a class="section-card" href="${esc(browse({ src, section: k }))}"><span class="emoji">${emoji}</span>` +
      `<div class="num">${esc(k)}</div><div class="name">${esc(name)}</div><div class="count">${plural(s.sections[k])}</div></a>`;
  }
  h(`<section class="hero"><h1>✨ girlpages ✨</h1><p>the ${src ? `<strong>${esc(sourceLabel(src))}</strong> ` : ""}manual, but make it girly 💅</p>` +
    `<div class="stats">${plural(s.pages)} in ${esc(sourceLabel(src))}</div></section>` +
    sourceChips(m, src, "") + `<div class="sections">${cards}</div>`);
}

const plural = (n) => (n === 1 ? "1 page" : `${n} pages`);

async function sourceSection(src, key, sub) {
  const m = await manifest();
  const [name, emoji] = sectionName(key);
  const entries = m.sections.filter(e => e.section.startsWith(key[0]) && e.section[0] === key[0]);
  const indexes = await Promise.all(entries.map(sectionIndex));
  const subs = new Map();
  let items = [];
  for (const si of indexes) {
    for (const p of si.pages) {
      if (src && !(p.sources || []).includes(src)) continue;
      subs.set(si.section, (subs.get(si.section) || 0) + 1);
      if (sub && si.section !== sub) continue;
      const other = src && p.other ? p.other[src] : "";
      items.push({ name: p.name, sec: si.section, desc: p.description || "", girl: !other && (p.variants || []).includes("girl"),
        url: browse({ page: p.name, sec: si.section, src }) });
    }
  }
  items.sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()) || (a.sec < b.sec ? -1 : 1));
  const label = sub || key;
  document.title = `${sourceLabel(src)} · section ${label} · girlpages 💅`;
  let chips = "";
  if (subs.size > 1) {
    chips = `<a class="pill${sub ? "" : " on"}" href="${esc(browse({ src, section: key }))}">all</a>` +
      [...subs].sort().map(([s, n]) => `<a class="pill${s === sub ? " on" : ""}" href="${esc(browse({ src, section: key, sub: s }))}">${esc(s)} (${n})</a>`).join("");
    chips = `<div class="chips">${chips}</div>`;
  }
  // Chunks of the listing, grouped by first letter, as the static
  // listings' pages are; letters jump to the chunk they start in.
  const letterOf = (n) => (/^[a-z]/i.test(n) ? n[0].toUpperCase() : "#");
  const starts = new Map();
  items.forEach((it, i) => { const l = letterOf(it.name); if (!starts.has(l)) starts.set(l, Math.floor(i / CHUNK) + 1); });
  const chunkHTML = (n) => {
    let out = "", prev = n > 1 ? letterOf(items[(n - 1) * CHUNK - 1].name) : "", open = false;
    for (const it of items.slice((n - 1) * CHUNK, n * CHUNK)) {
      const l = letterOf(it.name);
      if (!open || l !== prev) {
        if (open) out += "</ul>";
        out += l === prev ? `<h2 class="letter cont">${esc(l)} <span class="sub">(continued)</span></h2>` : `<h2 class="letter" id="letter-${esc(l)}">${esc(l)}</h2>`;
        out += '<ul class="pages">';
        open = true;
        prev = l;
      }
      out += `<li><a href="${esc(it.url)}"><span class="name">${esc(it.name)}</span><span class="sec">(${esc(it.sec)})</span></a>` +
        `<span class="desc">${esc(it.desc)}</span>${it.girl ? '<span class="done" title="girlified">✨</span>' : ""}</li>`;
    }
    return open ? out + "</ul>" : out;
  };
  const chunks = Math.max(1, Math.ceil(items.length / CHUNK));
  let body = `<nav class="letters" aria-label="Jump to letter">${[...starts.keys()].map(l => `<a href="#letter-${esc(l)}" data-chunk="${starts.get(l)}">${esc(l)}</a>`).join("")}</nav>` +
    `<div class="listing"><div class="chunk" data-chunk="1">${chunkHTML(1)}</div></div>`;
  if (!items.length) body = '<div class="empty">nothing here, bestie 😭</div>';
  const crumbs = `<a href="${esc(base)}">home</a> › ` + (src ? `<a href="${esc(browse({ src }))}">${esc(sourceLabel(src))}</a> › ` : "");
  const save = "caches" in globalThis ? '<p class="save"><button type="button" class="pill" id="save"></button></p>' : "";
  h(`<div class="card"><nav class="crumbs">${crumbs}section ${esc(label)}</nav>` +
    `<div class="pagehead"><h1>section ${esc(label)} · ${esc(name)} ${emoji}</h1><p class="sub">${plural(items.length)}${src ? " in " + esc(sourceLabel(src)) : ""}</p>${save}</div>` +
    sourceChips(m, src, key[0]) + chips + body + "</div>");
  const button = document.getElementById("save");
  if (button) saveButton(button, cache, sub || key, src);
  const listing = location.href === routingFor && app.querySelector(".listing");
  if (listing && chunks > 1 && "IntersectionObserver" in globalThis) {
    const list = new VirtualList(listing, { count: chunks, load: async (n) => chunkHTML(n) });
    current = list;
    app.querySelector(".letters").addEventListener("click", (e) => {
      const a = e.target.closest("a[data-chunk]");
      if (!a) return;
      e.preventDefault();
      e.stopPropagation();
      list.jump(+a.dataset.chunk, a.getAttribute("href").slice(1));
    });
    list.fill();
  } else if (listing) {
    for (let n = 2; n <= chunks; n++) listing.insertAdjacentHTML("beforeend", `<div class="chunk" data-chunk="${n}">${chunkHTML(n)}</div>`);
  }
}

// CHUNK is how many pages a listing shows at a time, as the static
// listings' pages do.
const CHUNK = 200;
// current is the listing on screen, stopped when the route changes.
let current = null;

// loadPage fetches what a page view needs; rest is what follows its name
// in the route: [version] [variant] [mono] [versions]. It resolves to the
// view's data, or {error} to show instead.
async function loadPage(name, sec, rest, src) {
  let l;
  try {
    l = await listing(name, sec);
  } catch (e) {
    if (e instanceof TypeError) return { error: `you're offline, and ${name}(${sec}) isn't saved 📵` };
    if (e.status !== 404) throw e;
    return { error: `there's no ${name}(${sec}) here 😭` };
  }
  rest = [...rest];
  const at = rest[rest.length - 1] === "versions" ? rest.pop() : "";
  const mono = rest[rest.length - 1] === "mono" ? !!rest.pop() : false;
  // Versions by preference: the latest, then this source's, then newest added.
  const fallback = src && l.sources && l.sources[src] || l.current;
  const versions = [...l.versions].sort((a, b) => (a.source === l.current) !== (b.source === l.current) ? (a.source === l.current ? -1 : 1)
    : (a.source === fallback) !== (b.source === fallback) ? (a.source === fallback ? -1 : 1) : (b.added || "").localeCompare(a.added || ""));
  const findVersion = (tok) => versions.find(x => x.version === tok || (tok.length >= 8 && /^[0-9a-f]+$/.test(tok) && x.source.startsWith(tok)));
  // slug is how a version is named in a route: not at all if it's the one
  // shown by default, else by its upstream version if that's unique.
  const slug = (x) => x.source === fallback ? "" :
    x.version && l.versions.filter(y => y.version === x.version).length === 1 ? x.version : x.source.slice(0, 12);
  let ver = null, which = "";
  if (rest.length >= 2) [ver, which] = [findVersion(rest[0]), rest[1]];
  else if (rest.length === 1) {
    ver = findVersion(rest[0]);
    if (!ver) which = rest[0];
  }
  // One word that's neither a version nor a variant is a version that isn't here.
  const isVariant = (k) => l.versions.some(x => x.variants && x.variants[k]);
  if (!ver && (rest.length >= 2 || (rest.length === 1 && !isVariant(rest[0])))) {
    return { error: `there's no version ${rest[0]} of ${name}(${sec}) here 🤔` };
  }
  ver = ver || versions.find(x => x.source === fallback) || versions[0];
  if (!ver) return { error: "that page has no versions 🤔" };
  const has = (k) => ver.variants && ver.variants[k];
  const variants = Object.keys(ver.variants || {}).filter(k => /^[a-z][a-z0-9-]{0,31}$/.test(k)).sort(variantOrder);
  if (!which || !has(which)) which = variants[0] || "man";
  const doc = await variant(ver.source, which, ver.variants[which].sha256);
  return { l, ver, which, variants, versions, slug, doc, at, mono };
}

// pageView shows a page.
async function pageView(name, sec, rest, src) {
  const d = await loadPage(name, sec, rest, src);
  if (d.error) return oops(d.error);
  const { l, ver, which, variants, versions, slug, doc, at, mono } = d;
  const title = ver.title || `${name.toUpperCase()}(${sec})`;
  const latest = ver.source === l.current;
  const suffix = which === "girl" ? "" : which === "man" ? " · original" : " · " + which;
  document.title = `${name}(${sec})${ver.description ? " — " + ver.description : ""}${suffix} · girlpages 💅`;
  const here = (extra) => browse({ page: name, sec, version: slug(ver), src, ...extra });

  let body = "", toc = [];
  const link = src ? (n, s) => browse({ page: n, sec: s, src }) : (n, s) => staticPage(n, s);
  if (doc.syntax === "girlpages-markup") {
    ({ html: body, toc } = renderHTML(variantText(doc), { link }));
    body = `<div class="content">${body}</div>`;
  } else if (!mono) {
    ({ html: body, toc } = renderOriginalHTML(doc, { link }));
    body = `<div class="content reflowed">${body}</div>`;
  } else {
    body = '<div class="original">' + (doc.header ? `<pre class="manhead">${esc(doc.header)}</pre>` : "") +
      doc.sections.map(s => (s.heading ? `<h2>${esc(s.heading)}</h2>` : "") + `<pre>${esc(s.body)}</pre>`).join("\n") +
      (doc.footer ? `<pre class="manhead">${esc(doc.footer)}</pre>` : "") + "</div>";
  }
  let meta = "";
  if (doc.generator) {
    const g = doc.generator, when = g.generated && !g.generated.startsWith("0001") ? g.generated.slice(0, 10) : "";
    meta += `<span>${which === "girl" ? "girlified" : "rewritten"} by ${esc(g.model || g.backend || "a model")}${when ? " on " + esc(when) : ""} 💖</span>`;
  }
  if (variants.length > 1) {
    meta += variants.map(k => k === which ? `<span class="pill on">${esc(variantLabel(k))}</span>`
      : `<a class="pill" href="${esc(here({ variant: k === variants[0] ? "" : k }))}">${esc(variantLabel(k))}</a>`).join("");
  }
  if (doc.syntax !== "girlpages-markup") {
    const href = here({ variant: which === variants[0] ? "" : which, mono: !mono });
    meta += `<a class="pill${mono ? " on" : ""}" href="${esc(href)}" title="as man prints it">⌨️ monospace</a>`;
  }
  const keys = sourceKeys(ver);
  const inLine = keys.length ? `<div class="meta in">📦 ${ver.version ? "version " + esc(ver.version) + ", " : ""}in ` +
    keys.map(k => `<a href="${esc(browse({ page: name, sec, src: k }))}">${esc(sourceLabel(k))}</a>`).join(", ") + "</div>" : "";
  let notice = "";
  if (!latest) notice += `<div class="notice">🕰️ this isn't the latest version of ${esc(name)}(${esc(sec)}). <a href="${esc(staticPage(name, sec))}">here's the latest</a>.</div>`;
  if (which === "man" && variants.length === 1) notice += `<div class="notice">💤 she hasn't gotten to this version yet, so here's the original.</div>`;
  const vlist = versions.length > 1 ? `<div class="provenance" id="versions"><details open><summary>🕰️ ${versions.length} versions, and where they're from</summary><ul>` +
    versions.map(x => {
      const label = (x.version ? x.version + " · " : "") + (sourceLabels(x).join(", ") || "added " + (x.added || "").slice(0, 10)) +
        (x.source === l.current ? " (latest)" : "");
      return x.source === ver.source ? `<li><strong>${esc(label)}</strong> (this one)</li>`
        : `<li><a href="${esc(browse({ page: name, sec, version: slug(x), src }))}">${esc(label)}</a></li>`;
    }).join("") + "</ul></details></div>" : "";
  const crumbs = `<nav class="crumbs"><a href="${esc(base)}">home</a> › ` + (src
    ? `<a href="${esc(browse({ src }))}">${esc(sourceLabel(src))}</a> › <a href="${esc(browse({ src, section: sec[0], sub: sec.length > 1 ? sec : "" }))}">section ${esc(sec)}</a>`
    : `<a href="${esc(base)}section/${encodeURIComponent(sec[0])}/">section ${esc(sec)}</a>`) + ` › ${esc(title)}</nav>`;
  const tocHTML = toc.length ? `<nav class="toc card" aria-label="Contents"><h2>✨ contents</h2><ul>` +
    toc.map(t => `<li${t.level === 2 ? ' class="sub"' : ""}><a href="#${esc(t.id)}">${esc(t.text)}</a></li>`).join("") + "</ul></nav>" : "";
  h(`<div class="layout"><article class="card manpage">${crumbs}<h1>${esc(title)}${esc(suffix)}</h1>` +
    `<div class="meta">${meta}</div>${inLine}${notice}${body}${vlist}</article>${tocHTML}</div>`);
  if (at) afterShow = () => document.getElementById(at)?.scrollIntoView();
}

function oops(msg) {
  h(`<div class="card waiting"><div class="spinner">😭</div><h1>${esc(msg)}</h1><p><a href="${esc(base)}">home</a> · <a href="${esc(base)}search/">search</a></p></div>`);
}

// The app runs in whatever page it's started on, hiding that page's own
// (static) view while it shows a route, and showing it again when history
// comes back to it. /browse/ (from earlier versions) redirects.
const root = document.documentElement;
const docURL = location.pathname + location.search;
const docTitle = document.title;
const isRoute = (h) => /^#\/./.test(h);
let shown = "";          // the address last shown
let routingFor = "";     // the address being shown; views for others are dropped
let afterShow = null;    // where a view wants scrolling to, once it's shown

// legacyRoute turns a /browse/?… query from earlier versions into a route.
function legacyRoute(q) {
  const src = q.get("src") || "";
  if (q.get("page") && q.get("sec")) {
    return browse({ page: q.get("page"), sec: q.get("sec"), src, version: (q.get("v") || "").slice(0, 12),
      variant: q.get("variant") || (q.get("mono") ? "man" : ""), mono: q.get("mono") === "1" });
  }
  if (q.get("section")) return browse({ src, section: q.get("section"), sub: q.get("sub") || "" });
  return browse({ src });
}

function showStatic() {
  root.classList.remove("routed");
  app.hidden = true;
  document.title = docTitle;
  if (history.state?.y != null) scrollTo(0, history.state.y);
}

function showApp() {
  root.classList.add("routed");
  app.hidden = false;
}

// parse reads a route, resolving to what it shows.
async function parse(h) {
  let segs;
  try {
    segs = h.slice(2).split("/").filter(x => x !== "").map(decodeURIComponent);
  } catch {
    return { kind: "bad" };
  }
  let src = "";
  // Only a word that isn't a section or "section" can start a source.
  if (segs.length && segs[0] !== "section" && !/^[0-9]/.test(segs[0])) {
    const sources = (await manifest()).sources || [];
    if (sources.some(s => s.distro === segs[0])) {
      src = segs.shift();
      if (segs.length && sources.some(s => s.distro === src && s.release === segs[0])) src += "/" + segs.shift();
    }
  }
  if (!segs.length) return { kind: "source", src };
  if (segs[0] === "section") return segs[1] ? { kind: "section", src, key: segs[1], sub: segs[2] || "" } : { kind: "source", src };
  if (segs.length < 2) return { kind: "bad" };
  return { kind: "page", src, sec: segs[0], name: segs[1], rest: segs.slice(2) };
}

async function route() {
  current?.destroy();
  current = null;
  const r = await parse(location.hash);
  switch (r.kind) {
    case "source": return sourceHome(r.src);
    case "section": return sourceSection(r.src, r.key, r.sub);
    case "page": return pageView(r.name, r.sec, r.rest, r.src);
  }
  return oops(r.kind === "bad" ? "there's nothing here 🤔" : "that link's broken 🤔");
}

// main shows the address. A route is rendered out of sight, and shown
// once it's ready (with a spinner in the meantime, if that's a while), so
// moving between views doesn't flash. Files that change are shown from the
// cache while they're refreshed, so if one turns out to be stale enough to
// point at something since removed, it's refreshed and shown again.
async function main() {
  const url = location.href;
  if (url === shown) return;
  shown = url;
  if (!isRoute(location.hash)) {
    // Back to the page the app started on, or somewhere else to load.
    if (location.pathname + location.search === docURL) return showStatic();
    return location.reload();
  }
  routingFor = url;
  afterShow = null;
  const slow = setTimeout(() => {
    if (routingFor !== url) return;
    app.innerHTML = '<div class="card waiting"><div class="spinner">💅</div><p>getting it ready, bestie…</p></div>';
    showApp();
  }, 300);
  try {
    await route();
  } catch (e) {
    if (e.status === 404 && await cache.revalidate()) {
      try {
        await route();
        e = null;
      } catch (e2) {
        e = e2;
      }
    }
    if (e) oops(e instanceof TypeError ? "you're offline, and this isn't saved yet 📵" : `something broke 💔 ${e.message}`);
  }
  clearTimeout(slow);
  if (location.href !== url) return;
  showApp();
  if (afterShow) afterShow();
  else scrollTo(0, history.state?.y ?? 0);
}

let started = false;

// start shows the address, and from then on follows it.
export function start() {
  if (!started) {
    started = true;
    if (location.pathname === base + "browse/") {
      location.replace(location.search ? legacyRoute(new URLSearchParams(location.search)) : base + location.hash);
      return;
    }
    // Links within a view (contents, letters) scroll rather than route.
    app.addEventListener("click", (e) => {
      const a = e.target.closest("a[href^='#']");
      if (!a || a.getAttribute("href").startsWith("#/")) return;
      e.preventDefault();
      document.getElementById(decodeURIComponent(a.getAttribute("href").slice(1)))?.scrollIntoView();
    });
    addEventListener("popstate", main);
    addEventListener("hashchange", main);
  }
  return main();
}

// prefetch fetches what a route will need, so following it is quick.
export async function prefetch(href) {
  const u = new URL(href, location.href);
  if (!isRoute(u.hash)) return;
  const r = await parse(u.hash);
  if (r.kind === "page") await loadPage(r.name, r.sec, r.rest, r.src);
}
