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

import { repoCache, saveButton, browseURL, chapter, isSeries, pageURL, listingURL as listingAt, sectionShard } from "./girlpages.js";
import { VirtualList } from "./vlist.js";
import { renderHTML, renderOriginalHTML, esc } from "./render.js";
import { escapeName, segmentName, searchPage } from "./search.js";

const SECTIONS = {
  "1": ["User commands", "💻"], "2": ["System calls", "📞"], "3": ["Library functions", "📚"],
  "4": ["Special files", "🔌"], "5": ["File formats & config", "📝"], "6": ["Games", "🎮"],
  "7": ["Miscellany", "🌈"], "8": ["System administration", "👑"], "9": ["Kernel routines", "🧠"],
  "n": ["Tcl/Tk", "🪶"], "l": ["Local", "🏡"], "o": ["Old", "🕰️"],
  "rfc": ["Requests for Comments", "🌐"],
};
const sectionName = (k) => SECTIONS[k] || SECTIONS[chapter(k)] || ["Section " + k, "📖"];

const app = document.getElementById("app");
const base = app.dataset.base;
const cache = repoCache(app.dataset.repo);
const enc = (s) => encodeURIComponent(escapeName(s));

// Where things are.
const staticPage = (name, sec) => pageURL(base, name, sec);
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

// h puts a view in place, unless the address has moved on since it was
// asked for. It's shown when it's ready (see main), or by reveal.
function h(html) {
  if (location.href !== routingFor) return;
  app.innerHTML = html;
  renderedFor = routingFor;
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
  for (const e of m.sections || []) sections[chapter(e.section)] = (sections[chapter(e.section)] || 0) + e.pages;
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
    cards += `<a class="section-card" href="${esc(src ? browse({ src, section: k }) : listingURL(k, "", 1))}"><span class="emoji">${emoji}</span>` +
      `<div class="num">${esc(k)}</div><div class="name">${esc(name)}</div><div class="count">${plural(s.sections[k])}</div></a>`;
  }
  document.title = src ? document.title : "the manual, but make it girly · girlpages 💅";
  const search = src ? "" : `<form class="hero-search" action="${esc(base)}search/" role="search"><input type="search" name="q" placeholder="what are we reading today? 🔎" aria-label="Search manual pages"></form>`;
  h(`<section class="hero"><h1>✨ girlpages ✨</h1><p>the ${src ? `<strong>${esc(sourceLabel(src))}</strong> ` : ""}manual, but make it girly 💅</p>` +
    search + `<div class="stats">${plural(s.pages)}${src ? " in " + esc(sourceLabel(src)) : ""}</div></section>` +
    sourceChips(m, src, "") + `<div class="sections">${cards}</div>`);
}

const plural = (n) => (n === 1 ? "1 page" : `${n} pages`);

