// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages service worker: keeps the app shell and the pages you've read
// available offline. Repository files aren't touched here; the pages cache
// those in localStorage themselves (static/girlpages.js).

const BASE = /*BASE*/"/";
const VERSION = /*VERSION*/"dev";
const SHELL = /*SHELL*/[];
const SHELL_CACHE = "girlpages-shell-" + VERSION;
const PAGE_CACHE = "girlpages-pages";
const MAX_PAGES = 300;
const NETWORK_TIMEOUT = 4000;

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
      if (name.startsWith("girlpages-shell-") && name !== SHELL_CACHE) await caches.delete(name);
    }
    await self.clients.claim();
  })());
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== location.origin || !url.pathname.startsWith(BASE)) return;
  if (url.pathname.startsWith(BASE + "repo/")) return; // localStorage's job
  if (req.mode === "navigate") {
    event.respondWith(page(event, req));
  } else if (url.pathname.startsWith(BASE + "static/") || url.pathname === BASE + "manifest.webmanifest") {
    event.respondWith(asset(event, req));
  }
});

// page tries the network (briefly), then pages read before, then the
// offline page.
async function page(event, req) {
  const cache = await caches.open(PAGE_CACHE);
  try {
    const res = await withTimeout(fetch(req), NETWORK_TIMEOUT);
    if (res.ok) event.waitUntil(remember(cache, req, res.clone()));
    return res;
  } catch {
    const hit = await cache.match(req, { ignoreSearch: true }) ||
      await caches.match(req, { ignoreSearch: true });
    if (hit) return hit;
    return (await caches.match(BASE + "offline/")) || Response.error();
  }
}

// remember stores a page, dropping the oldest beyond MAX_PAGES.
async function remember(cache, req, res) {
  await cache.delete(req, { ignoreSearch: true }); // so it moves to the end
  await cache.put(req, res);
  const keys = await cache.keys();
  for (const k of keys.slice(0, Math.max(0, keys.length - MAX_PAGES))) await cache.delete(k);
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
