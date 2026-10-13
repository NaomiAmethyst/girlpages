// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// Listings: a chapter's pages (or a section's, or a source's) from the
// chapter's index, a chunk at a time, scrolling through all of them.

import { VirtualList } from "./vlist.js";
import { html } from "./html.js";
import { rows, chapterShardsOf } from "./format.js";
import { chapter, isSeries, letterOf, nameCmp } from "./manual.js";

// PAGE is how many pages each of the static listing's pages shows.
export const PAGE = 200;

// createListings makes the listing functions for an app: ctx has the
// repository cache, manifest(), browse(params) and staticPage(name, sec)
// for links, and the router.
export function createListings({ cache, manifest, browse, staticPage, router }) {
  // chapterPlan works out a listing of a chapter (key), or of one of its
  // sections (sub), from one source (src) or all, from the chapter's index:
  // its chunks are the index's, {count, load(n) resolving to a chunk's
  // items, letters (letter → the chunk it starts in), total (if it's known),
  // subs (section → count, if known), prevLetter(n), startOf(n) (where in
  // the listing chunk n starts, if it's known), chunkOf(name),
  // chunkForPage(p) (the chunk static listing page p starts in), order(n)
  // (the order to load chunks in, from n)}. Listing just some of the
  // chapter (a section, or a source's pages), chunks hold the ones wanted,
  // which may be none, and where they start is known once all before them
  // are loaded.
  async function chapterPlan(m, key, sub, src) {
    const e = (m.chapters || []).find(c => c.chapter === chapter(key));
    const secs = (m.sections || []).filter(x => chapter(x.section) === chapter(key));
    const subs = new Map(secs.map(x => [x.section, src ? null : x.pages]));
    if (!e) {
      return { count: 0, total: 0, subs, letters: new Map(), load: async () => [], prevLetter: () => "", startOf: () => 0,
        chunkOf: () => 1, chunkForPage: async () => 1, order: () => [] };
    }
    const ci = await cache.chapterIndex(e);
    const shards = chapterShardsOf(ci);
    const count = 1 + shards.length;
    const sizes = [ci.count - shards.reduce((n, r) => n + r.count, 0), ...shards.map(r => r.count)];
    const filtered = !!(sub || src);
    const keep = (p) => (!sub || p.section === sub) && (!src || p.sources.includes(src));
    const toItem = (p) => ({
      name: p.name, sec: p.section, desc: p.description || "", girl: src ? p.girl.includes(src) : p.variants.includes("girl"),
      url: src ? browse({ page: p.name, sec: p.section, src }) : staticPage(p.name, p.section),
    });
    // For a filtered listing: how many of each chunk's pages are wanted, and
    // the last one's letter, once it's loaded.
    const wanted = new Map(), lastLetter = new Map();
    const load = async (n) => {
      const items = (await cache.chapterChunk(ci, n)).filter(keep).map(toItem);
      wanted.set(n, items.length);
      lastLetter.set(n, items.length ? letterOf(items[items.length - 1].name) : "");
      return items;
    };
    const startOf = (n) => {
      let at = 0;
      for (let k = 1; k < n; k++) {
        if (!filtered) at += sizes[k - 1];
        else if (wanted.has(k)) at += wanted.get(k);
        else return null;
      }
      return at;
    };
    const prevLetter = (n) => {
      if (!filtered) return n > 1 ? shards[n - 2].prev || "" : "";
      for (let k = n - 1; k >= 1; k--) {
        if (!lastLetter.has(k)) return "";
        if (lastLetter.get(k)) return lastLetter.get(k);
      }
      return "";
    };
    const letters = new Map(rows(ci.letters).map(l => [l.letter, l.chunk]));
    let total = filtered ? null : ci.count;
    if (sub && !src) total = (secs.find(x => x.section === sub) || {}).pages || 0;
    return {
      count, total, subs, letters, load, prevLetter, startOf,
      chunkOf: (name) => {
        const low = name.toLowerCase();
        let n = 1;
        shards.forEach((r, i) => { if (nameCmp(r.first.toLowerCase(), low) <= 0) n = i + 2; });
        return n;
      },
      chunkForPage: async (p) => {
        const at = (p - 1) * PAGE;
        for (let n = 1, from = 0; n <= count; n++) {
          const size = filtered ? (wanted.has(n) ? wanted.get(n) : (await load(n)).length) : sizes[n - 1];
          if (at < from + size) return n;
          from += size;
        }
        return 1;
      },
      order: (from) => [...new Set([from, ...letters.values(), ...Array.from({ length: count }, (_, i) => i + 1)])],
    };
  }

  // chunkMaker renders a plan's chunks: its items, under letters (or
  // thousands) as they start, a letter carried over from the chunk before
  // being marked as such.
  function chunkMaker(plan) {
    const entry = (it) => {
      const series = isSeries(it.sec);
      return html`<li data-name="${it.name}"><a href="${it.url}"><span class="name">${series ? `${it.sec.toUpperCase()} ${it.name}` : it.name}</span>${
        !series && html`<span class="sec">(${it.sec})</span>`}</a><span class="desc">${it.desc}</span>${
        it.girl && html`<span class="done" title="girlified">✨</span>`}</li>`;
    };
    return async (n) => {
      // Runs of a letter, each under its heading: marked as carried on if
      // the chunk before ended with it.
      const runs = [];
      for (const it of await plan.load(n)) {
        const l = letterOf(it.name);
        if (!runs.length || runs[runs.length - 1].letter !== l) runs.push({ letter: l, items: [] });
        runs[runs.length - 1].items.push(it);
      }
      const prev = plan.prevLetter(n);
      return html`${runs.map(({ letter, items }, i) => html`${i === 0 && letter === prev
        ? html`<h2 class="letter cont">${letter} <span class="sub">(continued)</span></h2>`
        : html`<h2 class="letter" id="letter-${letter}">${letter}</h2>`}<ul class="pages">${items.map(entry)}</ul>`)}`;
    };
  }

  // listInto makes a listing element, holding chunk first, scroll through
  // the rest of a plan: loading chunks as they're neared, and all of them
  // meanwhile (in plan.order: letters' first), following along with the
  // address if pageURL is given (pageURL(n) is a static listing page's, of
  // PAGE pages each; page is the one it's at) by follow(url), which changes
  // the address if it's still the listing's to change, and jumping by its
  // letters.
  async function listInto(listing, plan, first, { pageURL = null, page = 1, letters = null, follow = () => false } = {}) {
    const chunks = Math.max(1, plan.count);
    const chunkHTML = chunkMaker(plan);
    if (chunks < 2 || !("IntersectionObserver" in globalThis)) {
      for (let n = first + 1; n <= chunks; n++) listing.insertAdjacentHTML("beforeend", html`<div class="chunk" data-chunk="${n}">${await chunkHTML(n)}</div>`);
      return null;
    }
    // The static page the entry at the top of the screen is on, if it's
    // known where in the listing its chunk starts.
    const topPage = (c) => {
      const start = plan.startOf(+c.dataset.chunk);
      const lis = c.querySelectorAll("li[data-name]");
      if (start == null || !lis.length) return 0;
      let lo = 0, hi = lis.length - 1;
      while (lo < hi) {
        const mid = (lo + hi) >> 1;
        if (lis[mid].getBoundingClientRect().bottom > 80) hi = mid;
        else lo = mid + 1;
      }
      return Math.floor((start + lo) / PAGE) + 1;
    };
    // Where the list starts is where the address already is, even if a
    // page before it shows above (a static page taken over keeps its first
    // entry where it was): the address follows once that changes.
    let from = 0;
    const list = new VirtualList(listing, {
      count: chunks, load: chunkHTML,
      onTop: pageURL ? (c) => {
        const p = topPage(c);
        if (p && !from) from = p;
        if (!p || p === from || p === page) return;
        if (!follow(pageURL(p))) return;
        from = -1;
        page = p;
      } : undefined,
    });
    letters?.addEventListener("click", (e) => {
      const a = e.target.closest("a[href]");
      if (!a) return;
      const l = a.textContent.trim();
      if (!plan.letters.has(l)) return;
      e.preventDefault();
      e.stopPropagation();
      list.jump(plan.letters.get(l), "letter-" + l);
    });
    list.fill();
    // The rest, a couple at a time, until the list goes or the network does.
    const todo = plan.order(first);
    let stop = false;
    const worker = async () => {
      while (todo.length && !stop && !list.dead) {
        try {
          await plan.load(todo.shift());
        } catch (e) {
          if (e instanceof TypeError) stop = true;
        }
      }
    };
    (globalThis.requestIdleCallback || ((f) => setTimeout(f, 200)))(() => Promise.all([worker(), worker()]));
    return list;
  }

  // attachListing takes over a static listing page (on its first load) with
  // one made from the repository's section indexes, which then scrolls
  // through the whole section: starting where the static page does, with
  // its first entry kept where it is on the screen.
  async function attachListing(listing, { key, sub = "", pageURL, letters = null }) {
    const m = await manifest();
    const plan = await chapterPlan(m, key, sub, "");
    const firstLi = listing.querySelector("li[data-name]");
    if (!firstLi || !plan.count) return null;
    const name = firstLi.dataset.name;
    const n = plan.chunkOf(name);
    const markup = await chunkMaker(plan)(n);
    const y = firstLi.getBoundingClientRect().top;
    listing.innerHTML = html`<div class="chunk" data-chunk="${n}">${markup}</div>`;
    // The static view's address, while history's at it and the app isn't
    // showing anything over it.
    let at = location.href;
    const follow = (u) => {
      if (!router.followStatic(at, u)) return false;
      at = location.href;
      return true;
    };
    const list = await listInto(listing, plan, n, { pageURL, page: +listing.dataset.page || 1, letters, follow });
    // Keep the entry that was first on the page where it was: again once
    // the entries around it are laid out, as the sizes of those that aren't
    // yet are guesses (see style.css), until it stays put.
    for (let i = 0; i < 5; i++) {
      const again = [...listing.querySelectorAll("li[data-name]")].find(li => li.dataset.name === name);
      const d = again ? again.getBoundingClientRect().top - y : 0;
      if (Math.abs(d) < 1) break;
      scrollBy(0, d);
      await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)));
    }
    return list;
  }

  return { chapterPlan, chunkMaker, listInto, attachListing };
}