// sourceSection shows a section's listing (or a subsection's), from one
// source or all; start is the chunk to start at, and pageURL(n), if given,
// the static listing page the address follows as it scrolls.
async function sourceSection(src, key, sub, { start = 1, pageURL = null } = {}) {
  const m = await manifest();
  const [name, emoji] = sectionName(key);
  const entries = m.sections.filter(e => chapter(e.section) === chapter(key));
  const plan = await listingPlan(entries, src, sub);
  const label = sub || key;
  document.title = `${sourceLabel(src)} · ${sectionLabel(label)} · girlpages 💅`;
  let chips = "";
  if (plan.subs.size > 1) {
    const n = (c) => (c == null ? "" : ` (${c})`);
    chips = `<a class="pill${sub ? "" : " on"}" href="${esc(browse({ src, section: key }))}">all</a>` +
      [...plan.subs].sort().map(([s, c]) => `<a class="pill${s === sub ? " on" : ""}" href="${esc(browse({ src, section: key, sub: s }))}">${esc(s)}${n(c)}</a>`).join("");
    chips = `<div class="chips">${chips}</div>`;
  }
  // The count, where it's known without fetching everything.
  let total = plan.total;
  if (total == null && !sub) {
    const se = (m.sources || []).find(x => (x.release ? `${x.distro}/${x.release}` : x.distro) === src);
    total = se && se.sections ? se.sections[chapter(key)] : null;
  }
  const chunkHTML = async (n) => {
    const items = await plan.load(n);
    let out = "", prev = plan.prevLetter(n), open = false;
    for (const it of items) {
      const l = letterOf(it.name);
      if (!open || l !== prev) {
        if (open) out += "</ul>";
        out += l === prev ? `<h2 class="letter cont">${esc(l)} <span class="sub">(continued)</span></h2>` : `<h2 class="letter" id="letter-${esc(l)}">${esc(l)}</h2>`;
        out += '<ul class="pages">';
        open = true;
        prev = l;
      }
      const name = isSeries(it.sec) ? `${it.sec.toUpperCase()} ${it.name}` : it.name;
      out += `<li><a href="${esc(it.url)}"><span class="name">${esc(name)}</span>${isSeries(it.sec) ? "" : `<span class="sec">(${esc(it.sec)})</span>`}</a>` +
        `<span class="desc">${esc(it.desc)}</span>${it.girl ? '<span class="done" title="girlified">✨</span>' : ""}</li>`;
    }
    return open ? out + "</ul>" : out;
  };
  const chunks = Math.max(1, plan.count);
  const first = Math.min(Math.max(1, start), chunks);
  let body = `<nav class="letters" aria-label="Jump to letter">${[...plan.letters].map(([l, c]) => `<a href="#letter-${esc(l)}" data-chunk="${c}">${esc(l)}</a>`).join("")}</nav>` +
    `<div class="listing"><div class="chunk" data-chunk="${first}">${await chunkHTML(first)}</div></div>`;
  if (!plan.count || total === 0) body = '<div class="empty">nothing here, bestie 😭</div>';
  const crumbs = `<a href="${esc(base)}">home</a> › ` + (src ? `<a href="${esc(browse({ src }))}">${esc(sourceLabel(src))}</a> › ` : "");
  const save = "caches" in globalThis ? '<p class="save"><button type="button" class="pill" id="save"></button></p>' : "";
  const count = total == null ? "" : plural(total);
  const heading = isSeries(label) ? `${name} ${emoji}` : `${sectionLabel(label)} · ${name} ${emoji}`;
  h(`<div class="card"><nav class="crumbs">${crumbs}${esc(sectionLabel(label))}</nav>` +
    `<div class="pagehead"><h1>${esc(heading)}</h1><p class="sub">${count}${src ? (count ? " in " : "in ") + esc(sourceLabel(src)) : ""}</p>${save}</div>` +
    sourceChips(m, src, chapter(key)) + chips + body + "</div>");
  if (location.href !== routingFor) return;
  const button = app.querySelector("#save");
  if (button) saveButton(button, cache, sub || key, src);
  const listing = app.querySelector(".listing");
  if (listing && chunks > 1 && "IntersectionObserver" in globalThis) {
    const list = new VirtualList(listing, { count: chunks, load: chunkHTML,
      onChunk: pageURL ? (n) => { if (location.href === routingFor) { history.replaceState(history.state, "", pageURL(n)); shown = routingFor = renderedFor = revealedFor = location.href; } } : undefined });
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
    for (let n = first + 1; n <= chunks; n++) listing.insertAdjacentHTML("beforeend", `<div class="chunk" data-chunk="${n}">${await chunkHTML(n)}</div>`);
  }
}

