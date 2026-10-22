// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The app's views of the repository, other than pages (see pageview.js):
// home (everything, or a source), a section's listing, and search.

import { html } from "./html.js";
import { chapter, isSeries, sectionName, sectionLabel, sourceLabel, plural } from "./manual.js";
import { saveButton, sourceNav } from "./widgets.js";
import { searchPage } from "./search.js";
import { Listing, chunkMaker } from "./listing.js";

// createViews makes the views for an app: ctx has its element (el), the
// site's base, the repository cache and manifest(), link makers (browse,
// listingURL), and its listings.
export function createViews({ el: app, base, cache, manifest, browse, listingURL, listings }) {
  const { chapterPlan } = listings;

  function sourceChips(m, on, section) {
    const sources = (m.sources || []).filter(s => !section || (s.sections || {})[section]);
    const href = (key) => (key ? browse({ src: key, section }) : section ? listingURL(section, "", 1) : base);
    return html`<nav class="chips sources" aria-label="Browse by source">${sourceNav(sources, { on, href })}</nav>`;
  }

  // everything is the whole repository as a source: the latest version of
  // every page.
  function everything(m) {
    const sections = {};
    for (const e of m.sections || []) sections[chapter(e.section)] = (sections[chapter(e.section)] || 0) + e.pages;
    return { pages: m.pages, sections };
  }

  // sourceKey is a manifest source's key: "debian", or "debian/trixie".
  const sourceKey = (x) => (x.release ? `${x.distro}/${x.release}` : x.distro);

  async function sourceHome(nav, src) {
    const m = await manifest();
    const s = src ? (m.sources || []).find(x => sourceKey(x) === src) : everything(m);
    if (!s) return oops(nav, "there's no source called that here 🤔");
    // Chapters with numbers first, then the rest.
    const digit = (k) => /\d/.test(k[0]);
    const keys = Object.keys(s.sections || {}).sort((a, b) => (digit(a) !== digit(b) ? (digit(a) ? -1 : 1) : a < b ? -1 : 1));
    const cards = keys.map(k => {
      const [name, emoji] = sectionName(k);
      return html`<a class="section-card" href="${src ? browse({ src, section: k }) : listingURL(k, "", 1)}"><span class="emoji">${emoji}</span>
        <div class="num">${k}</div><div class="name">${name}</div><div class="count">${plural(s.sections[k])}</div></a>`;
    });
    nav.setTitle(src ? `${sourceLabel(src)} · girlpages 💅` : "the manual, but make it girly · girlpages 💅");
    const search = !src && html`<form class="hero-search" action="${base}search/" role="search"><input type="search" name="q" placeholder="what are we reading today? 🔎" aria-label="Search manual pages"></form>`;
    nav.render(html`<section class="hero"><h1>✨ girlpages ✨</h1>
      <p>the ${src && html`<strong>${sourceLabel(src)}</strong> `}manual, but make it girly 💅</p>${search}
      <div class="stats">${plural(s.pages)}${src && ` in ${sourceLabel(src)}`}</div></section>
      ${sourceChips(m, src, "")}${(m.sources || []).length > 0 && html`<div class="chips"><a class="pill" href="${base}packages/${src ? src.split("/").map(encodeURIComponent).join("/") + "/" : ""}">📦 browse by package</a></div>`}
      <div class="sections">${cards}</div>`);
  }

  // sourceSection shows a section's listing (or a subsection's), from one
  // source or all; start is the chunk to start at, and pageURL(n), if given,
  // the static listing page the address follows as it scrolls.
  /** @param {any} nav @param {string} src @param {string} key @param {string} sub @param {{start?: number, pageURL?: ((n: number) => string) | null}} [opts] */
  async function sourceSection(nav, src, key, sub, { start = 1, pageURL = null } = {}) {
    const m = await manifest();
    const [name, emoji] = sectionName(key);
    const plan = await chapterPlan(m, key, sub, src);
    const label = sub || key;
    nav.setTitle(`${sourceLabel(src)} · ${sectionLabel(label)} · girlpages 💅`);
    const chips = plan.subs.size > 1 && html`<div class="chips">
      <a class="pill${sub ? "" : " on"}" href="${browse({ src, section: key })}">all</a>${[...plan.subs].sort().map(([s, c]) =>
        html`<a class="pill${s === sub ? " on" : ""}" href="${browse({ src, section: key, sub: s })}">${s}${c != null && ` (${c})`}</a>`)}</div>`;
    // The count, where it's known without fetching everything.
    let total = plan.total;
    if (total == null && !sub) {
      const se = (m.sources || []).find(x => sourceKey(x) === src);
      total = se && se.sections ? se.sections[chapter(key)] : null;
    }
    const chunks = Math.max(1, plan.count);
    const first = Math.min(Math.max(1, await plan.chunkForPage(start)), chunks);
    const body = !plan.count || total === 0 ? html`<div class="empty">nothing here, bestie 😭</div>`
      : html`<nav class="letters" aria-label="Jump to letter">${[...plan.letters].map(([l, c]) => html`<a href="#letter-${l}" data-chunk="${c}">${l}</a>`)}</nav>
        <div class="listing"><div class="chunk" data-chunk="${first}">${await chunkMaker(plan)(first)}</div></div>`;
    const count = total == null ? "" : plural(total);
    const heading = isSeries(label) ? `${name} ${emoji}` : `${sectionLabel(label)} · ${name} ${emoji}`;
    nav.render(html`<div class="card"><nav class="crumbs"><a href="${base}">home</a> › ${src && html`<a href="${browse({ src })}">${sourceLabel(src)}</a> › `}${sectionLabel(label)}</nav>
      <div class="pagehead"><h1>${heading}</h1><p class="sub">${count}${src && `${count ? " in " : "in "}${sourceLabel(src)}`}</p>
      ${"caches" in globalThis && html`<p class="save"><button type="button" class="pill" id="save"></button></p>`}</div>
      ${sourceChips(m, src, chapter(key))}${chips}${body}</div>`);
    if (!nav.current()) return;
    const button = app.querySelector("#save");
    if (button) saveButton(button, cache, sub || key, src);
    const listing = app.querySelector(".listing");
    if (listing) {
      await nav.own(new Listing(listing, plan, first, {
        pageURL, page: start, letters: app.querySelector(".letters"), follow: (u) => nav.replace(u),
      })).start();
    }
  }

  // searchView is the search page, in the app.
  async function searchView(nav, params) {
    nav.setTitle("search · girlpages 💅");
    nav.render(html`<div class="card" id="search" data-repo="${app.dataset.repo}" data-base="${base}">
      <div class="crumbs"><a href="${base}">home</a> › search</div>
      <div class="pagehead"><h1>🔎 search</h1><p class="sub" id="search-status">type something up there, bestie: “in quotes” for a phrase, pkg:coreutils for a package’s pages</p>
      ${"caches" in globalThis && html`<p class="save"><button type="button" class="pill" data-save-search="words"></button><button type="button" class="pill" data-save-search="typos"></button><a class="pill" id="search-saved" hidden></a></p>`}</div>
      <nav class="chips modes" id="search-mode" aria-label="How words match"></nav>
      <nav class="chips sources" id="search-sources" aria-label="Search within a source"></nav><ul class="pages" id="search-results"></ul></div>`);
    if (!nav.current()) return;
    for (const b of app.querySelectorAll("button[data-save-search]")) saveButton(b, cache, "search:" + b.dataset.saveSearch);
    nav.reveal();
    await searchPage(app.querySelector("#search"), params, {
      current: () => nav.current(),
      signal: nav.signal,
      // Straight to a page a query names, in place of the search.
      go: (u) => nav.redirect(u),
    });
  }

  function oops(nav, msg) {
    nav.render(html`<div class="card waiting"><div class="spinner">😭</div><h1>${msg}</h1><p><a href="${base}">home</a> · <a href="${base}search/">search</a></p></div>`);
  }

  return { sourceHome, sourceSection, searchView, oops };
}
