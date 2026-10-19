// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// Contents that follow the reading. As a page scrolls, the contents' link
// to the section being read is marked (.here, and aria-current for screen
// readers), and kept in sight in the contents' own scrolling. The section
// being read is the last whose heading has gone past a line a little way
// down the screen (past the bar at the top, where following a link to a
// heading puts it), or at the end of the page, the last whose heading is on
// the screen at all, so short sections at the end get their turn. It's
// worked out again as the page scrolls, and as it changes size: parts of a
// long page are drawn as they're neared (see draw.js), which moves what's
// after them without any scrolling.

// How far down the screen the line is, as a part of its height.
const LINE = 0.3;

export class Spy {
  /** @param {HTMLElement} nav the contents (nav.toc), linking to headings by #id */
  constructor(nav) {
    this.nav = nav;
    /** @type {{heading: HTMLElement, link: HTMLElement}[]} */
    this.sections = [];
    /** @type {HTMLElement | null} */
    this.here = null;
    this.frame = 0;
    this.onScroll = () => {
      if (!this.frame) this.frame = requestAnimationFrame(() => { this.frame = 0; this.update(); });
    };
    this.resized = "ResizeObserver" in globalThis ? new ResizeObserver(this.onScroll) : null;
  }

  // watch starts following the reading, of the page the contents are in:
  // its headings, not another's with the same ids (a static page the app
  // has covered can have a DESCRIPTION too).
  watch() {
    const doc = this.nav.ownerDocument;
    const page = this.nav.closest(".layout") || doc;
    for (const a of /** @type {NodeListOf<HTMLAnchorElement>} */ (this.nav.querySelectorAll('a[href^="#"]'))) {
      const heading = /** @type {HTMLElement | null} */ (page.querySelector(`[id="${CSS.escape(decodeURIComponent(a.hash.slice(1)))}"]`));
      if (heading) this.sections.push({ heading, link: a });
    }
    if (!this.sections.length) return this;
    addEventListener("scroll", this.onScroll, { passive: true });
    addEventListener("resize", this.onScroll, { passive: true });
    this.resized?.observe(doc.body);
    this.update();
    return this;
  }

  // update marks the section being read.
  update() {
    // Contents that aren't shown (on a narrow screen, or a page the app
    // has covered) have nothing to follow.
    if (!this.nav.checkVisibility()) return;
    const pad = parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop) || 0;
    const line = Math.max(pad + 8, innerHeight * LINE);
    const ss = this.sections;
    // The headings are in order down the page: the last above the line.
    let lo = 0, hi = ss.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (ss[mid].heading.getBoundingClientRect().top <= line) lo = mid + 1;
      else hi = mid;
    }
    let i = lo - 1;
    if (innerHeight + scrollY >= document.documentElement.scrollHeight - 2) {
      while (i + 1 < ss.length && ss[i + 1].heading.getBoundingClientRect().top < innerHeight) i++;
    }
    this.mark(i >= 0 ? ss[i].link : null);
  }

  /** @param {HTMLElement | null} link */
  mark(link) {
    if (link === this.here) return;
    if (this.here) {
      this.here.classList.remove("here");
      this.here.removeAttribute("aria-current");
    }
    this.here = link;
    if (!link) return;
    link.classList.add("here");
    link.setAttribute("aria-current", "location");
    // In sight in the contents (which scroll by themselves when they're
    // longer than the screen), without scrolling the page.
    const n = this.nav.getBoundingClientRect(), l = link.getBoundingClientRect();
    if (l.top < n.top + 40) this.nav.scrollTop -= n.top + 40 - l.top;
    else if (l.bottom > n.bottom - 16) this.nav.scrollTop += l.bottom - (n.bottom - 16);
  }

  destroy() {
    removeEventListener("scroll", this.onScroll);
    removeEventListener("resize", this.onScroll);
    this.resized?.disconnect();
    cancelAnimationFrame(this.frame);
  }
}