// letterOf is the group a name is listed under, as in site.letterOf: its
// first letter, or a number's thousand.
function letterOf(n) {
  if (/^[0-9]+$/.test(n)) return +n < 1000 ? "1–999" : `${Math.floor(+n / 1000) * 1000}s`;
  return /^[a-z]/i.test(n) ? n[0].toUpperCase() : "#";
}
// nameCmp orders names as manpage.NameLess does: numbers by value.
function nameCmp(a, b) {
  const num = (x) => /^[0-9]+$/.test(x);
  if (num(a) && num(b) && a.replace(/^0+/, "").length !== b.replace(/^0+/, "").length) {
    return a.replace(/^0+/, "").length - b.replace(/^0+/, "").length;
  }
  return a < b ? -1 : a > b ? 1 : 0;
}
const byName = (a, b) => nameCmp(a.name, b.name) || (a.sec < b.sec ? -1 : a.sec > b.sec ? 1 : 0);

// listingPlan works out a listing's chunks: {count, load(n) resolving to
// the chunk's items, letters (letter → the chunk it starts in), total
// (null if unknown), subs (section → count, or null), prevLetter(n)}. Short
// sections' indexes are read whole and cut into chunks of CHUNK; long
// sections' shards are read as they're needed, a chunk being the names
// between one shard's start and the next's, across the chapter's sections.
async function listingPlan(entries, src, sub) {
  const indexes = await Promise.all(entries.map(sectionIndex));
  const want = (p) => !src || (p.sources || []).includes(src);
  const toItem = (p, sec) => {
    const other = src && p.other ? p.other[src] : "";
    return { name: p.name, sec, desc: p.description || "", girl: !other && (p.variants || []).includes("girl"),
      url: src ? browse({ page: p.name, sec, src }) : staticPage(p.name, sec) };
  };
  if (!indexes.some(si => si.shards && si.shards.length)) {
    const subs = new Map();
    const items = [];
    for (const si of indexes) {
      for (const p of si.pages || []) {
        if (!want(p)) continue;
        subs.set(si.section, (subs.get(si.section) || 0) + 1);
        if (!sub || si.section === sub) items.push(toItem(p, si.section));
      }
    }
    // In the static listings' order, so chunks are their pages.
    items.sort((a, b) => nameCmp(a.name.toLowerCase(), b.name.toLowerCase()) || (a.sec < b.sec ? -1 : a.sec > b.sec ? 1 : 0));
    const letters = new Map();
    items.forEach((it, i) => { const l = letterOf(it.name); if (!letters.has(l)) letters.set(l, Math.floor(i / CHUNK) + 1); });
    return {
      count: Math.ceil(items.length / CHUNK), total: items.length, subs, letters,
      load: async (n) => items.slice((n - 1) * CHUNK, n * CHUNK),
      prevLetter: (n) => (n > 1 ? letterOf(items[(n - 1) * CHUNK - 1].name) : ""),
    };
  }
  const parts = [], subs = new Map();
  let total = 0;
  for (const si of indexes) {
    const n = si.count || (si.pages || []).length;
    subs.set(si.section, src ? null : n);
    if (sub && si.section !== sub) continue;
    total += n;
    for (const r of si.shards || []) parts.push({ sec: si.section, first: r.first, last: r.last, sha: r.sha256 });
    const ps = si.pages || [];
    if (ps.length) parts.push({ sec: si.section, first: ps[0].name, last: ps[ps.length - 1].name, pages: ps });
  }
  const bounds = [...new Set(parts.map(p => p.first))].sort(nameCmp);
  const letters = new Map();
  bounds.forEach((b, i) => { const l = letterOf(b); if (!letters.has(l)) letters.set(l, i + 1); });
  return {
    count: bounds.length, total: src ? null : total, subs, letters,
    load: async (n) => {
      const lo = bounds[n - 1], hi = n < bounds.length ? bounds[n] : null;
      const inRange = (name) => nameCmp(name, lo) >= 0 && (hi === null || nameCmp(name, hi) < 0);
      const items = [];
      await Promise.all(parts.filter(p => nameCmp(p.last, lo) >= 0 && (hi === null || nameCmp(p.first, hi) < 0)).map(async (part) => {
        const pages = part.pages || (await cache.get(sectionShard(part.sha))).pages;
        for (const p of pages) if (inRange(p.name) && want(p)) items.push(toItem(p, part.sec));
      }));
      return items.sort(byName);
    },
    prevLetter: (n) => (n > 1 && letterOf(bounds[n - 2]) === letterOf(bounds[n - 1]) ? letterOf(bounds[n - 1]) : ""),
  };
}

