// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages in the browser: what every page runs. It registers the service
// worker (sw.js), which makes the site an app; keeps an eye out for newly
// published repositories; wires up save buttons and source menus; and
// starts the browse app (app.js), which, once it's loaded, shows pages
// in place as links are followed.

import { repoCache, idle } from "./cache.js";
import { saveButton, menus } from "./widgets.js";
import { isRoute } from "./routes.js";

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages in the browser: a cache of repository files (in the Cache API,
// so it can hold whole sections saved for offline reading), a background
// check for newly published repositories that clears out the search index
// files they no longer use, and the service worker that makes the site an
// app. Offline, the service worker sends static pages to the browse app,
// which renders them from this cache.

const PREFIX = "girlpages:";

// forgetLocalStorage removes the files earlier versions kept in
// localStorage.
function forgetLocalStorage() {
  try {
    const s = globalThis.localStorage;
    for (let i = s.length - 1; i >= 0; i--) {
      const k = s.key(i);
      if (k && k.startsWith(PREFIX + "file:")) s.removeItem(k);
    }
  } catch {
    // nothing to forget
  }
}

// startApp lets any page become the browse app (static/app.js) without
// loading another. Following a link to a route (#/…) shows it in place,
// and once the app has loaded (in idle time), so does following any link
// within the site, or searching: from then on the static pages are only
// what the first visit, search engines, and browsers without JavaScript
// see. History leads back. A link's files are fetched as soon as it's
// pointed at or touched.
function startApp(base) {
  let loading = null, app = null;
  const load = () => (loading ||= import(new URL(base + "static/app.js", location.href).href).then(m => {
    app = m;
    // Its links are followed in place now; prerendering them is waste.
    for (const s of document.querySelectorAll('script[type="speculationrules"]')) s.remove();
    return m;
  }));
  const open = () => load().then(m => m.start());
  // Following a link: the app's router takes it from here (see router.js).
  // If the app can't load, the link's followed as a plain one.
  const go = (href) => load().then(m => m.navigate(href), () => { location.href = href; });
  // A link the app shows, from an event on it, or null: routes always,
  // anything else once the app has loaded (but not links within a page).
  const appLink = (e) => {
    const a = e.target.closest?.("a[href]");
    if (!a || a.target || a.hasAttribute("download")) return null;
    const u = new URL(a.href, location.href);
    if (u.origin !== location.origin) return null;
    if (u.pathname === base && isRoute(u.hash)) return u;
    if (u.hash && u.pathname === location.pathname && u.search === location.search) return null;
    return app && app.handles(u.href) ? u : null;
  };
  if (location.pathname === base || location.pathname === base + "browse/") open();
  document.addEventListener("click", (e) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const u = appLink(e);
    if (!u) return;
    e.preventDefault();
    go(u.href);
  });
  document.addEventListener("submit", (e) => {
    const f = e.target;
    if (!app || e.defaultPrevented || f.method.toLowerCase() !== "get") return;
    const u = new URL(f.action, location.href);
    u.search = new URLSearchParams(new FormData(f)).toString();
    if (!app.handles(u.href)) return;
    e.preventDefault();
    f.querySelector("input")?.blur();
    go(u.href);
  });
  const warmed = new Set();
  const warm = (e) => {
    const u = appLink(e);
    if (!u || warmed.has(u.href)) return;
    warmed.add(u.href);
    load().then(m => m.prefetch(u.href)).catch(() => {});
  };
  for (const ev of ["pointerover", "touchstart", "focusin"]) document.addEventListener(ev, warm, { passive: true });
  idle(() => load().catch(() => {}));
}

// boot runs on every page: it registers the service worker, starts
// watching the repository for updates, and wires up save buttons.
function boot() {
  const root = document.documentElement;
  const base = root.dataset.base, repo = root.dataset.repo;
  if (!base) return;
  if ("serviceWorker" in navigator && location.protocol !== "file:") {
    navigator.serviceWorker.register(base + "sw.js", { scope: base }).catch(() => {});
  }
  forgetLocalStorage();
  menus();
  if (!repo) return;
  startApp(base);
  const cache = repoCache(repo);
  cache.watch();
  // A static page read (or listing seen) is kept, so the browse app can
  // show it offline.
  const page = document.querySelector("[data-keep-page]");
  if (page) idle(() => cache.keepPage(page.dataset.keepPage, page.dataset.keepSection).catch(() => {}));
  if (typeof globalThis.caches !== "undefined") {
    for (const b of document.querySelectorAll("button[data-save-section], button[data-save-search]")) {
      b.hidden = false;
      saveButton(b, cache, b.dataset.saveSection || "search:" + b.dataset.saveSearch);
    }
  }
}

if (typeof document !== "undefined") boot();
