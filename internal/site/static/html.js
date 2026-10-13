// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// Building HTML safely.

// esc matches Go's html.EscapeString.
export function esc(s) {
  return s.replace(/[&'<>"]/g, c => ({ "&": "&amp;", "'": "&#39;", "<": "&lt;", ">": "&gt;", '"': "&#34;" })[c]);
}