// LAYOUT is how series of documents are laid out, where it isn't man's: as
// rfc.Indent and rfc.Width.
const LAYOUT = { rfc: { indent: 3, width: 72, titleBlock: true } };

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
    ({ html: body, toc } = renderOriginalHTML(doc, { link, ...(LAYOUT[sec] || {}) }));
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
  const when = isSeries(sec) && ver.packages && ver.packages[0] ? publishedLabel(ver.packages[0].version) : "";
  // Switching between versions, as on the static page: this one first,
  // then the latest, then the newest added.
  let switcher = ver.version ? `version ${esc(ver.version)}, ` : "";
  if (versions.length > 1) {
    const items = [ver, ...versions.filter(x => x !== ver)].map(x => {
      const label = (x.version ? x.version + " · " : "") + (sourceLabels(x).join(", ") || "added " + (x.added || "").slice(0, 10)) +
        (x.source === l.current ? " (latest)" : "");
      return x === ver ? `<li><strong>${esc(label)}</strong></li>`
        : `<li><a href="${esc(x.source === l.current && !src ? staticPage(name, sec) : browse({ page: name, sec, version: slug(x), src }))}">${esc(label)}</a></li>`;
    }).join("");
    switcher = `<details class="versions" id="versions"><summary>${ver.version ? "version " + esc(ver.version) : "this version"} <span class="n">· ${versions.length} versions</span></summary><ul>${items}</ul></details>, `;
  }
  const inLine = keys.length ? `<div class="meta in">${isSeries(sec) ? `📅 ${when ? "published " + esc(when) + ", " : ""}from ` : `📦 ${switcher}in `}` +
    keys.map(k => `<a href="${esc(browse({ page: name, sec, src: k }))}">${esc(sourceLabel(k))}</a>`).join(", ") + "</div>" : "";
  let notice = "";
  if (!latest) notice += `<div class="notice">🕰️ this isn't the latest version of ${esc(name)}(${esc(sec)}). <a href="${esc(staticPage(name, sec))}">here's the latest</a>.</div>`;
  if (which === "man" && variants.length === 1) notice += `<div class="notice">💤 she hasn't gotten to this version yet, so here's the original.</div>`;
  const crumbs = `<nav class="crumbs"><a href="${esc(base)}">home</a> › ` + (src
    ? `<a href="${esc(browse({ src }))}">${esc(sourceLabel(src))}</a> › <a href="${esc(browse({ src, section: chapter(sec), sub: chapter(sec) !== sec ? sec : "" }))}">${esc(sectionLabel(sec))}</a>`
    : `<a href="${esc(listingURL(chapter(sec), chapter(sec) !== sec ? sec : "", 1))}">${esc(sectionLabel(sec))}</a>`) + ` › ${esc(title)}</nav>`;
  const tocHTML = toc.length ? `<nav class="toc card" aria-label="Contents"><h2>✨ contents</h2><ul>` +
    toc.map(t => `<li${t.level === 2 ? ' class="sub"' : ""}><a href="#${esc(t.id)}">${esc(t.text)}</a></li>`).join("") + "</ul></nav>" : "";
  const lede = isSeries(sec) && ver.description ? `<p class="lede">${esc(ver.description)}</p>` : "";
  h(`<div class="layout"><article class="card manpage">${crumbs}<h1>${esc(title)}${esc(suffix)}</h1>${lede}` +
    `<div class="meta">${meta}</div>${inLine}${notice}${body}</article>${tocHTML}</div>`);
  if (at) {
    afterShow = () => {
      const d = app.querySelector(`[id="${at}"]`);
      if (d) {
        d.open = true;
        d.scrollIntoView({ block: "center" });
      }
    };
  }
}

