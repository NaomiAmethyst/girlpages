// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// Building HTML safely: views write html`<p>${text}</p>`, and whatever
// goes in is escaped, unless it's HTML already (made by html, or marked
// with raw), so there's no forgetting to.

// esc matches Go's html.EscapeString.
export function esc(s) {
  return String(s).replace(/[&'<>"]/g, c => ({ "&": "&amp;", "'": "&#39;", "<": "&lt;", ">": "&gt;", '"': "&#34;" })[c]);
}

// HTML is markup that's safe to put in a page as it is.
export class HTML {
  constructor(s) { this.s = s; }
  toString() { return this.s; }
}

// raw marks markup made some other way (rendered pages) as safe.
export const raw = (s) => new HTML(String(s));

// piece is a value as it goes into html: HTML as it is, lists of things
// one after another, nothing for null, undefined and false, and anything
// else as escaped text.
function piece(v) {
  if (v == null || v === false) return "";
  if (v instanceof HTML) return v.s;
  if (Array.isArray(v)) return v.map(piece).join("");
  return esc(v);
}

// html is a template tag that makes HTML, escaping what goes into it.
export function html(strings, ...values) {
  let out = strings[0];
  for (let i = 0; i < values.length; i++) out += piece(values[i]) + strings[i + 1];
  return new HTML(out);
}
