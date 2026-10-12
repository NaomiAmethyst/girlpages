// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// A long list shown a chunk at a time: chunks load as they come near the
// screen, above or below, and chunks far from it are emptied (keeping their
// height, so nothing moves) and filled again when they come back. The
// static listings use it to scroll through all their pages, and the browse
// app for its own listings.

// How near the screen a chunk loads, and how far before it's emptied.
const NEAR = 1500;
const FAR = 4000;

export class VirtualList {
  // container holds div.chunk[data-chunk] elements, numbered from 1, of
  // which there are count; load(n) resolves to a chunk's inner HTML.
  // onChunk(n) is told which chunk is at the top of the screen.
  constructor(container, { count, load, onChunk = () => {} }) {
    this.el = container;
    this.count = count;
    this.loadHTML = load;
    this.onChunk = onChunk;
    this.html = new Map();
    this.busy = false;
    this.current = 0;
    container.classList.add("virtual");
    for (const c of this.chunks()) this.html.set(+c.dataset.chunk, c.innerHTML);
    this.top = edge("top");
    this.bottom = edge("bottom");
    container.prepend(this.top);
    container.append(this.bottom);
    this.near = new IntersectionObserver(() => this.fill(), { rootMargin: `${NEAR}px 0px` });
    this.near.observe(this.top);
    this.near.observe(this.bottom);
    this.far = new IntersectionObserver((entries) => {
      for (const e of entries) e.isIntersecting ? this.restore(e.target) : this.empty(e.target);
    }, { rootMargin: `${FAR}px 0px` });
    for (const c of this.chunks()) this.far.observe(c);
    // Chunks above load once the list is scrolled up (or asked for, as
    // there's no scrolling up from the top), so arriving at a page shows it
    // as it is.
    this.upward = false;
    this.earlier = document.createElement("button");
    this.earlier.type = "button";
    this.earlier.className = "pill earlier";
    this.earlier.textContent = "⬆ earlier pages";
    this.earlier.hidden = this.first() <= 1;
    this.earlier.addEventListener("click", () => this.goUp());
    this.top.append(this.earlier);
    let ticking = false, lastY = scrollY;
    this.onScroll = () => {
      if (scrollY < lastY && !this.upward) this.goUp();
      lastY = scrollY;
      if (ticking) return;
      ticking = true;
      requestAnimationFrame(() => { ticking = false; this.track(); });
    };
    addEventListener("scroll", this.onScroll, { passive: true });
  }

  goUp() {
    this.upward = true;
    this.earlier.hidden = true;
    this.fill();
  }

  // destroy stops watching, for when the list goes.
  destroy() {
    this.near.disconnect();
    this.far.disconnect();
    removeEventListener("scroll", this.onScroll);
  }

  chunks() { return [...this.el.querySelectorAll(":scope > .chunk")]; }
  first() { const cs = this.chunks(); return cs.length ? +cs[0].dataset.chunk : 0; }
  last() { const cs = this.chunks(); return cs.length ? +cs[cs.length - 1].dataset.chunk : 0; }
  chunk(n) { return this.el.querySelector(`:scope > .chunk[data-chunk="${n}"]`); }

  // get is a chunk's HTML, loaded once however many ask at a time.
  async get(n) {
    if (this.html.has(n)) return this.html.get(n);
    if (!this.pending) this.pending = new Map();
    if (!this.pending.has(n)) {
      this.pending.set(n, this.loadHTML(n).then(html => {
        this.html.set(n, html);
        return html;
      }).finally(() => this.pending.delete(n)));
    }
    return this.pending.get(n);
  }

  // prefetch loads every chunk's HTML (not into the page: only those near
  // the screen go there), nearest first, a few at a time, so scrolling
  // rarely has to wait. It stops if the network goes.
  async prefetch(jobs = 2) {
    const from = this.first() || 1;
    const todo = Array.from({ length: this.count }, (_, i) => i + 1)
      .filter(n => !this.html.has(n))
      .sort((a, b) => Math.abs(a - from) - Math.abs(b - from) || a - b);
    let stop = false;
    const worker = async () => {
      while (todo.length && !stop) {
        try {
          await this.get(todo.shift());
        } catch (e) {
          if (e instanceof TypeError) stop = true;
        }
      }
    };
    await Promise.all(Array.from({ length: jobs }, worker));
  }

  make(n, html) {
    const c = document.createElement("div");
    c.className = "chunk";
    c.dataset.chunk = n;
    c.innerHTML = html;
    this.far.observe(c);
    return c;
  }

  // fill loads chunks while an end of the list is near the screen.
  async fill() {
    if (this.busy) return;
    this.busy = true;
    try {
      for (;;) {
        if (nearScreen(this.bottom) && this.last() < this.count) {
          const n = this.last() + 1;
          this.say(this.bottom, "💅 loading more…");
          this.bottom.before(this.make(n, await this.get(n)));
        } else if (this.upward && nearScreen(this.top) && this.first() > 1) {
          const n = this.first() - 1;
          this.say(this.top, "💅 loading earlier ones…");
          const c = this.make(n, await this.get(n));
          this.say(this.top, "");
          // Keep what's on screen where it is.
          const y = scrollY;
          this.top.after(c);
          scrollTo(0, y + c.offsetHeight);
        } else {
          break;
        }
      }
      this.say(this.bottom, "");
    } catch (e) {
      this.say(this.top, "");
      this.say(this.bottom, e instanceof TypeError ? "📵 you're offline, so that's all for now" : `💔 ${e.message}`);
    } finally {
      this.busy = false;
    }
  }

  // say shows a message at an end of the list ("" for none).
  say(edge, text) {
    edge.querySelector(".edge-msg").textContent = text;
  }

  // empty drops a far-off chunk's contents, keeping its height.
  empty(c) {
    if (c.dataset.empty) return;
    c.style.height = `${c.offsetHeight}px`;
    c.replaceChildren();
    c.dataset.empty = "1";
  }

  restore(c) {
    if (!c.dataset.empty) return;
    c.innerHTML = this.html.get(+c.dataset.chunk) || "";
    c.style.height = "";
    delete c.dataset.empty;
  }

  // track tells onChunk which chunk is at the top of the screen.
  track() {
    for (const c of this.chunks()) {
      const r = c.getBoundingClientRect();
      if (r.bottom > 80) {
        const n = +c.dataset.chunk;
        if (n !== this.current) {
          this.current = n;
          this.onChunk(n);
        }
        return;
      }
    }
  }

  // jump shows chunk n, scrolled to the element with id there (or its top).
  // If n isn't in the list, the list starts again from it.
  async jump(n, id = "") {
    let c = this.chunk(n);
    if (!c) {
      const html = await this.get(n);
      for (const old of this.chunks()) {
        this.far.unobserve(old);
        old.remove();
      }
      c = this.make(n, html);
      this.top.after(c);
      this.upward = false;
      this.earlier.hidden = n <= 1;
    }
    this.restore(c);
    (id && this.el.querySelector(`[id="${CSS.escape(id)}"]`) || c).scrollIntoView();
    this.track();
    this.fill();
  }
}

function edge(which) {
  const e = document.createElement("div");
  e.className = "edge edge-" + which;
  const msg = document.createElement("p");
  msg.className = "edge-msg";
  msg.setAttribute("role", "status");
  e.append(msg);
  return e;
}

function nearScreen(el) {
  const r = el.getBoundingClientRect();
  return r.top < innerHeight + NEAR && r.bottom > -NEAR;
}
