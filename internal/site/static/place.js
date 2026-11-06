// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// Keeping the reader's place when a page changes variant: the same section,
// the same part of the way through it. Variants write their headings their
// own way ("✨ OPTIONS ✨", "# options") and may add or drop some, so a
// heading's matched by its words, in order, and failing that by where it
// falls among those that match. A section's length differs between them, so
// the part of the way through is a share of its height, not a distance.
//
// History keeps a page's place the same way: a distance down a long page
// depends on how much of it had been drawn (see draw.js).

/** The page on the screen, if one is. */
export const thePage = () => [...document.querySelectorAll("article.manpage")].find(a => a.checkVisibility());

/**
 * A page's headings, in order.
 * @param {Element} page
 */
export const headingsOf = (page) => [...page.querySelectorAll(".content :is(h1, h2, h3, h4, h5, h6), .original h2")];

// The line a little under the bar at the top of the screen, where following
// a link to a heading puts it.
const line = () => (parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop) || 0) + 2;

/**
 * A heading's words, for comparing with another variant's.
 * @param {string} text
 */
export const words = (text) => text.normalize("NFKD").toLowerCase().replace(/[^\p{L}\p{N}]+/gu, " ").trim();

/**
 * headingFor is which of the headings to (as words) is the same section as
 * from[i], or -1 if to is empty: the one with the same words, or (with the
 * flourish some variants add) containing them or contained by them; failing
 * that, as far after the last one before it that matched, short of the first
 * after it that did; and with none matching at all, as far through.
 * @param {string[]} from
 * @param {number} i
 * @param {string[]} to
 */
export function headingFor(from, i, to) {
  if (!to.length) return -1;
  const holds = (a, b) => a.length >= 3 && ` ${b} `.includes(` ${a} `);
  const matched = [];
  let next = 0;
  for (const name of from) {
    let j = to.indexOf(name, next);
    if (j < 0) j = to.findIndex((t, k) => k >= next && (holds(name, t) || holds(t, name)));
    matched.push(j);
    if (j >= 0) next = j + 1;
  }
  if (matched[i] >= 0) return matched[i];
  let before = i - 1, after = i + 1;
  while (before >= 0 && matched[before] < 0) before--;
  while (after < from.length && matched[after] < 0) after++;
  if (before < 0 && after >= from.length) return Math.round(i * (to.length - 1) / Math.max(1, from.length - 1));
  const last = after < from.length ? matched[after] - 1 : to.length - 1;
  const j = before < 0 ? i : matched[before] + (i - before);
  return Math.max(0, Math.min(j, last));
}

// sectionHeight is how tall heading i's section is, drawn: what's off the
// screen is only estimated (see draw.js).
/** @param {Element[]} hs @param {number} i */
function sectionHeight(hs, i) {
  const h = hs[i], end = hs[i + 1];
  for (let el = h.nextElementSibling; el && el !== end; el = el.nextElementSibling) el.classList.add("near");
  const bottom = end ? end.getBoundingClientRect().top : /** @type {Element} */ (h.parentElement).getBoundingClientRect().bottom;
  return bottom - h.getBoundingClientRect().top;
}

// reading is which of the headings hs is being read, or -1 if none is yet,
// and how far through its section, drawing it first if asked (which moves
// things, so not while it's being read).
/** @param {Element[]} hs @param {boolean} draw */
function reading(hs, draw) {
  const at = line();
  let i = -1;
  while (i + 1 < hs.length && hs[i + 1].getBoundingClientRect().top <= at) i++;
  if (i < 0) return { i, through: 0 };
  if (draw) sectionHeight(hs, i);
  const top = hs[i].getBoundingClientRect().top, end = hs[i + 1] || hs[i].parentElement;
  const bottom = hs[i + 1] ? end.getBoundingClientRect().top : end.getBoundingClientRect().bottom;
  return { i, through: bottom > top ? Math.min(1, Math.max(0, (at - top) / (bottom - top))) : 0 };
}

// goTo scrolls to how far through heading j's section.
/** @param {Element[]} hs @param {number} j @param {number} through */
function goTo(hs, j, through) {
  const tall = sectionHeight(hs, j);
  hs[j].scrollIntoView({ behavior: "instant" });
  scrollBy({ top: through * tall, behavior: "instant" });
}

/**
 * readingNow is the place on the page on the screen, for history to keep,
 * if there's a page with a heading gone past.
 */
export function readingNow() {
  const page = thePage();
  if (!page) return undefined;
  const r = reading(headingsOf(page), false);
  return r.i < 0 ? undefined : r;
}

/**
 * returnTo goes back to a place readingNow gave, on the same page, reporting
 * whether it could.
 * @param {{i: number, through: number}} at
 */
export function returnTo(at) {
  const page = thePage();
  const hs = page ? headingsOf(page) : [];
  if (!hs[at.i]) return false;
  goTo(hs, at.i, at.through);
  return true;
}

// Where the reader was, for the address they're going to.
/** @type {{href: string, names: string[], i: number, through: number} | null} */
let held = null;

/**
 * holdPlace notes where the reader is, on their way to href. Before the
 * first heading isn't anywhere in particular: a new page starts at the top.
 * @param {string} href
 */
export function holdPlace(href) {
  held = null;
  const page = thePage();
  if (!page) return;
  const hs = headingsOf(page), { i, through } = reading(hs, true);
  if (i >= 0) held = { href, names: hs.map(h => words(h.textContent || "")), i, through };
}

/**
 * takePlace is what puts the reader back in their place on the page shown
 * at href, if one's held for it. It's only for that address: one held for
 * somewhere they didn't end up is dropped.
 * @param {string} href
 */
export function takePlace(href) {
  const place = held;
  held = null;
  if (!place || place.href !== href) return null;
  return () => {
    const page = thePage();
    if (!page) return;
    const hs = headingsOf(page);
    const j = headingFor(place.names, place.i, hs.map(h => words(h.textContent || "")));
    if (j >= 0) goTo(hs, j, place.through);
  };
}
