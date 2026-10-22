// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The app's addresses: what each shows.

import { browseURL } from "./urls.js";

export const isRoute = (h) => /^#\/./.test(h);

// The site's top-level directories (and files) that aren't variants (as
// site.Reserved): anything else at the top is a variant (a voice).
export const RESERVED = new Set(["section", "search", "browse", "offline", "static", "repo", "src",
  "404.html", "index.html", "sw.js", "sitemap.xml", "robots.txt", "manifest.webmanifest"]);

// unsegment undoes site.SegmentName (names starting with a dot are escaped).
const unsegment = (s) => (s.startsWith("%") ? decodeURIComponent(s) : s);

// viewOf is the view an address (a URL) shows in the app: a route, or one
// of the static site's pages (which, once the app is running, it shows
// itself), or null if it's not one of the site's.
/**
 * What an address shows, and the voice (variant) it's in: "" for none.
 * @typedef {{kind: "route", hash: string, voice: string}
 *   | {kind: "home", voice: string}
 *   | {kind: "page", sec: string, name: string, voice: string}
 *   | {kind: "section", key: string, sub: string, page: number, voice: string}
 *   | {kind: "search", params: URLSearchParams, voice: string}} View
 */

// viewOf is the view an address (a URL) shows in the app: a route, or one
// of the site's pages (which, once the app is running, it shows itself),
// in a voice (girl/1/ls/, pirate/section/1/) or not; null if it's not one
// of the site's.
/** @param {URL} u @param {string} base @param {string} [origin] @returns {View | null} */
export function viewOf(u, base, origin = globalThis.location?.origin) {
  if (u.origin !== origin || !u.pathname.startsWith(base)) return null;
  if (u.pathname === base && isRoute(u.hash)) return { kind: "route", hash: u.hash, voice: "" };
  if (u.pathname === base) return { kind: "home", voice: "" };
  let parts;
  try {
    parts = u.pathname.slice(base.length).split("/").filter(Boolean).map(decodeURIComponent).map(unsegment);
  } catch {
    return null;
  }
  let voice = "";
  if (!RESERVED.has(parts[0])) {
    if (!/^[a-z][a-z0-9-]{0,31}$/.test(parts[0])) return null;
    voice = parts.shift() || "";
    if (parts.length === 0) return { kind: "home", voice };
    if (parts.length === 2 && parts[0] !== "section") return { kind: "page", sec: parts[0], name: parts[1], voice };
  }
  if (parts[0] === "search" && parts.length === 1) return { kind: "search", params: u.searchParams, voice };
  if (parts[0] === "section") {
    let page = 1;
    if (parts.length >= 4 && parts[parts.length - 2] === "page") page = +parts.splice(-2)[1] || 1;
    if (parts.length === 2 || parts.length === 3) return { kind: "section", key: parts[1], sub: parts[2] || "", page, voice };
  }
  return null;
}

// parse reads a route (a hash, #/…), resolving to what it shows; sources()
// resolves to the manifest's sources.
/**
 * What a route shows.
 * @typedef {{kind: "bad"}
 *   | {kind: "source", src: string}
 *   | {kind: "section", src: string, key: string, sub: string}
 *   | {kind: "page", src: string, sec: string, name: string, rest: string[]}} Route
 */

/** @param {string} h @param {() => Promise<any[]>} sources @returns {Promise<Route>} */
export async function parse(h, sources) {
  let segs;
  try {
    segs = h.slice(2).split("/").filter(x => x !== "").map(decodeURIComponent);
  } catch {
    return { kind: "bad" };
  }
  let src = "";
  // Only a word that isn't a section or "section" can start a source.
  if (segs.length && segs[0] !== "section" && !/^[0-9]/.test(segs[0])) {
    const known = (await sources()) || [];
    if (known.some(s => s.distro === segs[0])) {
      src = segs.shift();
      if (segs.length && known.some(s => s.distro === src && s.release === segs[0])) src += "/" + segs.shift();
    }
  }
  if (!segs.length) return { kind: "source", src };
  if (segs[0] === "section") return segs[1] ? { kind: "section", src, key: segs[1], sub: segs[2] || "" } : { kind: "source", src };
  if (segs.length < 2) return { kind: "bad" };
  return { kind: "page", src, sec: segs[0], name: segs[1], rest: segs.slice(2) };
}

// legacyRoute turns a /browse/?… query from earlier versions into a route.
export function legacyRoute(q, base) {
  const src = q.get("src") || "";
  if (q.get("page") && q.get("sec")) {
    return browseURL(base, { page: q.get("page"), sec: q.get("sec"), src, version: (q.get("v") || "").slice(0, 12),
      variant: q.get("variant") || (q.get("mono") ? "man" : ""), mono: q.get("mono") === "1" });
  }
  if (q.get("section")) return browseURL(base, { src, section: q.get("section"), sub: q.get("sub") || "" });
  return browseURL(base, { src });
}
