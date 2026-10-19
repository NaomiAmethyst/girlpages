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
import { Drawer } from "./draw.js";
import { Spy } from "./spy.js";
import { keys } from "./keys.js";
import { outdated, watchUpdates } from "./update.js";

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
// pointed at or touched. It returns how it follows a link. Once a newer
// version of the site has taken over (update.js), links are followed as
// plain ones.
function startApp(base) {
  /** @type {Promise<any> | null} */
  let loading = null;
  /** @type {any} */
  let app = null;
  const load = () => (loading ||= import(new URL(base + "static/app.js", location.href).href).then(m => {
    app = m;
    // Its links are followed in place now; prerendering them is waste.
    for (const s of document.querySelectorAll('script[type="speculationrules"]')) s.remove();
    return m;
  }));
  const open = () => load().then(m => m.start());
  // Following a link: the app's router takes it from here (see router.js).
  // If the app can't load, the link's followed as a plain one.
  const go = (href) => outdated() ? void (location.href = href) : load().then(m => m.navigate(href), () => { location.href = href; });
  // A link the app shows, from an event on it, or null: routes always,
  // anything else once the app has loaded (but not links within a page).
  const appLink = (e) => {
    const a = e.target.closest?.("a[href]");
    if (!a || outdated() || a.target || a.hasAttribute("download")) return null;
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
    const f = /** @type {HTMLFormElement} */ (e.target);
    if (!app || outdated() || e.defaultPrevented || f.method.toLowerCase() !== "get") return;
    const u = new URL(f.action, location.href);
    u.search = new URLSearchParams(/** @type {any} */ (new FormData(f))).toString();
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
  return go;
}

// behindUs marks a page loaded whole from a link on another of the site's
// as having one of the site's behind it in history, for going back to it
// (keys.js); the app's own entries say how many (router.js).
/** @param {string} base */
function behindUs(base) {
  if (history.state?.depth != null || !document.referrer) return;
  const nav = /** @type {PerformanceNavigationTiming | undefined} */ (performance.getEntriesByType("navigation")[0]);
  if (nav && nav.type !== "navigate") return;
  const from = new URL(document.referrer);
  if (from.origin !== location.origin || !from.pathname.startsWith(base)) return;
  history.replaceState({ ...(history.state || {}), depth: 1 }, "");
}

// boot runs on every page: it registers the service worker, starts
// watching the repository for updates, and wires up save buttons.
function boot() {
  const root = document.documentElement;
  const base = root.dataset.base, repo = root.dataset.repo;
  if (!base) return;
  if ("serviceWorker" in navigator && location.protocol !== "file:") {
    watchUpdates();
    navigator.serviceWorker.register(base + "sw.js", { scope: base }).catch(() => {});
  }
  forgetLocalStorage();
  behindUs(base);
  menus();
  // A long page is drawn ahead of scrolling (see draw.js).
  new Drawer().watch(document.querySelectorAll(".content > *, .original > *"));
  // Its contents follow the reading (see spy.js).
  const toc = /** @type {HTMLElement | null} */ (document.querySelector(".static-view .toc"));
  if (toc) new Spy(toc).watch();
  if (!repo) {
    keys({ go: (href) => { location.href = href; }, base, repo: "" });
    return;
  }
  keys({ go: startApp(base), base, repo });
  const cache = repoCache(repo);
  cache.watch();
  // A static page read (or listing seen) is kept, so the browse app can
  // show it offline.
  const page = /** @type {HTMLElement | null} */ (document.querySelector("[data-keep-page]"));
  if (page) idle(() => cache.keepPage(page.dataset.keepPage, page.dataset.keepSection).catch(() => {}));
  if (typeof globalThis.caches !== "undefined") {
    for (const b of /** @type {NodeListOf<HTMLButtonElement>} */ (document.querySelectorAll("button[data-save-section], button[data-save-search]"))) {
      b.hidden = false;
      saveButton(b, cache, b.dataset.saveSection || "search:" + b.dataset.saveSearch);
    }
  }
}

if (typeof document !== "undefined") boot();
