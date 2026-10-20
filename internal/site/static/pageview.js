// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The app's view of a page: any version, any variant, from any source.

import { html, raw, HTML } from "./html.js";
import { renderHTML, renderOriginalHTML } from "./render.js";
import { errorKind } from "./cache.js";
import { Drawer } from "./draw.js";
import { Spy } from "./spy.js";
import { pathEscape, segmentName } from "./urls.js";
import { chapter, isSeries, sectionLabel, sourceLabel } from "./manual.js";

// createPageView makes the page view for an app: ctx has its element (el),
// the site's base, listing(name, sec) and variant(source, which, sha) to
// read the repository, link makers (browse, staticPage, listingURL), and
// oops(nav, message) for errors.
export function createPageView({ el: app, base, listing, findName, variant, browse, staticPage, listingURL, oops }) {
  // How variants are offered. Others (made with other prompts) get their name.
  function variantLabel(v) {
    return v === "girl" ? "💅 girly" : v === "man" ? "📜 original" : "✨ " + v;
  }

  // Girly first, the original last, anything else between.
  function variantOrder(a, b) {
    const rank = (v) => (v === "girl" ? 0 : v === "man" ? 2 : 1);
    return rank(a) - rank(b) || (a < b ? -1 : a > b ? 1 : 0);
  }

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

  // packageLabel describes a package, as repo.Package.String does.
  function packageLabel(p) {
    const parts = [];
    if (p.distro) parts.push(p.release ? `${p.distro} ${p.release}` : p.distro);
    if (p.name) parts.push(p.version ? `${p.name} ${p.version}` : p.name);
    return parts.join(": ") + (p.url ? ` from ${p.url}` : "");
  }

  // publishedLabel is a "2022-06" date as "June 2022", as site.published.
  function publishedLabel(d) {
    const m = /^(\d{4})-(\d{2})$/.exec(d || "");
    if (!m) return d || "";
    const months = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
    return `${months[+m[2] - 1] || m[2]} ${m[1]}`;
  }

  // LAYOUT is how series of documents are laid out, where it isn't man's: as
  // rfc.Indent and rfc.Width.
  const LAYOUT = { rfc: { indent: 3, width: 72, titleBlock: true } };

  // loadPage fetches what a page view needs; rest is what follows its name
  // in the route: [version] [variant] [mono] [versions]. It resolves to the
  // view's data, or {error} to show instead.
  async function loadPage(name, sec, rest, src) {
    let l;
    try {
      l = await listing(name, sec);
    } catch (e) {
      if (errorKind(e) === "offline") return { error: `you're offline, and ${name}(${sec}) isn't saved 📵` };
      if (errorKind(e) !== "missing") throw e;
      // A reference in another case than the page's (PCRE(3) for pcre(3)).
      const other = await findName(name, sec).catch(() => "");
      if (other) return { moved: other };
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
    /** @type {any} */
    let ver = null;
    let which = "";
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
  async function pageView(nav, name, sec, rest, src) {
    const d = await loadPage(name, sec, rest, src);
    if ("moved" in d) {
      const u = new URL(location.href);
      const seg = (n) => `/${pathEscape(segmentName(n))}/`;
      u.pathname = u.pathname.replace(seg(name), seg(d.moved));
      u.searchParams.forEach((v, k) => { if (v === name) u.searchParams.set(k, d.moved); });
      return nav.redirect(u.href);
    }
    if ("error" in d) return oops(nav, d.error);
    const { l, ver, which, variants, versions, slug, doc, at, mono } = d;
    const title = ver.title || `${name.toUpperCase()}(${sec})`;
    const latest = ver.source === l.current;
    const suffix = which === "girl" ? "" : which === "man" ? " · original" : " · " + which;
    nav.setTitle(`${name}(${sec})${ver.description ? " — " + ver.description : ""}${suffix} · girlpages 💅`);
    const here = (extra) => browse({ page: name, sec, version: slug(ver), src, ...extra });

    // The page itself: rendered from markup, reflowed from the original,
    // or the original as man prints it.
    let body, toc = [];
    const link = src ? (n, s) => browse({ page: n, sec: s, src }) : (n, s) => staticPage(n, s);
    if (doc.syntax === "girlpages-markup") {
      const r = renderHTML(variantText(doc), { link });
      body = html`<div class="content">${raw(r.html)}</div>`;
      toc = r.toc;
    } else if (!mono) {
      const r = renderOriginalHTML(doc, { link, ...(LAYOUT[sec] || {}) });
      body = html`<div class="content reflowed">${raw(r.html)}</div>`;
      toc = r.toc;
    } else {
      body = html`<div class="original">${doc.header && html`<pre class="manhead">${doc.header}</pre>`}${
        doc.sections.map(s => html`${s.heading && html`<h2>${s.heading}</h2>`}<pre>${s.body}</pre>
`)}${
        doc.footer && html`<pre class="manhead">${doc.footer}</pre>`}</div>`;
    }

    // Its variants, and the original monospaced.
    const pills = [];
    if (variants.length > 1) {
      for (const k of variants) {
        pills.push(k === which ? html`<span class="pill on">${variantLabel(k)}</span>`
          : html`<a class="pill" href="${here({ variant: k === variants[0] ? "" : k })}">${variantLabel(k)}</a>`);
      }
    }
    if (doc.syntax !== "girlpages-markup") {
      pills.push(html`<a class="pill${mono ? " on" : ""}" href="${here({ variant: which === variants[0] ? "" : which, mono: !mono })}" title="as man prints it">⌨️ monospace</a>`);
    }

    // Where it's from, and its versions, as on the static page: this one
    // first, then the latest, then the newest added.
    const keys = sourceKeys(ver);
    const when = isSeries(sec) && ver.packages && ver.packages[0] ? publishedLabel(ver.packages[0].version) : "";
    /** @type {string | HTML} */
    let switcher = ver.version && `version ${ver.version}, `;
    if (versions.length > 1) {
      const items = [ver, ...versions.filter(x => x !== ver)].map(x => {
        const label = (x.version ? x.version + " · " : "") + (sourceLabels(x).join(", ") || "added " + (x.added || "").slice(0, 10)) +
          (x.source === l.current ? " (latest)" : "");
        return x === ver ? html`<li><strong>${label}</strong></li>`
          : html`<li><a href="${x.source === l.current && !src ? staticPage(name, sec) : browse({ page: name, sec, version: slug(x), src })}">${label}</a></li>`;
      });
      switcher = html`<details class="versions" id="versions"><summary>${ver.version ? "version " + ver.version : "this version"} <span class="n">· ${versions.length} versions</span></summary><ul>${items}</ul></details>, `;
    }
    const inLine = keys.length > 0 && html`<div class="meta in">${isSeries(sec) ? `📅 ${when ? `published ${when}, ` : ""}from ` : html`📦 ${switcher}in `}${
      keys.map((k, i) => html`${i > 0 && ", "}<a href="${browse({ page: name, sec, src: k })}">${sourceLabel(k)}</a>`)}</div>`;

    const notices = [
      !latest && html`<div class="notice">🕰️ this isn't the latest version of ${name}(${sec}). <a href="${staticPage(name, sec)}">here's the latest</a>.</div>`,
      which === "man" && variants.length === 1 && html`<div class="notice">💤 she hasn't gotten to this version yet, so here's the original.</div>`,
    ];
    const sub = chapter(sec) !== sec ? sec : "";
    const crumbs = html`<nav class="crumbs"><a href="${base}">home</a> › ${src
      ? html`<a href="${browse({ src })}">${sourceLabel(src)}</a> › <a href="${browse({ src, section: chapter(sec), sub })}">${sectionLabel(sec)}</a>`
      : html`<a href="${listingURL(chapter(sec), sub, 1)}">${sectionLabel(sec)}</a>`} › ${title}</nav>`;
    const contents = toc.length > 0 && html`<nav class="toc card" aria-label="Contents"><h2>✨ contents</h2><ul>${
      toc.map(t => html`<li${t.level === 2 && raw(' class="sub"')}><a href="#${t.id}">${t.text}</a></li>`)}</ul></nav>`;
    const lede = isSeries(sec) && ver.description && html`<p class="lede">${ver.description}</p>`;
    // Where it's from: its packages, and what made it.
    const g = doc.generator, made = g && (g.generated && !g.generated.startsWith("0001") ? g.generated.slice(0, 10) : "");
    const from = [
      ...(ver.packages || []).map(p => html`<li>${packageLabel(p)}</li>`),
      g && html`<li>💖 ${which === "girl" ? "girlified" : "rewritten"} by ${g.model || g.backend || "a model"}${made && ` on ${made}`}</li>`,
    ].filter(Boolean);
    const provenance = from.length > 0 && html`<div class="provenance"><details><summary>📦 where this page comes from</summary><ul>${from}</ul></details></div>`;

    if (!nav.render(html`<div class="layout"><article class="card manpage">${crumbs}<h1>${title}${suffix}</h1>${lede}${inLine}${
      pills.length > 0 && html`<div class="meta">${pills}</div>`}${notices}${body}${provenance}</article>${contents}</div>`)) return;
    // Drawn ahead of scrolling (see draw.js).
    nav.own(new Drawer()).watch(app.querySelectorAll(".content > *, .original > *"));
    // The contents follow the reading (see spy.js).
    const side = /** @type {HTMLElement | null} */ (app.querySelector(".toc"));
    if (side) nav.own(new Spy(side)).watch();
    if (at) {
      nav.scrollTo(() => {
        const d = app.querySelector(`[id="${at}"]`);
        if (d) {
          d.open = true;
          d.scrollIntoView({ block: "center" });
        }
      });
    }
  }

  return { loadPage, pageView };
}
