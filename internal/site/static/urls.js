// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The site's addresses, as internal/site makes them.

import { escapeName } from "./format.js";
import { isSeries } from "./manual.js";
import { preferredVariant } from "./prefs.js";

// The voice (variant) the app's showing, from its address (pirate/…), or
// "" for none: links follow it.
let voice = "";
/** @param {string} v */
export const setVoice = (v) => { voice = v; };
export const currentVoice = () => voice;
// linkVoice is the variant pages are linked to in: the one shown, or the
// reader's default, or girly.
export const linkVoice = () => voice || preferredVariant() || "girl";

// segmentName matches site.SegmentName: the directory a name gets.
export function segmentName(s) {
  return s.startsWith(".") ? escapeName(s) : s;
}

// pathEscape escapes a path segment as Go's url.PathEscape does, so the
// app's addresses are exactly the static site's: it leaves $&+,:;=@ as
// they are, which encodeURIComponent wouldn't, and escapes !'()*, which it
// wouldn't either.
export const pathEscape = (s) => encodeURIComponent(s)
  .replace(/%(24|26|2B|2C|3A|3B|3D|40)/g, (m) => decodeURIComponent(m))
  .replace(/[!'()*]/g, (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase());

const seg = (s) => pathEscape(segmentName(s));

// pageURL is a page's address in a variant: <variant>/<sec>/<name>/
// (girl/1/ls/, the static page; pirate/1/ls/, man/1/ls/ the app's).
export function pageURL(base, name, sec, variant = linkVoice()) {
  return `${base}${seg(variant)}/${seg(sec)}/${seg(name)}/`;
}

// listingURL is page n of a listing: a chapter's (section/3/, for sub
// ""), one of its sections' (section/3/3ssl/, or section/3/3/ for just 3),
// or a series' (section/rfc/); in a voice (a variant, whose pages it links
// to), under it (pirate/section/3/).
export function listingURL(base, key, sub = "", n = 1, v = voice) {
  const u = `${base}${v ? seg(v) + "/" : ""}section/${seg(key)}/${sub && !isSeries(key) ? seg(sub) + "/" : ""}`;
  return n > 1 ? `${u}page/${n}/` : u;
}

// homeURL and searchURL are the home and search pages, in a voice or not.
export const homeURL = (base, v = voice) => `${base}${v ? seg(v) + "/" : ""}`;
export const searchURL = (base, v = voice) => `${homeURL(base, v)}search/`;

// packagesURL is page n of the list of packages from a source ("" for
// all, "debian", "debian/trixie"): packages/[<distro>/[<release>/]].
export function packagesURL(base, src = "", n = 1) {
  const u = `${base}packages/${src ? src.split("/").map(seg).join("/") + "/" : ""}`;
  return n > 1 ? `${u}page/${n}/` : u;
}

// packageURL is page n of a package's listing of its pages: package/<name>/.
// (From one source, it's the browse app's: browseURL's pkg.)
export function packageURL(base, name, n = 1) {
  const u = `${base}package/${seg(name)}/`;
  return n > 1 ? `${u}page/${n}/` : u;
}

// browseURL is a view in the browse app, which lives in the home page's
// hash: #/[<source>/]<sec>/<name>[/<version>][/<variant>][/mono] for a page
// (the latest version, or as a source has it, unless a version is given:
// an upstream version number, or the start of a source hash), and
// #/[<source>/]section/<key>[/<sec>] for a listing, and
// #/[<source>/]package/<name> for a package's pages, where a source is a
// distro or distro/release.
export function browseURL(base, { src = "", section = "", sub = "", page = "", sec = "", version = "", variant = "", mono = false, at = "", pkg = "" } = {}) {
  const segs = src ? src.split("/") : [];
  if (page) {
    segs.push(sec, page);
    if (version) segs.push(version);
    if (variant) segs.push(variant);
    if (mono) segs.push("mono");
    if (at) segs.push(at);
  } else if (section) {
    segs.push("section", section);
    if (sub) segs.push(sub);
  } else if (pkg) {
    segs.push("package", pkg);
  }
  return base + "#/" + segs.map(pathEscape).join("/");
}
