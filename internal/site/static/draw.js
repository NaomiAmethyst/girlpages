// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// Drawing ahead. Long listings' blocks and long pages' parts aren't laid out
// or painted while they're off the screen (content-visibility: auto, in
// style.css), which makes them quick to show; but the browser only draws
// them as they come within a little of the screen, so scrolling fast can
// outrun it, and show the background where they'll be. A Drawer marks the
// ones within a few screens .near, which draws them, well before they're
// scrolled to.

// How near the screen something's drawn.
const DRAW = 3000;

export class Drawer {
  constructor(margin = DRAW) {
    this.io = "IntersectionObserver" in globalThis
      ? new IntersectionObserver((es) => { for (const e of es) e.target.classList.toggle("near", e.isIntersecting); }, { rootMargin: `${margin}px 0px` })
      : null;
  }

  /** @param {Iterable<Element>} els */
  watch(els) { for (const el of els) this.io?.observe(el); return this; }

  /** @param {Iterable<Element>} els */
  unwatch(els) { for (const el of els) this.io?.unobserve(el); }

  destroy() { this.io?.disconnect(); }
}
