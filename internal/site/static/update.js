// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// Noticing the site's been republished under a page that's open. The
// scripts a page runs come from the service worker's shell, so the first
// page loaded after the site's republished still runs the old ones, until
// the new service worker (which comes with the new shell) takes over. From
// then on, the page is out of date: the next page is loaded whole, rather
// than shown in place by old scripts (which would load any they hadn't yet
// from the new shell, mixing the two).

let stale = false;

// outdated reports whether a newer version of the site has taken over
// since this page was loaded.
export const outdated = () => stale;

// watchUpdates watches for a service worker taking over from the one this
// page was loaded with (not the first one, as the page was loaded from the
// network then).
export function watchUpdates() {
  const sw = navigator.serviceWorker;
  if (!sw) return;
  let had = !!sw.controller;
  sw.addEventListener("controllerchange", () => {
    if (had) stale = true;
    had = true;
  });
}
