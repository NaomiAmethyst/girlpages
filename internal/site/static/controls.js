// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// A page's variants and versions, within reach while reading it: once
// they've scrolled off the screen, a copy of them is at the top of its
// contents. A copy, as taking them out of the page would move what's being
// read. Its links work as theirs do, as links are followed from the document
// (see girlpages.js).
//
// Where the contents aren't shown (a narrow screen: style.css says which), a
// button in the corner opens a copy of them and of those, always, as the top
// of a page is when one most wants to jump into it.

import { headingsOf, readingNow } from "./place.js";

export class PageControls {
  /** @param {HTMLElement} toc the contents (nav.toc) of a page */
  constructor(toc) {
    this.toc = toc;
    this.layout = toc.closest(".layout");
    this.page = this.layout?.querySelector("article.manpage") ?? null;
    /** @type {Element[]} */
    this.rows = [];
    this.box = document.createElement("div");
    this.box.className = "toc-controls controls";
    this.box.hidden = true;
    this.corner = document.createElement("details");
    this.corner.className = "corner-controls";
    const button = document.createElement("summary");
    button.textContent = "☰";
    button.setAttribute("aria-label", "this page's contents, variants and versions");
    button.title = "contents, variants and versions";
    this.panel = document.createElement("div");
    this.panel.className = "controls";
    this.corner.append(button, this.panel);
    this.corner.addEventListener("toggle", () => {
      if (this.corner.open) this.openPanel();
    });
    /** @type {IntersectionObserver | null} */
    this.io = null;
  }

  // watch starts following whether they're on the screen.
  watch() {
    if (!this.page) return this;
    this.layout?.append(this.corner);
    if (!("IntersectionObserver" in globalThis)) return this;
    this.rows = [...this.page.querySelectorAll(".meta")];
    if (!this.rows.length) return this;
    this.toc.prepend(this.box);
    this.io = new IntersectionObserver(() => this.update(), { rootMargin: `-${this.bar()}px 0px 0px 0px` });
    for (const r of this.rows) this.io.observe(r);
    return this;
  }

  // bar is how much of the top of the screen the bar there covers.
  bar() {
    return parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop) || 0;
  }

  // update shows the copy if they've all gone past the top of the screen.
  update() {
    const bar = this.bar(), shown = this.rows.filter(r => r.checkVisibility());
    const past = shown.length > 0 && shown.every(r => r.getBoundingClientRect().bottom <= bar);
    if (past === !this.box.hidden) return;
    if (past) this.fill(this.box);
    this.box.hidden = !past || !this.box.childElementCount;
  }

  // openPanel fills the corner's panel, as things are now, with the section
  // being read marked and in sight.
  openPanel() {
    this.fill(this.panel);
    const contents = document.createElement("nav");
    contents.className = "corner-contents";
    contents.setAttribute("aria-label", "Contents");
    for (const el of this.toc.children) {
      if (el !== this.box) contents.append(el.cloneNode(true));
    }
    this.panel.append(contents);
    const page = /** @type {Element} */ (this.page);
    const hs = headingsOf(page), at = readingNow();
    const links = new Map([...contents.querySelectorAll("a[href^='#']")].map(a => [decodeURIComponent((a.getAttribute("href") || "").slice(1)), a]));
    // The heading being read, or the last before it that's in the contents.
    for (let i = at ? at.i : -1; i >= 0; i--) {
      const link = /** @type {HTMLElement | undefined} */ (links.get(hs[i].id));
      if (!link) continue;
      link.classList.add("here");
      link.setAttribute("aria-current", "location");
      this.panel.scrollTop += link.getBoundingClientRect().top - this.panel.getBoundingClientRect().top - this.panel.clientHeight / 2;
      break;
    }
  }

  // fill copies them, as they are now, into el.
  /** @param {HTMLElement} el */
  fill(el) {
    const page = /** @type {Element} */ (this.page);
    el.replaceChildren();
    const versions = page.querySelector("details.versions");
    if (versions) {
      const copy = /** @type {HTMLDetailsElement} */ (versions.cloneNode(true));
      copy.removeAttribute("id");
      copy.open = false;
      el.append(copy);
    }
    // (Not the button for choosing a default, which belongs to its page.)
    const pills = [...page.querySelectorAll(".meta .pill:not(button)")].filter(p => p.checkVisibility());
    if (pills.length) {
      const row = document.createElement("div");
      row.className = "pills";
      row.append(...pills.map(p => p.cloneNode(true)));
      el.append(row);
    }
  }

  destroy() {
    this.io?.disconnect();
    this.box.remove();
    this.corner.remove();
  }
}
