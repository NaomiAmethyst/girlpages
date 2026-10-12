// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages service worker: keeps the app shell (the scripts, styles, and
// the home, search and offline pages) for offline use. Repository files
// aren't touched here; the pages cache those themselves
// (static/girlpages.js). Static pages aren't kept either: offline, they're
// sent to the browse app, which renders the same thing from the cached
// repository.

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

// page tries the network (briefly). Failing that, a man page or section
// listing goes to the browse app, the home page comes from the shell, and
// anything else gets the offline page.
async function page(req, url) {
  try {
    return await withTimeout(fetch(req), NETWORK_TIMEOUT);
  } catch {
    const there = offlineView(url);
    if (there) return Response.redirect(there, 302);
    if (url.pathname === BASE) {
      const home = await caches.match(BASE);
      if (home) return home;
    }
    return (await caches.match(BASE + "offline/")) || Response.error();
  }
}

// offlineView is the browse app's view of a static page, if it has one:
// man/<sec>/<name>/ is a page, section/<key>/ and section/<key>/<sec>/ a
// listing. The app lives in the home page, which the shell has.
function offlineView(url) {
  let parts;
  try {
    parts = url.pathname.slice(BASE.length).split("/").filter(Boolean).map(decodeURIComponent).map(unsegment);
  } catch {
    return "";
  }
  let route;
  if (parts[0] === "man" && parts.length === 3) route = [parts[1], parts[2]];
  else if (parts[0] === "section" && (parts.length === 2 || parts.length === 3)) route = parts;
  // A series (rfc/9110/, rfc/): anything else that isn't one of the site's own.
  else if (isSeries(parts[0]) && !RESERVED.includes(parts[0]) && parts.length === 2) route = [parts[0], parts[1]];
  else if (isSeries(parts[0]) && !RESERVED.includes(parts[0]) && parts.length === 1) route = ["section", parts[0]];
  else return "";
  return BASE + "#/" + route.map(encodeURIComponent).join("/");
}

const isSeries = (sec) => sec.length > 1 && !/^[0-9]/.test(sec);
const RESERVED = ["man", "section", "search", "browse", "offline", "static", "repo", "src"];

// unsegment undoes site.SegmentName, which escapes names starting with a
// dot as file names are.
function unsegment(s) {
  return s.startsWith("%") ? decodeURIComponent(s) : s;
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
