// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// The browse app's router: everything to do with which view is shown for
// which address, kept in one place.
//
// The app runs in whatever page was loaded first, over that page's own
// (static) view: it shows its views in #app, hiding the static view, and
// shows the static view again when history comes back to it.
//
// Each change of address is a Navigation. Only the newest may change
// anything: starting another aborts it (nav.signal), and from then on its
// render, reveal, title and address changes do nothing, and nav.current()
// is false, so a view still loading drops what it would have shown. A view
// is rendered out of sight and revealed once it's ready (or sooner, with
// nav.reveal, or a spinner if it's slow), so moving between views doesn't
// flash. What a view leaves running (a listing) it gives to nav.own, and
// that's stopped when the next navigation starts.
//
// Nothing else changes the address: links go through navigate, a view
// changes its own with nav.replace (a listing following its scroll), and
// nav.redirect (a search naming a page). Every history entry remembers its
// scroll position, so going back or forward lands where it was.

const isRoute = (hash) => /^#\/./.test(hash);

class Navigation {
  constructor(router, url) {
    this.router = router;
    this.url = url;
    this.controller = new AbortController();
    this.signal = this.controller.signal;
    this.rendered = false;
    this.revealed = false;
    this.done = false;
    this.afterShow = null;
    this.owned = [];
  }

  // current reports whether this is still the navigation being shown, for
  // the address it's for.
  current() {
    return !this.signal.aborted && this.router.nav === this && location.href === this.url;
  }

  // render puts the view in place (out of sight until it's revealed).
  render(html) {
    if (!this.current()) return false;
    this.router.app.innerHTML = html;
    this.rendered = true;
    return true;
  }

  // reveal shows the view, scrolled to where history left it, or where
  // the view asked to be (afterShow). Views that go on working once they
  // show something (search) call it early; otherwise it's called when the
  // view's done.
  reveal() {
    if (!this.current() || this.revealed) return;
    this.revealed = true;
    this.router.showApp();
    if (this.afterShow) this.afterShow();
    else scrollTo(0, history.state?.y ?? 0);
  }

  // scrollTo asks for fn to scroll the view once it's revealed.
  scrollTo(fn) {
    this.afterShow = fn;
  }

  setTitle(t) {
    if (this.current()) document.title = t;
  }

  // own takes something the view leaves running, to stop (its destroy) when
  // the next navigation starts.
  own(thing) {
    if (!thing) return thing;
    if (this.signal.aborted) thing.destroy?.();
    else this.owned.push(thing);
    return thing;
  }

  // replace changes this view's address in place (in the same history
  // entry), for a view that moves within itself, reporting whether it did.
  replace(url) {
    if (!this.current()) return false;
    const u = new URL(url, location.href).href;
    history.replaceState(history.state, "", u);
    this.url = u;
    return true;
  }

  // redirect shows another address in this one's place.
  redirect(url) {
    if (!this.current()) return;
    history.replaceState(history.state, "", new URL(url, location.href).href);
    this.router.sync();
  }

  stop() {
    this.controller.abort();
    for (const t of this.owned.splice(0)) t.destroy?.();
  }
}

export class Router {
  // app is the element the app's views go in. resolve(nav) shows the
  // view for nav.url (rendering it with nav.render), or throws; handles(url)
  // says whether the app can show an address at all (if not, it's loaded
  // as a page). onError(nav, e) shows an error. waiting is the spinner's
  // HTML.
  constructor({ app, resolve, handles, onError, waiting, slowAfter = 300 }) {
    this.app = app;
    this.resolve = resolve;
    this.handles = handles;
    this.onError = onError;
    this.waiting = waiting;
    this.slowAfter = slowAfter;
    this.root = document.documentElement;
    // The page that was loaded, whose own view is the static one.
    this.docURL = location.pathname + location.search;
    this.docTitle = document.title;
    this.nav = null;        // the newest navigation
    this.showing = "";      // the address shown, if it's the static view's
    this.started = false;
  }

  // start follows the address from now on, and shows it.
  start() {
    if (!this.started) {
      this.started = true;
      if ("scrollRestoration" in history) history.scrollRestoration = "manual";
      // popstate and hashchange can both fire for one change; sync sees
      // the second is the same address.
      addEventListener("popstate", () => this.sync());
      addEventListener("hashchange", () => this.sync());
      // Remember where each entry was scrolled to, for coming back to it.
      let timer = 0;
      addEventListener("scroll", () => {
        clearTimeout(timer);
        timer = setTimeout(() => this.remember(), 150);
      }, { passive: true });
      addEventListener("pagehide", () => this.remember());
    }
    return this.sync();
  }

  // showingStatic reports whether the static view is what's shown for
  // the current address (not just what's on the screen while a view of
  // the app's loads out of sight).
  showingStatic() {
    return !this.started || (!this.nav && this.showing === location.href);
  }

  // remember keeps the scroll position, and whether the static view is
  // what's shown, in the current history entry.
  remember() {
    history.replaceState({ ...(history.state || {}), y: scrollY, own: this.showingStatic() }, "");
  }

  // navigate goes to an address, in a new history entry.
  navigate(href) {
    const u = new URL(href, location.href).href;
    this.remember();
    // How many entries of the site's are behind this one, for going back
    // without leaving it.
    history.pushState({ depth: (history.state?.depth ?? 0) + 1 }, "", u);
    return this.started ? this.sync() : this.start();
  }

  // isStatic reports whether the address is shown by the page's own view:
  // it's the page loaded (and not a route), or history says it was.
  isStatic() {
    if (history.state?.own) return true;
    return location.pathname + location.search === this.docURL && !isRoute(location.hash);
  }

  // sync shows whatever the address is now: the static view, or a view of
  // the app's (unless it's already showing or on its way).
  async sync() {
    const url = location.href;
    const nav = this.nav;
    if (this.isStatic()) {
      if (this.showing === url && !nav) return;
      this.stopNav();
      this.showing = url;
      this.showStatic();
      return;
    }
    // The same address again: on its way, or shown (a link to the address
    // already shown is followed by showing it again, so it can't stay
    // stuck if anything went wrong).
    if (nav && nav.url === url && !nav.signal.aborted && (!nav.done || nav.revealed)) return;
    if (!this.handles(url)) {
      location.reload();
      return;
    }
    this.stopNav();
    this.showing = "";
    const next = new Navigation(this, url);
    this.nav = next;
    const slow = setTimeout(() => {
      if (!next.current() || next.rendered) return;
      this.app.innerHTML = this.waiting;
      this.showApp();
    }, this.slowAfter);
    try {
      await this.resolve(next);
    } catch (e) {
      if (next.current()) await this.onError(next, e);
    } finally {
      clearTimeout(slow);
    }
    next.done = true;
    next.reveal();
  }

  stopNav() {
    if (this.nav) this.nav.stop();
    this.nav = null;
  }

  // followStatic changes the static view's address in place (its listing
  // following its scroll), if the static view is still what's shown at
  // address from; it reports whether it did.
  followStatic(from, to) {
    if (location.href !== from || !this.showingStatic()) return false;
    history.replaceState(history.state, "", new URL(to, location.href).href);
    if (this.started) this.showing = location.href;
    return true;
  }

  showStatic() {
    this.root.classList.remove("routed");
    this.app.hidden = true;
    document.title = this.docTitle;
    if (history.state?.y != null) scrollTo(0, history.state.y);
  }

  showApp() {
    this.root.classList.add("routed");
    this.app.hidden = false;
  }
}
