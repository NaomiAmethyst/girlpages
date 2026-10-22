// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// Keys, meant to come naturally to someone at home in a terminal (less,
// man, fzf):
//
//   /            search (the box at the top)
//   Esc          close a menu, leave the search box, or else go back (but
//                not off the site)
//   Ctrl+Enter   go to a page: by name, with suggestions as you type (↑↓ or
//                Tab to pick one)
//   ?            these keys
//
// On the home page (or a source's), typing a section (1, 3, rfc) goes to
// it, or picks the one it's the start of, for Enter. On a listing,
// typing jumps to the names that start that way, picking the first; ↑↓ pick the one before or after (or one on the screen, if
// none is), Enter goes to it, Esc lets it go. Anywhere else (a page,
// search results), it's less: j k a line, d u half a screen, Space b a
// screen, g G the top and bottom, q back.

import { html } from "./html.js";
import { pageURL } from "./urls.js";

// editing reports whether el takes typing.
const editing = (el) => !!el && (/** @type {HTMLElement} */ (el).isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName));

// How long a pause ends what's being typed to jump to.
const TYPED_FOR = 1000;

// A line, for j and k.
const LINE = 48;

// less's keys: how far each scrolls, as a function of the screen's height.
/** @type {Record<string, (h: number) => number>} */
const LESS = {
  j: () => LINE, k: () => -LINE,
  d: (h) => h / 2, u: (h) => -h / 2,
  f: (h) => h - LINE, b: (h) => -(h - LINE),
  g: () => -Infinity, G: () => Infinity,
};