// staticListing shows a listing by its static pages (which are smaller than
// the section indexes they're made from, and so the same as what's served),
// scrolling through them as the static page itself does. Offline, it's made
// from the section indexes saved, if they are.
async function staticListing(key, sub, start) {
  const pageURL = (n) => listingURL(key, sub, n);
  const fetchPage = async (n) => {
    const r = await fetch(pageURL(n));
    if (!r.ok) {
      const e = new Error(`${pageURL(n)}: ${r.status}`);
      e.status = r.status;
      throw e;
    }
    return new DOMParser().parseFromString(await r.text(), "text/html");
  };
  let doc;
  try {
    doc = await fetchPage(start);
  } catch (e) {
    if (e instanceof TypeError) return sourceSection("", key, sub, { start, pageURL });
    throw e;
  }
  const card = doc.querySelector(".static-view > .card");
  if (!card) return oops("that listing's broken 💔");
  h(card.outerHTML);
  if (location.href !== routingFor) return;
  document.title = doc.title;
  app.querySelector(".pager")?.setAttribute("hidden", "");
  for (const b of app.querySelectorAll("button[data-save-section]")) {
    b.hidden = !("caches" in globalThis);
    saveButton(b, cache, b.dataset.saveSection);
  }
  const listing = app.querySelector(".listing[data-pages]");
  const pages = listing ? +listing.dataset.pages : 1;
  if (pages < 2 || !("IntersectionObserver" in globalThis)) return;
  const list = new VirtualList(listing, {
    count: pages,
    load: async (n) => (await fetchPage(n)).querySelector(".listing .chunk")?.innerHTML || "",
    onChunk: (n) => {
      if (location.href !== routingFor) return;
      history.replaceState(history.state, "", pageURL(n));
      shown = routingFor = renderedFor = revealedFor = location.href;
    },
  });
  current = list;
  app.querySelector(".letters")?.addEventListener("click", (e) => {
    const a = e.target.closest("a[href]");
    if (!a) return;
    e.preventDefault();
    e.stopPropagation();
    const u = new URL(a.href);
    const m = u.pathname.match(/\/page\/(\d+)\/$/);
    list.jump(m ? +m[1] : 1, decodeURIComponent(u.hash.slice(1)));
  });
  list.fill();
  (globalThis.requestIdleCallback || ((f) => setTimeout(f, 200)))(() => list.prefetch());
}

