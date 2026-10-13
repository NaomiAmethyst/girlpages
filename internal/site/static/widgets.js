// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// Bits of pages: save buttons, and the sources' menus.

import { esc } from "./html.js";

// sourceNav is a manifest's sources as chips, as site.sourceChips makes
// them: everything first, then each distribution, with a menu of its
// releases if it has any. on is the source shown ("" for everything), and
// href(key) a source's link ("" for everything).
export function sourceNav(sources, { on = "", href, all = "✨ latest of everything" }) {
  const pill = (key, text) => `<a class="pill${key === on ? " on" : ""}" href="${esc(href(key))}">${esc(text)}</a>`;
  const groups = new Map();
  for (const s of sources || []) {
    if (!groups.has(s.distro)) groups.set(s.distro, { whole: false, releases: [] });
    const g = groups.get(s.distro);
    if (s.release) g.releases.push(s.release);
    else g.whole = true;
  }
  let out = pill("", all);
  for (const [distro, g] of groups) {
    if (!g.releases.length) {
      out += pill(distro, distro);
      continue;
    }
    const release = on.startsWith(distro + "/") ? on.slice(distro.length + 1) : "";
    const here = on === distro || g.releases.includes(release);
    out += `<details class="source"><summary class="pill${here ? " on" : ""}">${esc(release ? `${distro} · ${release}` : distro)}</summary><div class="menu">` +
      (g.whole ? pill(distro, `all of ${distro}`) : "") + g.releases.map(r => pill(`${distro}/${r}`, r)).join("") + "</div></details>";
  }
  return out;
}

// saveButton makes a button save a section for offline reading, showing how
// it's going and whether it's done.
export function saveButton(button, cache, key, src = "") {
  const part = key.startsWith("search:") ? key.slice(7) : "";
  const what = { words: "search", typos: "typo matching" }[part] || "";
  let size = "";
  const label = async () => {
    const s = cache.saved(key, src);
    const saved = what ? `✅ ${what} saved offline` : "✅ saved for offline";
    if (!s) return what ? `⬇️ ${what} offline${size}` : "⬇️ save for offline";
    let current = "";
    try {
      current = (await cache.get("manifest.json")).generated;
    } catch {
      return saved;
    }
    return s.generated === current ? saved : `🔄 update offline ${what || "copy"}`;
  };
  label().then(t => { button.textContent = t; });
  if (part) {
    cache.searchSize(part).then(n => {
      if (n) size = ` · ${megabytes(n)}`;
      return label();
    }).then(t => { if (!button.disabled) button.textContent = t; }).catch(() => {});
  }
  button.addEventListener("click", async () => {
    if (button.disabled) return;
    button.disabled = true;
    navigator.storage?.persist?.().catch(() => {});
    try {
      const progress = (done, total) => { button.textContent = `⏳ saving ${done}/${total}…`; };
      const { pages, failed } = part ? await cache.saveSearch(part, { progress }) : await cache.save(key, { src, progress });
      button.textContent = failed ? `💔 ${failed} of ${pages} didn't save · try again` : await label();
      button.dispatchEvent(new CustomEvent("saved", { bubbles: true }));
    } catch {
      button.textContent = "📵 can't save while offline";
    }
    button.disabled = false;
  });
}

function megabytes(n) {
  return n < 1e6 ? `${Math.max(1, Math.round(n / 1e3))} kB` : `${(n / 1e6).toFixed(n < 1e7 ? 1 : 0)} MB`;
}

// menus makes the page's menus behave: a source's menu stays on the screen
// (one under a chip near the edge is moved in), and an open menu (a
// source's, or the version switcher) closes when anything else is clicked,
// or a link in it is.
export function menus() {
  document.addEventListener("toggle", (e) => {
    const d = e.target;
    if (!d.matches?.("details.source") || !d.open) return;
    const menu = d.querySelector(".menu");
    menu.style.left = "";
    const r = menu.getBoundingClientRect(), edge = document.documentElement.clientWidth - 8;
    if (r.right > edge) menu.style.left = `${Math.max(8 - r.left, edge - r.right)}px`;
  }, true);
  document.addEventListener("click", (e) => {
    for (const d of document.querySelectorAll("details.versions[open], details.source[open]")) {
      if (!d.contains(e.target) || e.target.closest("a")) d.open = false;
    }
  });
}
