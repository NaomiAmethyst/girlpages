// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages service worker: keeps the app shell (the scripts, styles, and
// the home, search and offline pages) for offline use. Repository files
// aren't touched here; the pages cache those themselves
// (static/cache.js). Static pages aren't kept either: offline, they (and
// pages in other variants, and listings) get the app's shell, which shows
// the same thing from the cached repository.

const BASE = /*BASE*/"/";
const VERSION = /*VERSION*/"dev";
const SHELL = /*SHELL*/[];
const SHELL_CACHE = "girlpages-shell-" + VERSION;
const NETWORK_TIMEOUT = 4000;
// Pages of the shell, served from it first.
const APP_PAGES = ["browse/", "search/", "offline/"].map(p => BASE + p);

self.addEventListener("install", (event) => {
  event.waitUntil((async () => {
    const c = await caches.open(SHELL_CACHE);
    await c.addAll(SHELL.map(p => new Request(BASE + p, { cache: "reload" })));
    await self.skipWaiting();
  })());
});

self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    for (const name of await caches.keys()) {
      // girlpages-pages held static pages read, in earlier versions.
      if ((name.startsWith("girlpages-shell-") && name !== SHELL_CACHE) || name === "girlpages-pages") await caches.delete(name);
    }
    await self.clients.claim();
  })());
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== location.origin || !url.pathname.startsWith(BASE)) return;
  if (url.pathname.startsWith(BASE + "repo/")) return; // the pages' job
  if (req.mode === "navigate") {
    event.respondWith(APP_PAGES.includes(url.pathname) ? asset(event, req) : page(req, url));
  } else if (url.pathname.startsWith(BASE + "static/") || url.pathname === BASE + "manifest.webmanifest") {
    event.respondWith(asset(event, req));
  }
});

// page tries the network (briefly). Failing that, the home page comes from
// the shell, and a page or listing (in a variant or not: girl/1/ls/,
// pirate/1/ls/, section/1/) gets the app's shell, which shows it from the
// repository the pages have cached; anything else gets the offline page.
async function page(req, url) {
  try {
    return await withTimeout(fetch(req), NETWORK_TIMEOUT);
  } catch {
    if (url.pathname === BASE) {
      const home = await caches.match(BASE);
      if (home) return home;
    }
    if (appView(url)) {
      const shell = await caches.match(BASE + "static/shell.html");
      if (shell) return shell;
    }
    return (await caches.match(BASE + "offline/")) || Response.error();
  }
}

// The site's top-level directories (and files) that aren't variants (as
// routes.js's RESERVED): anything else at the top is a variant.
const RESERVED = ["section", "search", "browse", "offline", "static", "repo", "src",
  "404.html", "index.html", "sw.js", "sitemap.xml", "robots.txt", "manifest.webmanifest"];

// appView reports whether the app's shell shows an address: a listing,
// or anything in a variant.
function appView(url) {
  const first = url.pathname.slice(BASE.length).split("/")[0];
  return first === "section" || (first !== "" && !RESERVED.includes(first) && /^[a-z][a-z0-9-]{0,31}$/.test(first));
}

// asset serves the shell from its cache (it's refreshed whenever the site
// is republished, as that changes VERSION) and anything else from the
// network, falling back to whatever is cached.
async function asset(event, req) {
  const hit = await caches.match(req, { ignoreSearch: true });
  if (hit) return hit;
  try {
    return await fetch(req);
  } catch {
    return Response.error();
  }
}

function withTimeout(p, ms) {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error("timeout")), ms);
    p.then(v => { clearTimeout(t); resolve(v); }, e => { clearTimeout(t); reject(e); });
  });
}
