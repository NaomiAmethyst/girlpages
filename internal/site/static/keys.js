// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// Keys, on every page:
//
//   /            search (the box at the top)
//   Esc          close a menu, leave the search box, or else go back (but
//                not off the site)
//   Ctrl+Enter   go to a page: by name, with suggestions as you type
//   typing       on a listing, jump to the names that start that way

import { html } from "./html.js";
import { pageURL } from "./urls.js";

// editing reports whether el takes typing.
const editing = (el) => !!el && (/** @type {HTMLElement} */ (el).isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName));

// How long a pause ends what's being typed to jump to.
const TYPED_FOR = 1000;

/**
 * keys sets the keys up: go(href) follows a link (through the app, if
 * it's running), and base and repo are where the site and its repository
 * are.
 * @param {{go: (href: string) => void, base: string, repo: string}} opts
 */
export function keys({ go, base, repo }) {
  let typed = "", typedAt = 0, seeking = 0, seekTimer = 0;
  /** @type {HTMLElement | null} */
  let shown = null;

  document.addEventListener("keydown", (e) => {
    if (e.defaultPrevented || e.isComposing) return;
    const field = editing(document.activeElement);
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
      e.preventDefault();
      goTo({ go, base, repo });
      return;
    }
    if (e.key === "Escape") {
      if (document.querySelector("dialog[open]")) return; // it closes itself
      const menus = document.querySelectorAll("details.source[open], details.versions[open]");
      if (menus.length) {
        for (const d of /** @type {NodeListOf<HTMLDetailsElement>} */ (menus)) d.open = false;
      } else if (field) {
        /** @type {HTMLElement} */ (document.activeElement).blur();
      } else if ((history.state?.depth ?? 0) > 0) {
        history.back();
      } else {
        return;
      }
      e.preventDefault();
      return;
    }
    if (field || e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.key === "/") {
      const box = /** @type {HTMLInputElement | null} */ (document.querySelector('.top input[name="q"]'));
      if (!box) return;
      e.preventDefault();
      box.focus();
      box.select();
      return;
    }
    if (e.key.length !== 1 || !/[\p{L}\p{N}._+:@-]/u.test(e.key)) return;
    const listing = [...document.querySelectorAll(".listing")].find(l => l.checkVisibility());
    if (!listing) return;
    e.preventDefault();
    typed = performance.now() - typedAt > TYPED_FOR ? e.key : typed + e.key;
    typedAt = performance.now();
    show(typed);
    // Once typing pauses, so each letter doesn't set off a jump of its own.
    clearTimeout(seekTimer);
    const prefix = typed;
    seekTimer = setTimeout(() => seek(listing, prefix), 150);
  });

  // show says what's been typed, for a moment.
  /** @param {string} text */
  function show(text) {
    if (!shown) {
      shown = document.createElement("div");
      shown.className = "typed";
      shown.setAttribute("role", "status");
      document.body.append(shown);
    }
    shown.textContent = `↪ ${text}`;
    shown.hidden = false;
    const at = typedAt;
    setTimeout(() => { if (shown && typedAt === at) shown.hidden = true; }, TYPED_FOR + 500);
  }

  // seek goes to the first name starting with prefix in a listing: by its
  // Listing, which has the whole section, or else in what's on the page.
  /** @param {Element} listing @param {string} prefix */
  async function seek(listing, prefix) {
    const mine = ++seeking;
    const { shownListing } = await import("./listing.js");
    if (mine !== seeking) return; // typed on meanwhile
    const l = shownListing();
    if (l) {
      if (!(await l.seek(prefix)) && shown) shown.textContent = `↪ ${prefix} · nothing 🤷`;
      return;
    }
    const p = prefix.toLowerCase();
    const li = [.../** @type {NodeListOf<HTMLElement>} */ (listing.querySelectorAll("li[data-name]"))]
      .find(x => (x.dataset.name || "").toLowerCase() >= p);
    if (!li) return;
    li.scrollIntoView({ block: "center" });
    li.classList.add("found");
    setTimeout(() => li.classList.remove("found"), 1500);
  }
}