// back goes back, if there's an entry of the site's to go back to.
const back = () => {
  if ((history.state?.depth ?? 0) <= 0) return false;
  history.back();
  return true;
};

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
  // The entry picked (highlighted), and the seek that's to pick one, if
  // typing's paused for less long than it waits (run it, for Enter not to
  // wait), and the one going on.
  /** @type {HTMLElement | null} */
  let picked = null;
  /** @type {(() => Promise<void>) | null} */
  let toSeek = null;
  let seekDone = Promise.resolve();

  /** @param {HTMLElement | null} li */
  const pick = (li) => {
    picked?.classList.remove("found");
    picked = li;
    li?.classList.add("found");
  };
  // pickedNow is the entry picked, if it's still on the screen.
  const pickedNow = () => {
    if (picked && !(picked.isConnected && picked.checkVisibility())) pick(null);
    return picked;
  };
  addEventListener("pointerdown", () => pick(null), { passive: true });

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
      } else if (pickedNow() || toSeek) {
        clearTimeout(seekTimer);
        toSeek = null;
        seeking++;
        pick(null);
      } else if (!back()) {
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
    if (e.key === "Enter" && !e.shiftKey && (pickedNow() || toSeek)) {
      e.preventDefault();
      follow();
      return;
    }
    if (e.key === "?") {
      e.preventDefault();
      help();
      return;
    }
    const listing = [...document.querySelectorAll(".listing")].find(l => l.checkVisibility());
    const sections = listing ? null : [...document.querySelectorAll(".sections")].find(s => s.checkVisibility());
    if (sections && e.key.length === 1 && toSection(sections, e)) return;
    if ((e.key === "ArrowDown" || e.key === "ArrowUp") && !e.shiftKey && listing) {
      e.preventDefault();
      if (pickedNow()) step(e.key === "ArrowDown" ? 1 : -1);
      else pick(onScreen(listing));
      return;
    }
    if (!listing && Object.hasOwn(LESS, e.key)) {
      e.preventDefault();
      const by = LESS[e.key](innerHeight);
      if (Math.abs(by) === Infinity) scrollTo({ top: by < 0 ? 0 : document.documentElement.scrollHeight, behavior: "instant" });
      else scrollBy({ top: by, behavior: "instant" });
      return;
    }
    if (!listing && e.key === "q") {
      if (back()) e.preventDefault();
      return;
    }
    if (e.key.length !== 1 || !/[\p{L}\p{N}._+:@-]/u.test(e.key)) return;
    if (!listing) return;
    e.preventDefault();
    typed = performance.now() - typedAt > TYPED_FOR ? e.key : typed + e.key;
    typedAt = performance.now();
    show(typed);
    // Once typing pauses, so each letter doesn't set off a jump of its own.
    clearTimeout(seekTimer);
    const prefix = typed;
    toSeek = () => {
      toSeek = null;
      return (seekDone = seek(listing, prefix));
    };
    seekTimer = setTimeout(() => toSeek?.(), 150);
  });

  // toSection handles a key typed on a page of sections: one that starts
  // a section's key (with what was typed just before) picks it, or if it's
  // the whole of the only one, goes there. Digits always do; letters only
  // if they start a section, so less's keys still scroll. It reports
  // whether it took the key.
  /** @param {Element} sections @param {KeyboardEvent} e */
  function toSection(sections, e) {
    const cards = [.../** @type {NodeListOf<HTMLAnchorElement>} */ (sections.querySelectorAll("a.section-card"))];
    const keyOf = (a) => (a.querySelector(".num")?.textContent || "").trim().toLowerCase();
    const k = e.key.toLowerCase();
    const next = performance.now() - typedAt > TYPED_FOR ? k : typed + k;
    let matches = cards.filter(a => keyOf(a).startsWith(next));
    let text = next;
    if (!matches.length && next !== k) {
      // Not carrying on: a fresh start.
      text = k;
      matches = cards.filter(a => keyOf(a).startsWith(k));
    }
    if (!matches.length) return false;
    e.preventDefault();
    typed = text;
    typedAt = performance.now();
    show(typed);
    const exact = matches.find(a => keyOf(a) === typed);
    if (exact && matches.length === 1) {
      pick(null);
      go(exact.href);
      return true;
    }
    pick(exact || matches[0]);
    (exact || matches[0]).scrollIntoView({ block: "nearest" });
    return true;
  }

  // follow goes to the entry picked, once what's been typed is sought.
  async function follow() {
    if (toSeek) {
      clearTimeout(seekTimer);
      toSeek();
    }
    await seekDone;
    const p = pickedNow();
    if (p?.matches("a")) p.click();
    else p?.querySelector("a")?.click();
  }

  // step picks the entry by (1 or -1) from the one picked, in what's on
  // the page (the listing loads more as it nears the end of it).
  /** @param {number} by */
  function step(by) {
    const listing = picked?.closest(".listing");
    if (!listing) return;
    const lis = [.../** @type {NodeListOf<HTMLElement>} */ (listing.querySelectorAll("li[data-name]"))];
    const next = lis[lis.indexOf(/** @type {HTMLElement} */ (picked)) + by];
    if (!next) return;
    pick(next);
    next.scrollIntoView({ block: "nearest" });
  }

  // onScreen is the first entry of listing's wholly on the screen (below
  // the bar at the top).
  /** @param {Element} listing */
  function onScreen(listing) {
    const lis = /** @type {NodeListOf<HTMLElement>} */ (listing.querySelectorAll("li[data-name]"));
    for (const li of lis) {
      const r = li.getBoundingClientRect();
      if (r.top >= 80 && r.bottom <= innerHeight) return li;
    }
    return null;
  }

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
      const li = await l.seek(prefix);
      if (mine !== seeking) return;
      if (!li && shown) shown.textContent = `↪ ${prefix} · nothing 🤷`;
      pick(li);
      return;
    }
    const p = prefix.toLowerCase();
    const li = [.../** @type {NodeListOf<HTMLElement>} */ (listing.querySelectorAll("li[data-name]"))]
      .find(x => (x.dataset.name || "").toLowerCase() >= p);
    li?.scrollIntoView({ block: "center" });
    pick(li || null);
  }
}

