// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The browse app: everything the static site doesn't render itself, read
// straight from the repository. /browse/ takes:
//
//   ?src=debian/trixie                      a source's home
//   ?src=debian/trixie&section=1            its section 1 (&sub=1ssl for one subsection)
//   ?page=ls&sec=1                          a page (its latest version)
//   …&v=<source hash>                       a particular version
//   …&variant=man                           another variant: man is the original;
//                                           girl is the default, and there may be more
//   …&src=debian/trixie                     the version a source has, and links that stay in it

import { repoCache } from "./girlpages.js";
import { renderHTML, esc } from "./render.js";
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
function browse(params) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  return `${base}browse/?${q}`;
}
const sourceLabel = (key) => key.replace("/", " ");

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

function h(html) {
  app.innerHTML = html;
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

async function sourceHome(src) {
  const m = await manifest();
  const s = (m.sources || []).find(x => (x.release ? `${x.distro}/${x.release}` : x.distro) === src);
  if (!s) return oops("there's no source called that here 🤔");
  document.title = `${sourceLabel(src)} · girlpages 💅`;
  const keys = Object.keys(s.sections || {}).sort((a, b) => (/\d/.test(a[0]) !== /\d/.test(b[0]) ? (/\d/.test(a[0]) ? -1 : 1) : a < b ? -1 : 1));
  let cards = "";
  for (const k of keys) {
    const [name, emoji] = sectionName(k);
    cards += `<a class="section-card" href="${esc(browse({ src, section: k }))}"><span class="emoji">${emoji}</span>` +
      `<div class="num">${esc(k)}</div><div class="name">${esc(name)}</div><div class="count">${plural(s.sections[k])}</div></a>`;
  }
  h(`<section class="hero"><h1>✨ girlpages ✨</h1><p>the <strong>${esc(sourceLabel(src))}</strong> manual, but make it girly 💅</p>` +
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
  const groups = new Map();
  for (const it of items) {
    const l = /^[a-z]/i.test(it.name) ? it.name[0].toUpperCase() : "#";
    if (!groups.has(l)) groups.set(l, []);
    groups.get(l).push(it);
  }
  let body = `<nav class="letters" aria-label="Jump to letter">${[...groups.keys()].map(l => `<a href="#letter-${esc(l)}">${esc(l)}</a>`).join("")}</nav>`;
  for (const [l, its] of groups) {
    body += `<h2 class="letter" id="letter-${esc(l)}">${esc(l)}</h2><ul class="pages">` + its.map(it =>
      `<li><a href="${esc(it.url)}"><span class="name">${esc(it.name)}</span><span class="sec">(${esc(it.sec)})</span></a>` +
      `<span class="desc">${esc(it.desc)}</span>${it.girl ? '<span class="done" title="girlified">✨</span>' : ""}</li>`).join("") + "</ul>";
  }
  if (!items.length) body = '<div class="empty">nothing here, bestie 😭</div>';
  h(`<div class="card"><nav class="crumbs"><a href="${esc(base)}">home</a> › <a href="${esc(browse({ src }))}">${esc(sourceLabel(src))}</a> › section ${esc(label)}</nav>` +
    `<div class="pagehead"><h1>section ${esc(label)} · ${esc(name)} ${emoji}</h1><p class="sub">${plural(items.length)} in ${esc(sourceLabel(src))}</p></div>` +
    sourceChips(m, src, key[0]) + chips + body + "</div>");
}

async function pageView(name, sec, v, which, src) {
  let l;
  try {
    l = await listing(name, sec);
  } catch (e) {
    return oops(`there's no ${name}(${sec}) here 😭`);
  }
  const ver = l.versions.find(x => x.source === v) ||
    l.versions.find(x => x.source === (src && l.sources ? l.sources[src] : "")) ||
    l.versions.find(x => x.source === l.current) || l.versions[0];
  if (!ver) return oops("that page has no versions 🤔");
  const has = (k) => ver.variants && ver.variants[k];
  const variants = Object.keys(ver.variants || {}).filter(k => /^[a-z][a-z0-9-]{0,31}$/.test(k)).sort(variantOrder);
  if (!which || !has(which)) which = variants[0] || "man";
  const doc = await variant(ver.source, which, ver.variants[which].sha256);
  const title = ver.title || `${name.toUpperCase()}(${sec})`;
  const latest = ver.source === l.current;
  const suffix = which === "girl" ? "" : which === "man" ? " · original" : " · " + which;
  document.title = `${name}(${sec})${ver.description ? " — " + ver.description : ""}${suffix} · girlpages 💅`;
  const here = (extra) => browse({ page: name, sec, v: ver.source === l.current ? "" : ver.source, src, ...extra });

  let body = "", toc = [];
  if (doc.syntax === "girlpages-markup") {
    const link = src ? (n, s) => browse({ page: n, sec: s, src }) : (n, s) => staticPage(n, s);
    ({ html: body, toc } = renderHTML(variantText(doc), { link }));
    body = `<div class="content">${body}</div>`;
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
  const keys = sourceKeys(ver);
  const inLine = keys.length ? `<div class="meta in">📦 ${ver.version ? "version " + esc(ver.version) + ", " : ""}in ` +
    keys.map(k => `<a href="${esc(browse({ page: name, sec, src: k }))}">${esc(sourceLabel(k))}</a>`).join(", ") + "</div>" : "";
  let notice = "";
  if (!latest) notice += `<div class="notice">🕰️ this isn't the latest version of ${esc(name)}(${esc(sec)}). <a href="${esc(staticPage(name, sec))}">here's the latest</a>.</div>`;
  if (which === "man" && variants.length === 1) notice += `<div class="notice">💤 she hasn't gotten to this version yet, so here's the original.</div>`;
  const versions = [...l.versions].sort((a, b) => (a.source === l.current ? -1 : b.source === l.current ? 1 : (b.added || "").localeCompare(a.added || "")));
  const vlist = versions.length > 1 ? `<div class="provenance" id="versions"><details open><summary>🕰️ ${versions.length} versions, and where they're from</summary><ul>` +
    versions.map(x => {
      const label = (x.version ? x.version + " · " : "") + (sourceLabels(x).join(", ") || "added " + (x.added || "").slice(0, 10)) +
        (x.source === l.current ? " (latest)" : "");
      return x.source === ver.source ? `<li><strong>${esc(label)}</strong> (this one)</li>`
        : `<li><a href="${esc(browse({ page: name, sec, v: x.source === l.current ? "" : x.source, src }))}">${esc(label)}</a></li>`;
    }).join("") + "</ul></details></div>" : "";
  const crumbs = `<nav class="crumbs"><a href="${esc(base)}">home</a> › ` + (src
    ? `<a href="${esc(browse({ src }))}">${esc(sourceLabel(src))}</a> › <a href="${esc(browse({ src, section: sec[0], sub: sec.length > 1 ? sec : "" }))}">section ${esc(sec)}</a>`
    : `<a href="${esc(base)}section/${encodeURIComponent(sec[0])}/">section ${esc(sec)}</a>`) + ` › ${esc(title)}</nav>`;
  const tocHTML = toc.length ? `<nav class="toc card" aria-label="Contents"><h2>✨ contents</h2><ul>` +
    toc.map(t => `<li${t.level === 2 ? ' class="sub"' : ""}><a href="#${esc(t.id)}">${esc(t.text)}</a></li>`).join("") + "</ul></nav>" : "";
  h(`<div class="layout"><article class="card manpage">${crumbs}<h1>${esc(title)}${esc(suffix)}</h1>` +
    `<div class="meta">${meta}</div>${inLine}${notice}${body}${vlist}</article>${tocHTML}</div>`);
  if (location.hash) document.getElementById(location.hash.slice(1))?.scrollIntoView();
}

function oops(msg) {
  h(`<div class="card waiting"><div class="spinner">😭</div><h1>${esc(msg)}</h1><p><a href="${esc(base)}">home</a> · <a href="${esc(base)}search/">search</a></p></div>`);
}

async function main() {
  const q = new URLSearchParams(location.search);
  const page = q.get("page"), sec = q.get("sec"), src = q.get("src") || "", section = q.get("section");
  try {
    if (page && sec) await pageView(page, sec, q.get("v"), q.get("variant"), src);
    else if (src && section) await sourceSection(src, section, q.get("sub") || "");
    else if (src) await sourceHome(src);
    else location.replace(base);
  } catch (e) {
    oops(e instanceof TypeError ? "you're offline, and this isn't saved yet 📵" : `something broke 💔 ${e.message}`);
  }
}

main();
