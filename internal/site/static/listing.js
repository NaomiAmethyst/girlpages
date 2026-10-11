// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// A static listing's pages, scrolled through as one: the pages before and
// after this one load as they're neared (taken from their own static
// HTML), the address follows the page on screen, and letters jump within
// the list. Without JavaScript, the pager links them.

import { VirtualList } from "./vlist.js";

const listing = document.querySelector(".listing[data-pages]");
if (listing && "IntersectionObserver" in globalThis && +listing.dataset.pages > 1) {
  const first = listing.dataset.first;
  const url = (n) => (n <= 1 ? first : `${first}page/${n}/`);
  const pageOf = (href) => {
    const m = new URL(href, location.href).pathname.match(/\/page\/(\d+)\/$/);
    return m ? +m[1] : 1;
  };
  const load = async (n) => {
    const r = await fetch(url(n));
    if (!r.ok) throw new Error(`page ${n}: ${r.status}`);
    const doc = new DOMParser().parseFromString(await r.text(), "text/html");
    const c = doc.querySelector(".listing .chunk");
    if (!c) throw new Error(`page ${n} has no listing`);
    return c.innerHTML;
  };
  const list = new VirtualList(listing, {
    count: +listing.dataset.pages,
    load,
    onChunk: (n) => history.replaceState(history.state, "", url(n)),
  });
  document.querySelector(".pager")?.setAttribute("hidden", "");
  document.querySelector(".letters")?.addEventListener("click", (e) => {
    const a = e.target.closest("a[href]");
    if (!a) return;
    e.preventDefault();
    const id = decodeURIComponent(new URL(a.href).hash.slice(1));
    list.jump(pageOf(a.href), id).catch(() => { location.href = a.href; });
  });
  list.fill();
}