// help shows the keys.
function help() {
  /** @type {HTMLDialogElement | null} */
  let d = document.querySelector("dialog.keys");
  if (!d) {
    d = document.createElement("dialog");
    d.className = "keys card";
    const row = (k, what) => html`<tr><th>${k.map((x, i) => html`${i ? " " : ""}<kbd>${x}</kbd>`)}</th><td>${what}</td></tr>`;
    d.innerHTML = String(html`<form method="dialog"><h2>⌨️ keys</h2><table>
      ${row(["/"], "search")}
      ${row(["Ctrl+Enter"], "go to a page by name")}
      ${row(["Esc"], "close, or go back")}
      ${row(["?"], "these keys")}
      <tr><th colspan="2">on the home page</th></tr>
      ${row(["1", "3", "rfc"], "go to that section")}
      <tr><th colspan="2">on a listing</th></tr>
      ${row(["a–z"], "jump to the names that start that way")}
      ${row(["↑", "↓"], "pick the one before or after")}
      ${row(["Enter"], "go to the one picked")}
      <tr><th colspan="2">on a page (like less)</th></tr>
      ${row(["j", "k"], "a line down, up")}
      ${row(["d", "u"], "half a screen down, up")}
      ${row(["Space", "f", "b"], "a screen down, down, up")}
      ${row(["g", "G"], "the top, the bottom")}
      ${row(["q"], "back")}
      </table><p><button class="pill">💖 got it</button></p></form>`);
    document.body.append(d);
  }
  d.showModal();
}

// goTo opens the "go to page" box: a name (ls, open(2), 2 open), with
// suggestions from search as it's typed; Enter goes to the one picked, or
// the page named (open alone is the one man would show), or else searches
// for it.
/** @param {{go: (href: string) => void, base: string, repo: string}} opts */
async function goTo({ go, base, repo }) {
  /** @type {HTMLDialogElement | null} */
  let d = document.querySelector("dialog.goto");
  if (!d) {
    d = document.createElement("dialog");
    d.className = "goto card";
    d.innerHTML = String(html`<form method="dialog"><label>📖 go to page
      <input type="search" name="page" placeholder="ls, open(2), 2 open…" autocomplete="off" spellcheck="false"></label>
      <ul class="pages suggestions"></ul><p class="sub">↑↓ or Tab to pick · Enter to go · Esc to close</p></form>`);
    document.body.append(d);
    setUp(d, { go, base, repo });
  }
  d.showModal();
  const input = /** @type {HTMLInputElement} */ (d.querySelector("input"));
  input.select();
}

// named is the page called q (any case) that search ranks best, as man
// would pick (the earliest section), if there's one: for going to a page
// by its name alone, before suggestions come.
/** @param {any} sr @param {string} q @returns {Promise<{name: string, sec: string} | null>} */
async function named(sr, q) {
  const want = q.toLowerCase();
  for (const [id] of (await sr.rank(q)).slice(0, 20)) {
    const [name, sec] = await sr.name(id);
    if (name.toLowerCase() === want) return { name, sec };
  }
  return null;
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
    const by = e.key === "ArrowDown" || (e.key === "Tab" && !e.shiftKey) ? 1
      : e.key === "ArrowUp" || (e.key === "Tab" && e.shiftKey) ? -1 : 0;
    if (by && !e.ctrlKey && !e.altKey && !e.metaKey) {
      e.preventDefault();
      if (items.length) picked = (picked + by + items.length) % items.length;
      draw();
    }
  });
  d.querySelector("form")?.addEventListener("submit", async (e) => {
    e.preventDefault();
    const q = input.value.trim();
    if (!q) return d.close();
    if (items[picked]) return follow(items[picked].href);
    const { searcher, directPage } = await import("./search.js");
    const sr = searcher(repo);
    const page = await directPage(q, sr).catch(() => null) || await named(sr, q).catch(() => null);
    follow(page ? pageURL(base, page.name, page.sec) : `${base}search/?${new URLSearchParams({ q })}`);
  });
  list.addEventListener("click", () => d.close());
}