// goTo opens the "go to page" box: a name (ls, open(2), 2 open), with
// suggestions from search as it's typed; Enter goes to the one picked, or
// the page named, or else searches for it.
/** @param {{go: (href: string) => void, base: string, repo: string}} opts */
async function goTo({ go, base, repo }) {
  /** @type {HTMLDialogElement | null} */
  let d = document.querySelector("dialog.goto");
  if (!d) {
    d = document.createElement("dialog");
    d.className = "goto card";
    d.innerHTML = String(html`<form method="dialog"><label>📖 go to page
      <input type="search" name="page" placeholder="ls, open(2), 2 open…" autocomplete="off" spellcheck="false"></label>
      <ul class="pages suggestions"></ul><p class="sub">↑↓ to pick · Enter to go · Esc to close</p></form>`);
    document.body.append(d);
    setUp(d, { go, base, repo });
  }
  d.showModal();
  const input = /** @type {HTMLInputElement} */ (d.querySelector("input"));
  input.select();
}

/** @param {HTMLDialogElement} d @param {{go: (href: string) => void, base: string, repo: string}} opts */
function setUp(d, { go, base, repo }) {
  const input = /** @type {HTMLInputElement} */ (d.querySelector("input"));
  const list = /** @type {HTMLElement} */ (d.querySelector(".suggestions"));
  /** @type {{href: string, label: string, desc: string}[]} */
  let items = [];
  let picked = 0, asked = 0;
  const draw = () => {
    list.innerHTML = String(html`${items.map((it, i) => html`<li class="${i === picked ? "picked" : ""}"><a href="${it.href}">
      <span class="name">${it.label}</span></a><span class="desc">${it.desc}</span></li>`)}`);
  };
  const follow = (href) => {
    d.close();
    go(href);
  };
  const suggest = async () => {
    const q = input.value.trim();
    const mine = ++asked;
    if (!q) {
      items = [];
      draw();
      return;
    }
    const { searcher, directPage } = await import("./search.js");
    const s = searcher(repo);
    try {
      const [direct, ranked] = await Promise.all([directPage(q, s).catch(() => null), s.rank(q)]);
      /** @type {{href: string, label: string, desc: string}[]} */
      const out = [];
      if (direct) out.push({ href: pageURL(base, direct.name, direct.sec), label: `${direct.name}(${direct.sec})`, desc: "" });
      for (const [id] of ranked.slice(0, 8)) {
        const [name, sec] = await s.name(id);
        const href = pageURL(base, name, sec);
        if (out.some(x => x.href === href)) continue;
        const [desc] = await s.docOf(id);
        out.push({ href, label: `${name}(${sec})`, desc });
      }
      if (mine !== asked) return; // typed on meanwhile
      items = out.slice(0, 8);
      picked = 0;
      draw();
    } catch {
      // offline, say: Enter still searches
    }
  };
  let timer = 0;
  input.addEventListener("input", () => {
    // What was suggested was for what was there before.
    items = [];
    asked++;
    draw();
    clearTimeout(timer);
    timer = setTimeout(suggest, 120);
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (items.length) picked = (picked + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length;
      draw();
    }
  });
  d.querySelector("form")?.addEventListener("submit", async (e) => {
    e.preventDefault();
    const q = input.value.trim();
    if (!q) return d.close();
    if (items[picked]) return follow(items[picked].href);
    const { searcher, directPage } = await import("./search.js");
    const direct = await directPage(q, searcher(repo)).catch(() => null);
    follow(direct ? pageURL(base, direct.name, direct.sec) : `${base}search/?${new URLSearchParams({ q })}`);
  });
  list.addEventListener("click", () => d.close());
}
