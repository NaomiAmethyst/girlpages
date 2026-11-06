// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// A page's variants and versions, within reach while reading it: once
// they've scrolled off the screen, a copy of them is at the top of its
// contents, or where those aren't shown (a narrow screen: style.css says
// which), behind a button in the corner. A copy, as taking them out of the
// page would move what's being read. Its links work as theirs do, as links
// are followed from the document (see girlpages.js).

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
    this.corner.hidden = true;
    const button = document.createElement("summary");
    button.textContent = "🎛️";
    button.setAttribute("aria-label", "variants and versions of this page");
    button.title = "variants and versions";
    this.panel = document.createElement("div");
    this.panel.className = "controls";
    this.corner.append(button, this.panel);
    /** @type {IntersectionObserver | null} */
    this.io = null;
  }

  // watch starts following whether they're on the screen.
  watch() {
    if (!this.page || !("IntersectionObserver" in globalThis)) return this;
    this.rows = [...this.page.querySelectorAll(".meta")];
    if (!this.rows.length) return this;
    this.toc.prepend(this.box);
    this.layout?.append(this.corner);
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
    if (past) {
      this.fill(this.box);
      this.fill(this.panel);
    }
    this.corner.open = false;
    this.box.hidden = this.corner.hidden = !past || !this.box.childElementCount;
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
