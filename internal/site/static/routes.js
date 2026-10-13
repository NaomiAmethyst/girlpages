// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The app's addresses: what each shows.

import { browseURL } from "./urls.js";
import { isSeries } from "./manual.js";

export const isRoute = (h) => /^#\/./.test(h);

// Top-level directories of the site that aren't series.
const RESERVED = new Set(["man", "section", "search", "browse", "offline", "static", "repo", "src"]);

// unsegment undoes site.SegmentName (names starting with a dot are escaped).
const unsegment = (s) => (s.startsWith("%") ? decodeURIComponent(s) : s);

// viewOf is the view an address (a URL) shows in the app: a route, or one
// of the static site's pages (which, once the app is running, it shows
// itself), or null if it's not one of the site's.
/**
 * What an address shows.
 * @typedef {{kind: "route", hash: string}
 *   | {kind: "home"}
 *   | {kind: "page", sec: string, name: string}
 *   | {kind: "section", key: string, sub: string, page: number}
 *   | {kind: "search", params: URLSearchParams}} View
 */

/** @param {URL} u @param {string} base @param {string} [origin] @returns {View | null} */
export function viewOf(u, base, origin = globalThis.location?.origin) {
  if (u.origin !== origin || !u.pathname.startsWith(base)) return null;
  if (u.pathname === base && isRoute(u.hash)) return { kind: "route", hash: u.hash };
  if (u.pathname === base) return { kind: "home" };
  let parts;
  try {
    parts = u.pathname.slice(base.length).split("/").filter(Boolean).map(decodeURIComponent).map(unsegment);
  } catch {
    return null;
  }
  if (parts[0] === "man" && parts.length === 3) return { kind: "page", sec: parts[1], name: parts[2] };
  if (isSeries(parts[0]) && !RESERVED.has(parts[0])) {
    // A series: rfc/9110/, or its listing, rfc/ or rfc/page/2/.
    if (parts.length === 2 && parts[1] !== "page") return { kind: "page", sec: parts[0], name: parts[1] };
    if (parts.length === 1) return { kind: "section", key: parts[0], sub: "", page: 1 };
    if (parts.length === 3 && parts[1] === "page") return { kind: "section", key: parts[0], sub: "", page: +parts[2] || 1 };
  }
  if (parts[0] === "search" && parts.length === 1) return { kind: "search", params: u.searchParams };
  if (parts[0] === "section") {
    let page = 1;
    if (parts.length >= 4 && parts[parts.length - 2] === "page") page = +parts.splice(-2)[1] || 1;
    if (parts.length === 2 || parts.length === 3) return { kind: "section", key: parts[1], sub: parts[2] || "", page };
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
