// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// The reader's preferences, kept in localStorage (where there is one).

const VARIANT = "girlpages:variant";

// preferredVariant is the variant the reader would rather read pages in
// ("" for girlpages' own order: girly, then others, then the original).
export function preferredVariant() {
  try {
    return globalThis.localStorage?.getItem(VARIANT) || "";
  } catch {
    return "";
  }
}

// setPreferredVariant makes a variant the reader's default ("" for none).
/** @param {string} v */
export function setPreferredVariant(v) {
  try {
    if (v) globalThis.localStorage?.setItem(VARIANT, v);
    else globalThis.localStorage?.removeItem(VARIANT);
  } catch {
    // (it lasts as long as nothing: there's nowhere to keep it)
  }
}
