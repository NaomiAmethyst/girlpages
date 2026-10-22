// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The browse app: everything the static site doesn't render itself, read
// straight from the repository, in the home page's hash (see browseURL in
// urls.js):
//
//   #/debian/trixie                 a source's home
//   #/section/3, #/section/3/3ssl   a section's listing, or a subsection's
//   #/debian/section/1              …from one source
//   #/1/ls                          a page (its latest version)
//   #/debian/trixie/1/ls            the version a source has, with links that stay in it
//   #/1/ls/9.4                      another version: an upstream version, or a source hash's start
//   #/1/ls/man                      another variant: man is the original; girl is the default
//   #/1/ls/9.4/man/mono             the original as man prints it, not reflowed
//   …/versions                      scrolled to the list of versions
//
// Once it's running it shows the static site's pages too, in place, and
// it's where static pages go when offline. /browse/?page=… (from earlier
// versions) redirects here. Which view shows when is router.js's; the
// views are views.js's and pageview.js's.

import { Router } from "./router.js";
import { repoCache, errorKind } from "./cache.js";
import { namePath, listingPath, variantPath } from "./format.js";
import { pageURL, listingURL as listingAt, browseURL, setVoice } from "./urls.js";
import { viewOf, parse, legacyRoute } from "./routes.js";
import { createListings } from "./listing.js";
import { createViews } from "./views.js";
import { createPageView } from "./pageview.js";

// createApp makes the app, showing its views in el (#app), whose data
// attributes say where the site and repository are.
function createApp(el) {
  const base = el.dataset.base;
  const cache = repoCache(el.dataset.repo ?? "");
  const manifest = () => cache.get("manifest.json");
  const listing = async (name, sec) => cache.get(listingPath((await cache.get(namePath(name, sec))).sha256));
  // A variant is named after its own hash (the listing gives it), so it's
  // cached forever.
  const variant = (source, which, sha) => cache.get(variantPath(source, which, sha));
  const staticPage = (name, sec) => pageURL(base, name, sec);
  const browse = (params) => browseURL(base, params);
  const listingURL = (key, sub, n) => listingAt(base, key, sub, n);

  /** @type {Router} */
  let router;
  const listings = createListings({ cache, manifest, browse, staticPage, router: { followStatic: (from, to) => router.followStatic(from, to) } });
  const views = createViews({ el, base, cache, manifest, browse, listingURL, listings });
  const { sourceHome, sourceSection, searchView, oops } = views;
  const { loadPage, pageView } = createPageView({ el, base, listing, findName: listings.findName, variant, browse, staticPage, listingURL, oops });

  // route shows the view for nav's address.
  async function route(nav) {
    const v = viewOf(new URL(nav.url), base);
    setVoice(v ? v.voice : "");
    if (v) {
      switch (v.kind) {
        case "home": return sourceHome(nav, "");
        case "page": return pageView(nav, v.name, v.sec, [], "", v.voice);
        case "search": return searchView(nav, v.params);
        case "section": return sourceSection(nav, "", v.key, v.sub, { start: v.page, pageURL: (n) => listingURL(v.key, v.sub, n) });
      }
    }
    const r = await parse(new URL(nav.url).hash, async () => (await manifest()).sources);
    switch (r.kind) {
      case "source": return sourceHome(nav, r.src);
      case "section": return sourceSection(nav, r.src, r.key, r.sub);
      case "page": return pageView(nav, r.name, r.sec, r.rest, r.src);
    }
    return oops(nav, r.kind === "bad" ? "there's nothing here 🤔" : "that link's broken 🤔");
  }

  // The router shows views (see router.js). Files that change are shown
  // from the cache while they're refreshed, so if one turns out to be
  // stale enough to point at something since removed, it's refreshed and
  // the view shown again.
  router = new Router({
    app: el,
    resolve: async (nav) => {
      try {
        await route(nav);
      } catch (e) {
        if (errorKind(e) === "missing" && nav.current() && await cache.revalidate()) return route(nav);
        throw e;
      }
    },
    handles: (url) => !!viewOf(new URL(url), base),
    onError: (nav, e) => oops(nav, errorKind(e) === "offline" ? "you're offline, and this isn't saved yet 📵" : `something broke 💔 ${e.message}`),
    waiting: '<div class="card waiting"><div class="spinner">💅</div><p>getting it ready, bestie…</p></div>',
  });

  // Links within a view (contents, letters) scroll rather than route.
  el.addEventListener("click", (e) => {
    const a = e.target.closest("a[href^='#']");
    if (!a || a.getAttribute("href").startsWith("#/")) return;
    e.preventDefault();
    // (The page's own view, hidden, may have the same ids.)
    el.querySelector(`[id="${CSS.escape(decodeURIComponent(a.getAttribute("href").slice(1)))}"]`)?.scrollIntoView();
  });

  return {
    base, router, listings,
    // prefetch fetches what a view will need, so following a link to it
    // is quick.
    async prefetch(href) {
      const v = viewOf(new URL(href, location.href), base);
      if (v && v.kind === "page") await loadPage(v.name, v.sec, [], "");
      if (!v || v.kind !== "route") return;
      const r = await parse(v.hash, async () => (await manifest()).sources);
      if (r.kind === "page") await loadPage(r.name, r.sec, r.rest, r.src);
    },
    handles: (href) => !!viewOf(new URL(href, location.href), base),
  };
}

// The app's element: #app, which every page has.
const appElement = () => /** @type {HTMLElement} */ (document.getElementById("app"));

// The app is made the first time it's wanted, in the page it's in.
/** @type {ReturnType<typeof createApp> | null} */
let theApp = null;
function app() {
  if (!theApp) theApp = createApp(appElement());
  return theApp;
}

// moved reports whether this is /browse/ (from earlier versions), and if
// so, goes where it went.
function moved() {
  const base = appElement().dataset.base;
  if (location.pathname !== base + "browse/") return false;
  location.replace(location.search ? legacyRoute(new URLSearchParams(location.search), base) : base + location.hash);
  return true;
}

// start shows the address, and from then on follows it.
export function start() {
  return moved() || app().router.start();
}

// takeOver shows the page loaded with the app, in place of its own view.
export function takeOver() {
  if (moved()) return moved();
  const r = app().router;
  r.takeOver = true;
  return r.start();
}

// navigate goes to an address (a link followed), in a new history entry.
export function navigate(href) {
  return moved() || app().router.navigate(href);
}

// prefetch fetches what showing an address will need.
export const prefetch = (href) => app().prefetch(href);

// handles reports whether the app shows an address itself.
export const handles = (href) => app().handles(href);

// attachListing takes over a static listing page (see listing.js).
export const attachListing = (listing, opts) => app().listings.attachListing(listing, opts);
