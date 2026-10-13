// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The app's views of the repository, other than pages (see pageview.js):
// home (everything, or a source), a section's listing, and search.

import { esc } from "./html.js";
import { chapter, isSeries, sectionName, sectionLabel, sourceLabel, plural } from "./manual.js";
import { saveButton, sourceNav } from "./widgets.js";
import { searchPage } from "./search.js";

// createViews makes the views for an app: ctx has its element (el), the
// site's base, the repository cache and manifest(), link makers (browse,
// listingURL), and its listings.
export function createViews({ el: app, base, cache, manifest, browse, listingURL, listings }) {
  const { chapterPlan, chunkMaker, listInto } = listings;

  function sourceChips(m, on, section) {
    const sources = (m.sources || []).filter(s => !section || (s.sections || {})[section]);
    const href = (key) => (key ? browse({ src: key, section }) : section ? `${base}section/${encodeURIComponent(section)}/` : base);
    return `<nav class="chips sources" aria-label="Browse by source">${sourceNav(sources, { on, href })}</nav>`;
  }

  // everything is the whole repository as a source: the latest version of
  // every page.
  function everything(m) {
    const sections = {};
    for (const e of m.sections || []) sections[chapter(e.section)] = (sections[chapter(e.section)] || 0) + e.pages;
    return { pages: m.pages, sections };
  }

  async function sourceHome(nav, src) {
    const m = await manifest();
    const s = src ? (m.sources || []).find(x => (x.release ? `${x.distro}/${x.release}` : x.distro) === src) : everything(m);
    if (!s) return oops(nav, "there's no source called that here 🤔");
    const keys = Object.keys(s.sections || {}).sort((a, b) => (/\d/.test(a[0]) !== /\d/.test(b[0]) ? (/\d/.test(a[0]) ? -1 : 1) : a < b ? -1 : 1));
    let cards = "";
    for (const k of keys) {
      const [name, emoji] = sectionName(k);
      cards += `<a class="section-card" href="${esc(src ? browse({ src, section: k }) : listingURL(k, "", 1))}"><span class="emoji">${emoji}</span>` +
        `<div class="num">${esc(k)}</div><div class="name">${esc(name)}</div><div class="count">${plural(s.sections[k])}</div></a>`;
    }
    nav.setTitle(src ? `${sourceLabel(src)} · girlpages 💅` : "the manual, but make it girly · girlpages 💅");
    const search = src ? "" : `<form class="hero-search" action="${esc(base)}search/" role="search"><input type="search" name="q" placeholder="what are we reading today? 🔎" aria-label="Search manual pages"></form>`;
    nav.render(`<section class="hero"><h1>✨ girlpages ✨</h1><p>the ${src ? `<strong>${esc(sourceLabel(src))}</strong> ` : ""}manual, but make it girly 💅</p>` +
      search + `<div class="stats">${plural(s.pages)}${src ? " in " + esc(sourceLabel(src)) : ""}</div></section>` +
      sourceChips(m, src, "") + `<div class="sections">${cards}</div>`);
  }

  // sourceSection shows a section's listing (or a subsection's), from one
  // source or all; start is the chunk to start at, and pageURL(n), if given,
  // the static listing page the address follows as it scrolls.
  async function sourceSection(nav, src, key, sub, { start = 1, pageURL = null } = {}) {
    const m = await manifest();
    const [name, emoji] = sectionName(key);
    const plan = await chapterPlan(m, key, sub, src);
    const label = sub || key;
    nav.setTitle(`${sourceLabel(src)} · ${sectionLabel(label)} · girlpages 💅`);
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
    const chunkHTML = chunkMaker(plan);
    const chunks = Math.max(1, plan.count);
    const first = Math.min(Math.max(1, await plan.chunkForPage(start)), chunks);
    let body = `<nav class="letters" aria-label="Jump to letter">${[...plan.letters].map(([l, c]) => `<a href="#letter-${esc(l)}" data-chunk="${c}">${esc(l)}</a>`).join("")}</nav>` +
      `<div class="listing"><div class="chunk" data-chunk="${first}">${await chunkHTML(first)}</div></div>`;
    if (!plan.count || total === 0) body = '<div class="empty">nothing here, bestie 😭</div>';
    const crumbs = `<a href="${esc(base)}">home</a> › ` + (src ? `<a href="${esc(browse({ src }))}">${esc(sourceLabel(src))}</a> › ` : "");
    const save = "caches" in globalThis ? '<p class="save"><button type="button" class="pill" id="save"></button></p>' : "";
    const count = total == null ? "" : plural(total);
    const heading = isSeries(label) ? `${name} ${emoji}` : `${sectionLabel(label)} · ${name} ${emoji}`;
    nav.render(`<div class="card"><nav class="crumbs">${crumbs}${esc(sectionLabel(label))}</nav>` +
      `<div class="pagehead"><h1>${esc(heading)}</h1><p class="sub">${count}${src ? (count ? " in " : "in ") + esc(sourceLabel(src)) : ""}</p>${save}</div>` +
      sourceChips(m, src, chapter(key)) + chips + body + "</div>");
    if (!nav.current()) return;
    const button = app.querySelector("#save");
    if (button) saveButton(button, cache, sub || key, src);
    const listing = app.querySelector(".listing");
    if (listing) {
      nav.own(await listInto(listing, plan, first, {
        pageURL, page: start, letters: app.querySelector(".letters"), follow: (u) => nav.replace(u),
      }));
    }
  }

  // searchView is the search page, in the app.
  async function searchView(nav, params) {
    nav.setTitle("search · girlpages 💅");
    const save = "caches" in globalThis ? '<p class="save"><button type="button" class="pill" data-save-search="words"></button>' +
      '<button type="button" class="pill" data-save-search="typos"></button><a class="pill" id="search-saved" hidden></a></p>' : "";
    nav.render(`<div class="card" id="search" data-repo="${esc(app.dataset.repo)}" data-base="${esc(base)}"><div class="crumbs"><a href="${esc(base)}">home</a> › search</div>` +
      `<div class="pagehead"><h1>🔎 search</h1><p class="sub" id="search-status">type something up there, bestie</p>${save}</div>` +
      '<nav class="chips sources" id="search-sources" aria-label="Search within a source"></nav><ul class="pages" id="search-results"></ul></div>');
    if (!nav.current()) return;
    for (const b of app.querySelectorAll("button[data-save-search]")) saveButton(b, cache, "search:" + b.dataset.saveSearch);
    nav.reveal();
    await searchPage(app.querySelector("#search"), params, {
      current: () => nav.current(),
      // Straight to a page a query names, in place of the search.
      go: (u) => nav.redirect(u),
    });
  }

  function oops(nav, msg) {
    nav.render(`<div class="card waiting"><div class="spinner">😭</div><h1>${esc(msg)}</h1><p><a href="${esc(base)}">home</a> · <a href="${esc(base)}search/">search</a></p></div>`);
  }

  return { sourceHome, sourceSection, searchView, oops };
}
