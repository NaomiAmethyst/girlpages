// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The repository's formats (see doc/repo-format.md): decoding its compact
// indexes, and where its files are.

// escapeName matches repo.EscapeName: how a page name or section appears in
// a repository path.
export function escapeName(s) {
  const bytes = new TextEncoder().encode(s);
  let out = "";
  for (let i = 0; i < bytes.length; i++) {
    const c = bytes[i];
    const ch = String.fromCharCode(c);
    const ok = /[A-Za-z0-9_+\-.,:@]/.test(ch) && c < 0x80;
    if (!ok || (i === 0 && ch === ".")) out += "%" + c.toString(16).toUpperCase().padStart(2, "0");
    else out += ch;
  }
  return out;
}

// enc is how a name or section appears in a repository URL: escaped as a
// file name, then percent-encoded, as the browse app writes paths.
export const enc = (s) => encodeURIComponent(escapeName(s));

// namePath is the pointer to a page's listing; listingPath its listing,
// and variantPath a version's variant, both named after their hashes.
export const namePath = (name, sec) => `page/name-${enc(sec)}/${enc(name)}.json`;
export const listingPath = (sha) => `page/page-${sha.slice(0, 2)}/${sha}.json`;
export const variantPath = (source, variant, sha) => `contents/${source.slice(0, 2)}/${source}/${variant}-${sha}.json`;

// Indexes are compact: lists of records are tables ({keys, data}, the
// fields named once and each record's values in turn), and hashes in them
// are in basE91 (with ' for "), as repo.Encode91 writes them.
const B91 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#$%&()*+,./:;<=>?@[]^_`{|}~'";

const B91D = new Map([...B91].map((c, i) => [c, i]));

// decode91 decodes basE91 to bytes.
export function decode91(str) {
  const out = [];
  let queue = 0, nbits = 0, v = -1;
  for (const ch of str) {
    const c = B91D.get(ch);
    if (c === undefined) throw new Error(`${JSON.stringify(str)} isn't basE91`);
    if (v < 0) {
      v = c;
      continue;
    }
    v += c * 91;
    queue |= v << nbits;
    nbits += (v & 8191) > 88 ? 13 : 14;
    while (nbits > 7) {
      out.push(queue & 255);
      queue >>>= 8;
      nbits -= 8;
    }
    v = -1;
  }
  if (v >= 0) out.push((queue | (v << nbits)) & 255);
  return out;
}

// unhash reads a hash in basE91 as hex.
export const unhash = (str) => (str ? decode91(str).map(b => b.toString(16).padStart(2, "0")).join("") : "");

// rows reads a table as records, decoding the fields named in hashes (and
// in hashMaps, objects whose values are hashes).
/** @param {any} table @param {{hashes?: string[], hashMaps?: string[]}} [opts] @returns {any[]} */
export function rows(table, { hashes = [], hashMaps = [] } = {}) {
  const keys = (table && table.keys) || [], data = (table && table.data) || [], out = [];
  for (let i = 0; keys.length && i + keys.length <= data.length; i += keys.length) {
    const r = {};
    keys.forEach((k, j) => { r[k] = data[i + j]; });
    for (const h of hashes) if (r[h]) r[h] = unhash(r[h]);
    for (const h of hashMaps) {
      if (!r[h]) continue;
      const m = {};
      for (const [k, v] of Object.entries(r[h])) m[k] = unhash(v);
      r[h] = m;
    }
    out.push(r);
  }
  return out;
}

// indexPagesOf reads a chapter index's (or shard's) pages (see
// repo.IndexPages): each {name, section, title, description, page,
// version, versions, sources, variants, girl}, the last three lists.
export function indexPagesOf(t) {
  if (!t || !t.keys) return [];
  const bits = (m, vals) => {
    /** @type {string[]} */
    const out = [];
    for (let i = 0; m > 0; i++, m = Math.floor(m / 2)) if (m % 2) out.push(vals[i]);
    return out;
  };
  return rows(t, { hashes: ["page"] }).map(r => ({
    ...r, section: t.sections[r.section], version: t.versions[r.version] || "",
    sources: bits(r.sources, t.sources), variants: bits(r.variants, t.variants), girl: bits(r.girl, t.sources),
  }));
}

// chapterShardsOf reads a chapter index's shards: {sha256, bytes, count, first, last, prev}.
export const chapterShardsOf = (ci) => rows(ci.shards, { hashes: ["sha256"] });

// chapterFile is a chapter's index, from its manifest entry.
export const chapterFile = (e) => `section/${enc(e.chapter)}-${e.sha256}.json`;

// shardsOf reads a manifest's (or section index's) shards.
export const shardsOf = (m) => rows(m.shards, { hashes: ["sha256"] });

// sectionShard is a shard of a long section's index.
export const sectionShard = (sha) => `section/shard-${sha.slice(0, 2)}/${sha}.json`;
