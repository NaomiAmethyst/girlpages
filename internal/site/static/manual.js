// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The manual's conventions, as the Go side has them (internal/manpage,
// internal/web): keep them in step (site_test.go checks).

// chapter matches manpage.Chapter: the part of the manual a section belongs
// to (3 for 3ssl); a section not starting with a digit is its own.
export const chapter = (sec) => (/^[0-9]/.test(sec) ? sec[0] : sec);

// isSeries matches manpage.IsSeries: a series of documents (rfc), not a
// section of the manual.
export const isSeries = (sec) => sec.length > 1 && !/^[0-9]/.test(sec);

// letterOf is the group a name is listed under, as in site.letterOf: its
// first letter, or a number's thousand.
export function letterOf(n) {
  if (/^[0-9]+$/.test(n)) return +n < 1000 ? "1–999" : `${Math.floor(+n / 1000) * 1000}s`;
  return /^[a-z]/i.test(n) ? n[0].toUpperCase() : "#";
}

// nameCmp orders names as manpage.NameLess does: numbers first, by value,
// then the rest.
export function nameCmp(a, b) {
  const num = (x) => /^[0-9]+$/.test(x);
  const na = num(a), nb = num(b);
  if (na !== nb) return na ? -1 : 1;
  if (na) {
    const ta = a.replace(/^0+/, ""), tb = b.replace(/^0+/, "");
    if (ta.length !== tb.length) return ta.length - tb.length;
    if (ta !== tb) return ta < tb ? -1 : 1;
    return a.length - b.length;
  }
  return a < b ? -1 : a > b ? 1 : 0;
}

// SECTIONS names the manual's chapters, as web.SectionName does.
const SECTIONS = {
  "1": ["User commands", "💻"], "2": ["System calls", "📞"], "3": ["Library functions", "📚"],
  "4": ["Special files", "🔌"], "5": ["File formats & config", "📝"], "6": ["Games", "🎮"],
  "7": ["Miscellany", "🌈"], "8": ["System administration", "👑"], "9": ["Kernel routines", "🧠"],
  "n": ["Tcl/Tk", "🪶"], "l": ["Local", "🏡"], "o": ["Old", "🕰️"],
  "rfc": ["Requests for Comments", "🌐"],
};

// sectionName is a section's chapter's [name, emoji].
export const sectionName = (k) => SECTIONS[k] || SECTIONS[chapter(k)] || ["Section " + k, "📖"];

// sectionLabel names a section in breadcrumbs: "section 3ssl", or a
// series' name ("RFCs").
export const sectionLabel = (sec) => (isSeries(sec) ? sectionName(sec)[0] : `section ${sec}`);

// sourceLabel names a source ("debian trixie"), or everything ("").
export const sourceLabel = (key) => (key ? key.replace("/", " ") : "everything");

// plural counts pages.
export const plural = (n) => (n === 1 ? "1 page" : `${n} pages`);

// How the variants girlrepo has prompts for (llm.Variants) are offered;
// others (made with other prompts) get their name. As site.variantLabel.
const VARIANT_LABELS = {
  girl: "💅 girly", man: "📜 original", boy: "🎮 boy", femboy: "🥺 femboy", lolcat: "😺 lolcat", pirate: "🏴‍☠️ pirate",
  eli5: "🧸 eli5", woman: "☕ woman", witch: "🔮 witch", bofh: "🔥 bofh",
};

/** @param {string} v */
export function variantLabelOf(v) {
  return Object.hasOwn(VARIANT_LABELS, v) ? VARIANT_LABELS[/** @type {keyof typeof VARIANT_LABELS} */ (v)] : "✨ " + v;
}
