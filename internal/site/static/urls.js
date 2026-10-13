// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The site's addresses, as internal/site makes them.

import { escapeName } from "./format.js";
import { isSeries } from "./manual.js";

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

// pageURL is a page's static address: man/<sec>/<name>/, or
// <series>/<name>/.
export function pageURL(base, name, sec) {
  return isSeries(sec) ? `${base}${seg(sec)}/${seg(name)}/` : `${base}man/${seg(sec)}/${seg(name)}/`;
}

// listingURL is page n of a static listing: a chapter's (section/3/, for
// sub ""), one of its sections' (section/3/3ssl/, or section/3/3/ for just
// 3), or a series' (rfc/).
export function listingURL(base, key, sub = "", n = 1) {
  const u = isSeries(key) ? `${base}${seg(key)}/`
    : `${base}section/${seg(key)}/${sub ? seg(sub) + "/" : ""}`;
  return n > 1 ? `${u}page/${n}/` : u;
}

// browseURL is a view in the browse app, which lives in the home page's
// hash: #/[<source>/]<sec>/<name>[/<version>][/<variant>][/mono] for a page
// (the latest version, or as a source has it, unless a version is given:
// an upstream version number, or the start of a source hash), and
// #/[<source>/]section/<key>[/<sec>] for a listing, where a source is a
// distro or distro/release.
export function browseURL(base, { src = "", section = "", sub = "", page = "", sec = "", version = "", variant = "", mono = false, at = "" } = {}) {
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
  }
  return base + "#/" + segs.map(pathEscape).join("/");
}
