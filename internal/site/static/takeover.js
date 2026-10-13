// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// A static listing page, on its first load, taken over by the browse app:
// it's made again from its chapter's index, from this page's
// first entry, and scrolls through the whole section, the address
// following the page on screen. Without JavaScript, the pager links the
// static pages.

const listing = /** @type {HTMLElement | null} */ (document.querySelector(".listing[data-key]"));
if (listing && "IntersectionObserver" in globalThis && Number(listing.dataset.pages) > 1) {
  const base = document.documentElement.dataset.base;
  const first = listing.dataset.first;
  const url = (n) => (n <= 1 ? first : `${first}page/${n}/`);
  import(new URL(base + "static/app.js", location.href).href).then(async (app) => {
    const list = await app.attachListing(listing, {
      key: listing.dataset.key, sub: listing.dataset.sub, pageURL: url,
      letters: document.querySelector(".letters"),
    });
    if (list) document.querySelector(".pager")?.setAttribute("hidden", "");
  }).catch(() => {
    // Offline, or the repository's unreachable: the pager still works.
  });
}