// searchView is the search page, in the app.
async function searchView(params) {
  document.title = "search · girlpages 💅";
  const save = "caches" in globalThis ? '<p class="save"><button type="button" class="pill" data-save-search="words"></button>' +
    '<button type="button" class="pill" data-save-search="typos"></button><a class="pill" id="search-saved" hidden></a></p>' : "";
  h(`<div class="card" id="search" data-repo="${esc(app.dataset.repo)}" data-base="${esc(base)}"><div class="crumbs"><a href="${esc(base)}">home</a> › search</div>` +
    `<div class="pagehead"><h1>🔎 search</h1><p class="sub" id="search-status">type something up there, bestie</p>${save}</div>` +
    '<nav class="chips sources" id="search-sources" aria-label="Search within a source"></nav><ul class="pages" id="search-results"></ul></div>');
  if (location.href !== routingFor) return;
  for (const b of app.querySelectorAll("button[data-save-search]")) saveButton(b, cache, "search:" + b.dataset.saveSearch);
  reveal();
  const url = routingFor;
  await searchPage(app.querySelector("#search"), params, { current: () => location.href === url });
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
let renderedFor = "";    // the address whose view is in place
let revealedFor = "";    // the address whose view is shown

// reveal shows the view in place, if it's still wanted: views that go on
// working once they're showing something (search) call it early.
function reveal() {
  if (location.href !== routingFor || revealedFor === routingFor) return;
  revealedFor = routingFor;
  showApp();
  if (afterShow) afterShow();
  else scrollTo(0, history.state?.y ?? 0);
}

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

// viewOf is the view an address shows in the app: a route, or one of the
// static site's pages (which, once the app is running, it shows itself).
function viewOf(u) {
  if (u.origin !== location.origin || !u.pathname.startsWith(base)) return null;
  if (u.pathname === base && isRoute(u.hash)) return { kind: "route", hash: u.hash };
  if (u.pathname === base) return { kind: "home" };
  let parts;
  try {
    parts = u.pathname.slice(base.length).split("/").filter(Boolean).map(decodeURIComponent).map(unsegment);
  } catch {
    return null;
  }
  if (parts[0] === "man" && parts.length === 3) return { kind: "page", sec: parts[1], name: parts[2] };
  if (isSeries(parts[0]) && !RESERVED.has(parts[0])) {
    // A series: rfc/9110/, or its listing, rfc/ or rfc/page/2/.
    if (parts.length === 2 && parts[1] !== "page") return { kind: "page", sec: parts[0], name: parts[1] };
    if (parts.length === 1) return { kind: "section", key: parts[0], sub: "", page: 1 };
    if (parts.length === 3 && parts[1] === "page") return { kind: "section", key: parts[0], sub: "", page: +parts[2] || 1 };
  }
  if (parts[0] === "search" && parts.length === 1) return { kind: "search", params: u.searchParams };
  if (parts[0] === "section") {
    let page = 1;
    if (parts.length >= 4 && parts[parts.length - 2] === "page") page = +parts.splice(-2)[1] || 1;
    if (parts.length === 2 || parts.length === 3) return { kind: "section", key: parts[1], sub: parts[2] || "", page };
  }
  return null;
}

// unsegment undoes site.SegmentName (names starting with a dot are escaped).
const unsegment = (s) => (s.startsWith("%") ? decodeURIComponent(s) : s);

// publishedLabel is a "2022-06" date as "June 2022", as site.published.
function publishedLabel(d) {
  const m = /^(\d{4})-(\d{2})$/.exec(d || "");
  if (!m) return d || "";
  const months = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
  return `${months[+m[2] - 1] || m[2]} ${m[1]}`;
}

// listingURL is a static listing's page n.
const listingURL = (key, sub, n) => listingAt(base, key, sub, n);

// sectionLabel names a section in breadcrumbs: "section 3ssl", or a
// series' name ("RFCs").
const sectionLabel = (sec) => (isSeries(sec) ? sectionName(sec)[0] : `section ${sec}`);

// Top-level directories of the site that aren't series.
const RESERVED = new Set(["man", "section", "search", "browse", "offline", "static", "repo", "src"]);

async function route() {
  current?.destroy();
  current = null;
  const v = viewOf(new URL(location.href));
  switch (v && v.kind) {
    case "home": return sourceHome("");
    case "page": return pageView(v.name, v.sec, [], "");
    case "search": return searchView(v.params);
    case "section": return staticListing(v.key, v.sub, v.page);
  }
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
  if (history.state?.own || (location.pathname + location.search === docURL && !isRoute(location.hash))) return showStatic();
  if (!viewOf(new URL(url))) return location.reload();
  routingFor = url;
  afterShow = null;
  const slow = setTimeout(() => {
    if (routingFor !== url || renderedFor === url) return;
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
  reveal();
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
      // (The page's own view, hidden, may have the same ids.)
      app.querySelector(`[id="${CSS.escape(decodeURIComponent(a.getAttribute("href").slice(1)))}"]`)?.scrollIntoView();
    });
    addEventListener("popstate", main);
    addEventListener("hashchange", main);
  }
  return main();
}

// prefetch fetches what a view will need, so following a link to it is
// quick.
export async function prefetch(href) {
  const v = viewOf(new URL(href, location.href));
  if (!v) return;
  if (v.kind === "page") return loadPage(v.name, v.sec, [], "");
  if (v.kind !== "route") return;
  const r = await parse(v.hash);
  if (r.kind === "page") await loadPage(r.name, r.sec, r.rest, r.src);
}

// handles reports whether the app shows an address itself.
export const handles = (href) => !!viewOf(new URL(href, location.href));
