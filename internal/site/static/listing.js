// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// Listings: a chapter's pages (or a section's, or a source's) from the
// chapter's index, a chunk at a time, scrolling through all of them.

import { VirtualList } from "./vlist.js";
import { idle } from "./cache.js";
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
  // attachListing takes over a static listing page (on its first load) with
  // a Listing of its whole chapter (or section), starting where the static
  // page does, with its first entry kept where it is on the screen. The
  // address follows it while the static view is what's shown.
  async function attachListing(el, { key, sub = "", pageURL, letters = null }) {
    const m = await manifest();
    const plan = await chapterPlan(m, key, sub, "");
    const firstLi = el.querySelector("li[data-name]");
    if (!firstLi || !plan.count) return null;
    const name = firstLi.dataset.name;
    const n = plan.chunkOf(name);
    const markup = await chunkMaker(plan)(n);
    const y = firstLi.getBoundingClientRect().top;
    el.innerHTML = html`<div class="chunk" data-chunk="${n}">${markup}</div>`;
    let at = location.href;
    const follow = (u) => {
      if (!router.followStatic(at, u)) return false;
      at = location.href;
      return true;
    };
    const listing = new Listing(el, plan, n, { pageURL, page: +el.dataset.page || 1, letters, follow });
    await listing.start();
    await listing.keep(name, y);
    return listing;
  }

  return { chapterPlan, attachListing };
}

// chunkMaker renders a plan's chunks: its items, under letters (or
// thousands) as they start, a letter carried over from the chunk before
// being marked as such.
export function chunkMaker(plan) {
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

// frames waits for the browser to lay out and paint.
const frames = () => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)));

// A Listing scrolls through a plan in a listing element (.listing) that
// holds chunk first: loading chunks as they're neared (VirtualList), and
// all of them meanwhile, in plan.order (letters' first chunks, then the
// rest), until it's destroyed or the network goes. If pageURL is given
// (pageURL(n) being a static listing page's, of PAGE pages each; page the
// one it's at), the address follows the page at the top of the screen, by
// follow(url), which changes it if it's still the listing's to change. Its
// letters (a nav of links) jump to theirs.
export class Listing {
  constructor(el, plan, first, { pageURL = null, page = 1, letters = null, follow = () => false } = {}) {
    Object.assign(this, { el, plan, first, pageURL, page, letters, follow });
    this.chunkHTML = chunkMaker(plan);
    this.list = null;
    this.dead = false;
    // Where the list starts is where the address already is, even if a
    // page before it shows above (a static page taken over keeps its
    // first entry where it was): the address follows once that changes.
    this.from = 0;
    this.onLetter = (e) => {
      const a = e.target.closest("a[href]");
      const l = a && a.textContent.trim();
      if (!l || !this.plan.letters.has(l) || !this.list) return;
      e.preventDefault();
      e.stopPropagation();
      this.list.jump(this.plan.letters.get(l), "letter-" + l);
    };
  }

  // start sets it going.
  async start() {
    const chunks = Math.max(1, this.plan.count);
    if (chunks < 2 || !("IntersectionObserver" in globalThis)) {
      // All of it at once: short, or there's no watching the screen.
      for (let n = this.first + 1; n <= chunks && !this.dead; n++) {
        this.el.insertAdjacentHTML("beforeend", html`<div class="chunk" data-chunk="${n}">${await this.chunkHTML(n)}</div>`);
      }
      return this;
    }
    if (this.dead) return this;
    this.list = new VirtualList(this.el, {
      count: chunks, load: this.chunkHTML,
      onTop: this.pageURL ? (c) => this.track(c) : undefined,
    });
    this.letters?.addEventListener("click", this.onLetter);
    this.list.fill();
    idle(() => this.loadAll());
    return this;
  }

  // topPage is the static page the entry at the top of the screen is on,
  // in chunk element c, if it's known where in the listing c starts (or 0).
  topPage(c) {
    const start = this.plan.startOf(+c.dataset.chunk);
    const lis = c.querySelectorAll("li[data-name]");
    if (start == null || !lis.length) return 0;
    let lo = 0, hi = lis.length - 1;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (lis[mid].getBoundingClientRect().bottom > 80) hi = mid;
      else lo = mid + 1;
    }
    return Math.floor((start + lo) / PAGE) + 1;
  }

  // track has the address follow the page at the top of the screen.
  track(c) {
    const p = this.topPage(c);
    if (p && !this.from) this.from = p;
    if (!p || p === this.from || p === this.page) return;
    if (!this.follow(this.pageURL(p))) return;
    this.from = -1;
    this.page = p;
  }

  // loadAll fetches every chunk, a couple at a time.
  async loadAll() {
    const todo = this.plan.order(this.first);
    let offline = false;
    const worker = async () => {
      while (todo.length && !offline && !this.dead) {
        try {
          await this.plan.load(todo.shift());
        } catch (e) {
          if (e instanceof TypeError) offline = true;
        }
      }
    };
    await Promise.all([worker(), worker()]);
  }

  // keep puts the entry called name at y on the screen: again once the
  // entries around it are laid out, as the sizes of those that aren't yet
  // are guesses (see style.css), until it stays put.
  async keep(name, y) {
    for (let i = 0; i < 5 && !this.dead; i++) {
      const li = [...this.el.querySelectorAll("li[data-name]")].find(x => x.dataset.name === name);
      const d = li ? li.getBoundingClientRect().top - y : 0;
      if (Math.abs(d) < 1) break;
      scrollBy(0, d);
      await frames();
    }
  }

  // destroy stops everything it set going.
  destroy() {
    this.dead = true;
    this.list?.destroy();
    this.letters?.removeEventListener("click", this.onLetter);
  }
}
