#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
"""End-to-end checks of the website's navigation, in a real browser.

Builds a small site (generated man pages: enough for listings of several
pages and shards, a second section, and a subsection), serves it, and walks
it in headless Chromium: links between the static pages and the app, back
and forward, scroll positions, listings following and taking over, letter
jumps, quick taps, taps while scrolling, and search going straight to a
page. Exits non-zero if any check fails; skips (exit 0) without Playwright
or man.

    test/e2e/navigation.py [path/to/girlrepo]
"""

import asyncio
import os
import random
import shutil
import socket
import re
import subprocess
import sys
import tempfile
import threading
import time
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

try:
    from playwright.async_api import async_playwright
except ImportError:
    print("skipping: no playwright (pip install playwright)")
    sys.exit(0)
if not shutil.which("man"):
    print("skipping: no man")
    sys.exit(0)

GIRLREPO = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else "./girlrepo")
failures = []


def check(ok, what):
    print(("  ok   " if ok else "  FAIL ") + what)
    if not ok:
        failures.append(what)


def page_roff(name, sec, desc):
    words = " ".join(random.choice(["list", "files", "bestie", "options", "print", "data", "girly", "slay"]) for _ in range(30))
    return (f'.TH {name.upper()} {sec} 2026-01-01 test "Test Manual"\n.SH NAME\n{name} \\- {desc}\n'
            f".SH DESCRIPTION\n{words}\n.SH OPTIONS\n.TP\n.B \\-v\nbe loud\n")


NAMES = {}  # section → names generated


def build_site(tmp):
    random.seed(1)
    man = os.path.join(tmp, "man")
    pages = []
    # Section 1: every letter, some long, for several listing pages and shards.
    for i in range(1400):
        letter = "abcdefghijklmnopqrstuvwxyz"[min(25, int(random.expovariate(0.15)))]
        pages.append((f"{letter}tool{i:04d}", "1"))
    pages += [(f"admin{i:03d}", "8") for i in range(60)]
    pages += [(f"SSL_thing{i:03d}", "1ssl") for i in range(40)]
    for name, sec in pages:
        NAMES.setdefault(sec, []).append(name)
        d = os.path.join(man, "man" + sec[0])
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, f"{name}.{sec}"), "w") as f:
            f.write(page_roff(name, sec, f"does {name} things"))
    # A long page, for its contents to follow the reading (last in
    # section 8, so the others are where they were).
    long = '.TH ZLONGREAD 8 2026-01-01 test "Test Manual"\n.SH NAME\nzlongread \\- a long read\n'
    for k in range(12):
        long += f".SH PART{k:02d}\n" + " ".join(["words of part", str(k)] * 400) + "\n"
    with open(os.path.join(man, "man8", "zlongread.8"), "w") as f:
        f.write(long)
    site = os.path.join(tmp, "site")
    run = lambda *a: subprocess.run([GIRLREPO, "-d", site, *a], check=True, capture_output=True)
    run("ingest", man)
    # Two variants of one page (and its original), for choosing a default.
    girlify(tmp, site, ["admin050(8)"])
    run("publish", "--precompress", "none")
    return site


class FakeModel(BaseHTTPRequestHandler):
    """An OpenAI-compatible chat API that "rewrites" a page: in the voice
    its system prompt asks for, keeping its options."""

    def do_POST(self):
        req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        system = next((m["content"] for m in req["messages"] if m["role"] == "system"), "")
        user = next((m["content"] for m in req["messages"] if m["role"] == "user"), "")
        name = re.search(r"Rewrite the manual page (\S+)", user).group(1).split("(")[0].lower()
        voice = "arr matey, pirate" if "pirate captain" in system else "slay bestie, girly"
        opts = " ".join(sorted(set(re.findall(r"(?<![\w-])-[a-z]\b", user))))
        reply = f"# NAME\n\n{voice} {name} {opts}\n"
        body = json.dumps({"choices": [{"message": {"role": "assistant", "content": reply}, "finish_reason": "stop"}],
                           "model": "fake"}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


def girlify(tmp, site, names):
    """Girlifies pages, and makes their pirate variants, with FakeModel."""
    srv = ThreadingHTTPServer(("127.0.0.1", 0), FakeModel)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    conf = os.path.join(tmp, "config.toml")
    with open(conf, "w") as f:
        f.write(f'backend = "openai"\nbatch = "none"\n[openai]\nbase_url = "http://127.0.0.1:{srv.server_port}/v1"\n'
                'model = "fake"\napi_key = "x"\n')
    env = dict(os.environ, GIRLPAGES_CONFIG=conf, XDG_CACHE_HOME=os.path.join(tmp, "cache"))
    for variant in ["girl", "pirate"]:
        subprocess.run([GIRLREPO, "-d", site, "girlify", "-q", "--no-cache", "--variant", variant, *names],
                       check=True, capture_output=True, env=env)
    srv.shutdown()


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


SHOWN = """() => { const routed = document.documentElement.classList.contains('routed');
  const app = document.querySelector('#app'), sv = document.querySelector('.static-view');
  const h = (routed && app && !app.hidden ? app : sv)?.querySelector('h1');
  return [routed ? 'app' : 'static', h ? h.textContent.trim() : ''] }"""


async def shown(page):
    await page.wait_for_timeout(700)
    return await page.evaluate(SHOWN)


async def tap(page, href):
    """Follows a link to href, as a reader would."""
    await page.evaluate("""(h) => { const a = document.createElement('a'); a.href = h;
        document.body.append(a); a.click(); a.remove(); }""", href)


async def main():
    tmp = tempfile.mkdtemp(prefix="girlpages-e2e-")
    server = None
    try:
        print("building a test site…")
        site = build_site(tmp)
        port = free_port()
        base = f"http://127.0.0.1:{port}"
        server = subprocess.Popen([GIRLREPO, "-d", site, "serve", "--listen", f"127.0.0.1:{port}"],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for _ in range(50):
            try:
                socket.create_connection(("127.0.0.1", port), timeout=0.2).close()
                break
            except OSError:
                time.sleep(0.1)
        async with async_playwright() as p:
            browser = await p.chromium.launch()

            errors = []

            def watch(page):
                page.on("pageerror", lambda e: errors.append(f"{page.url}: {e}"))
                page.on("console", lambda m: m.type == "error" and errors.append(f"{page.url}: console: {m.text}"))

            async def fresh(url, width=390):
                ctx = await browser.new_context(viewport={"width": width, "height": 800}, is_mobile=width < 600, has_touch=width < 600)
                page = await ctx.new_page()
                watch(page)
                await page.goto(base + url, wait_until="networkidle")
                await page.wait_for_timeout(1500)  # the app loads in idle time
                return ctx, page

            path = lambda page: page.url.replace(base, "")

            print("back and forward between the static page and the app")
            ctx, page = await fresh("/section/1/")
            steps = []
            for href in ["/girl/1/atool0000/", "/", "/section/8/", "/girl/8/admin001/"]:
                await tap(page, base + href)
                steps.append((href, await shown(page)))
            for href, s in steps:
                check(s[0] == "app", f"{href} shown by the app")
            want = ["/section/8/", "/", "/girl/1/atool0000/", "/section/1/"]
            for w in want:
                await page.go_back()
                s = await shown(page)
                check(path(page) == w and (s[0] == "static") == (w == "/section/1/"), f"back to {w}: {path(page)} {s}")
            for w in reversed(["/girl/8/admin001/", "/section/8/", "/", "/girl/1/atool0000/"]):
                await page.go_forward()
                s = await shown(page)
                check(path(page) == w and s[0] == "app", f"forward to {w}: {path(page)} {s}")
            await ctx.close()

            print("going back lands where the view was scrolled to")
            ctx, page = await fresh("/girl/1/atool0000/")
            await tap(page, base + "/section/8/")
            await shown(page)
            await page.mouse.wheel(0, 900)
            await page.wait_for_timeout(500)
            y = await page.evaluate("scrollY")
            await tap(page, base + "/girl/8/admin002/")
            await shown(page)
            check(await page.evaluate("scrollY") < 50, "a new view starts at the top")
            await page.go_back()
            await shown(page)
            y2 = await page.evaluate("scrollY")
            check(abs(y2 - y) < 30, f"back to the listing at {y2}, scrolled to {y} before")
            await ctx.close()

            print("quick taps: the last one wins")
            ctx, page = await fresh("/girl/1/atool0000/")
            for href in ["/section/8/", "/section/1/1ssl/", "/", "/section/1/", "/section/8/"]:
                await tap(page, base + href)
                await page.wait_for_timeout(60)
            s = await shown(page)
            check(path(page) == "/section/8/" and "section 8" in s[1], f"after quick taps: {path(page)} {s}")
            # The same link again, while it's shown, keeps it shown.
            await tap(page, base + "/section/8/")
            s = await shown(page)
            check(path(page) == "/section/8/" and s[0] == "app" and "section 8" in s[1], f"tapping it again: {s}")
            await ctx.close()

            print("tapping while the static listing scrolls, with the repository slow")
            ctx, page = await fresh("/section/1/")
            await page.mouse.wheel(0, 4000)
            await page.wait_for_timeout(800)

            async def slow(route):
                await asyncio.sleep(0.7)
                await route.continue_()
            await page.route("**/repo/**", slow)
            target = next(n for n in NAMES["1"] if n.startswith("b"))
            await tap(page, base + f"/girl/1/{target}/")
            for _ in range(5):
                await page.mouse.wheel(0, 2500)
                await page.wait_for_timeout(60)
            await page.wait_for_timeout(2500)
            s = await shown(page)
            check(path(page) == f"/girl/1/{target}/" and s[0] == "app" and target.upper() in s[1], f"landed on the page: {path(page)} {s}")
            await ctx.close()

            print("listings follow the address as they scroll")
            ctx, page = await fresh("/section/1/")
            for _ in range(12):
                await page.mouse.wheel(0, 2500)
                await page.wait_for_timeout(200)
            check("/section/1/page/" in path(page), f"the static listing followed: {path(page)}")
            await tap(page, base + "/section/8/")
            await shown(page)
            await tap(page, base + "/section/1/")
            s = await shown(page)
            check(s[0] == "static", "the page's own listing is the static one again")
            await ctx.close()
            ctx, page = await fresh("/girl/1/atool0000/")
            await tap(page, base + "/#/section/1")
            await shown(page)
            for _ in range(12):
                await page.mouse.wheel(0, 2500)
                await page.wait_for_timeout(200)
            check("/section/1/page/" in path(page) or path(page) == "/#/section/1", f"the app's listing scrolled: {path(page)}")
            await ctx.close()

            print("a static listing page taken over keeps its first entry where it was")
            ctx = await browser.new_context(viewport={"width": 1280, "height": 900})
            page = await ctx.new_page()
            watch(page)
            hold = asyncio.Event()

            async def held(route):
                await hold.wait()
                await route.continue_()
            await page.route("**/repo/**", held)
            await page.goto(base + "/section/1/page/3/", wait_until="domcontentloaded")
            await page.wait_for_timeout(500)
            li = page.locator(".static-view .listing li[data-name]").first
            name = await li.get_attribute("data-name")
            y0 = await li.evaluate("e => e.getBoundingClientRect().top")
            hold.set()
            await page.wait_for_function("document.querySelector('.pager')?.hidden", timeout=20000)
            await page.wait_for_timeout(1000)
            y1 = await page.locator(f'.static-view .listing li[data-name="{name}"]').first.evaluate("e => e.getBoundingClientRect().top")
            check(abs(y1 - y0) < 2, f"{name} stayed at {round(y0)} (now {round(y1)})")
            check(path(page) == "/section/1/page/3/", f"the address stayed until scrolled: {path(page)}")
            await ctx.close()

            print("letters jump")
            ctx, page = await fresh("/girl/1/atool0000/")
            await tap(page, base + "/section/1/")
            await shown(page)
            await page.locator("#app .letters a", has_text="M").first.click()
            await page.wait_for_timeout(1200)
            top = await page.evaluate("""() => { const h = document.querySelector('#app [id="letter-M"]');
                return h ? Math.round(h.getBoundingClientRect().top) : null }""")
            check(top is not None and 0 <= top < 200, f"M's heading is at the top ({top})")
            await ctx.close()

            print("search goes straight to a page it names, and back comes back")
            ctx, page = await fresh("/girl/8/admin003/")
            await tap(page, base + "/search/?q=" + "1+atool0000")
            await page.wait_for_timeout(1500)
            s = await shown(page)
            check(path(page) == "/girl/1/atool0000/" and "ATOOL0000" in s[1], f"straight to the page: {path(page)} {s}")
            await page.go_back()
            s = await shown(page)
            check(path(page) == "/girl/8/admin003/" and s[0] == "static", f"back past the search: {path(page)} {s}")
            await ctx.close()

            print("keys: /, Esc, typing to jump, Ctrl+Enter")
            ctx, page = await fresh("/section/1/", width=1100)
            await page.keyboard.press("/")
            check(await page.evaluate("document.activeElement?.name") == "q", "/ goes to the search box")
            await page.keyboard.press("Escape")
            check(await page.evaluate("document.activeElement === document.body"), "Esc leaves it")
            target = sorted(n for n in NAMES["1"] if n.startswith("m"))[0]
            await page.keyboard.type(target[:5], delay=40)
            await page.wait_for_timeout(700)
            found = await page.evaluate("""() => { const f = document.querySelector('li.found');
                return f && [f.dataset.name, Math.round(f.getBoundingClientRect().top)] }""")
            check(found is not None and found[0] == target and 0 <= found[1] < 400, f"typing {target[:5]!r} jumps to {target}: {found}")
            picked = "() => document.querySelector('li.found')?.dataset.name"
            await page.keyboard.press("ArrowDown")
            await page.keyboard.press("ArrowDown")
            await page.keyboard.press("ArrowUp")
            after = await page.evaluate("""() => { const f = document.querySelector('li.found');
                return f && f.previousElementSibling?.dataset.name }""")
            check(after == target, f"↓↓↑ picks the one after {target}: after {after}")
            nxt = await page.evaluate(picked)
            await page.keyboard.press("Enter")
            s = await shown(page)
            check(path(page) == f"/girl/1/{nxt}/", f"Enter goes to the one picked, {nxt}: {path(page)} {s}")
            await page.go_back()
            await shown(page)
            await page.wait_for_timeout(300)
            check(await page.evaluate(picked) is None, "nothing's picked coming back")
            here = path(page)
            await page.keyboard.press("ArrowDown")
            top = await page.evaluate("""() => { const f = document.querySelector('li.found');
                return f && Math.round(f.getBoundingClientRect().top) }""")
            check(top is not None and 0 <= top < 850, f"↓ with nothing picked picks one on the screen: {top}")
            await page.keyboard.press("Escape")
            await page.wait_for_timeout(300)
            check(await page.evaluate(picked) is None and path(page) == here, f"Esc lets it go, and stays: {path(page)}")
            await page.keyboard.type(target[:5], delay=20)
            await page.keyboard.press("Enter")
            s = await shown(page)
            check(path(page) == f"/girl/1/{target}/", f"Enter right after typing goes to {target}: {path(page)} {s}")
            await page.go_back()
            await shown(page)
            await page.wait_for_timeout(300)
            await tap(page, base + "/section/8/")
            await shown(page)
            await page.keyboard.press("Escape")
            s = await shown(page)
            check(s[0] == "static" and path(page).startswith("/section/1/"), f"Esc goes back: {path(page)} {s}")
            await page.keyboard.press("Control+Enter")
            await page.wait_for_timeout(200)
            check(await page.evaluate("!!document.querySelector('dialog.goto[open]')"), "Ctrl+Enter opens go to page")
            await page.keyboard.type("admin003(8)")
            await page.keyboard.press("Enter")
            s = await shown(page)
            check(path(page) == "/girl/8/admin003/" and "ADMIN003" in s[1], f"and goes there: {path(page)} {s}")
            bare = sorted(NAMES["8"])[4]
            await page.keyboard.press("Control+Enter")
            await page.wait_for_timeout(200)
            await page.keyboard.type(bare)
            await page.keyboard.press("Enter")
            await shown(page)
            check(path(page) == f"/girl/8/{bare}/", f"a name alone goes to its page, {bare}: {path(page)}")
            await page.keyboard.press("Control+Enter")
            await page.wait_for_timeout(200)
            await page.keyboard.type("admin00")
            await page.wait_for_timeout(800)
            await page.keyboard.press("Tab")
            await page.keyboard.press("Tab")
            want = await page.evaluate("document.querySelector('dialog.goto li.picked a')?.getAttribute('href')")
            await page.keyboard.press("Enter")
            await shown(page)
            check(want is not None and path(page) == want, f"Tab picks a suggestion: {want} {path(page)}")
            await page.set_viewport_size({"width": 1100, "height": 300})
            await page.wait_for_timeout(300)
            room = await page.evaluate("document.documentElement.scrollHeight - innerHeight")
            await page.keyboard.press("G")
            await page.wait_for_timeout(100)
            bottom = await page.evaluate("scrollY")
            await page.keyboard.press("g")
            await page.keyboard.press("j")
            await page.wait_for_timeout(100)
            j = await page.evaluate("scrollY")
            check(room > 100 and abs(bottom - room) < 2 and j == 48, f"less keys on a page: G to {bottom} of {room}, g then j to {j}")
            await page.keyboard.press("?")
            await page.wait_for_timeout(200)
            check(await page.evaluate("!!document.querySelector('dialog.keys[open]')"), "? shows the keys")
            await page.keyboard.press("Escape")
            await page.wait_for_timeout(200)
            before = path(page)
            await page.keyboard.press("q")
            await shown(page)
            check(path(page) != before and path(page) == f"/girl/8/{bare}/", f"q goes back: {before} → {path(page)}")
            await ctx.close()

            print("typing a section on the home page goes to it")
            ctx, page = await fresh("/", width=1100)
            await page.keyboard.type("8", delay=40)
            await shown(page)
            check(path(page) == "/section/8/", f"8 goes to section 8: {path(page)}")
            await ctx.close()

            print("search by parts of words")
            ctx, page = await fresh("/girl/8/admin003/")
            part = target = sorted(NAMES["8"])[5]
            part = target[2:]
            await tap(page, base + "/search/?" + f"q={part}&mode=ngrams")
            await page.wait_for_timeout(1500)
            names = await page.evaluate("[...document.querySelectorAll('#app #search-results li .name')].map(x => x.textContent)")
            check(target in names, f"{part!r} finds {target} in parts of words: {names[:5]}")
            on = await page.evaluate("document.querySelector('#app #search-mode a.on')?.textContent")
            check(on is not None and "parts" in on, f"the mode's shown: {on}")
            await ctx.close()

            print("a newer version taking over loads the next page whole")
            ctx, page = await fresh("/section/8/", width=1100)
            await page.wait_for_function("!!navigator.serviceWorker.controller", timeout=10000)
            await page.reload(wait_until="networkidle")  # loaded under the service worker
            await page.wait_for_timeout(1500)
            await page.evaluate("window.marked = 1")
            await tap(page, base + "/girl/8/admin001/")
            s = await shown(page)
            check(await page.evaluate("window.marked") == 1 and s[0] == "app", f"before, links show in place: {s}")
            # Republishing changes sw.js (its VERSION).
            sw = os.path.join(site, "sw.js")
            with open(sw) as f:
                text = f.read()
            text, n = re.subn(r'const VERSION = "[^"]*"', 'const VERSION = "e2e-newer"', text)
            check(n == 1, "found sw.js's VERSION")
            with open(sw, "w") as f:
                f.write(text)
            took = await page.evaluate("""() => new Promise(done => {
                navigator.serviceWorker.addEventListener("controllerchange", () => done(true));
                navigator.serviceWorker.getRegistration().then(r => r.update());
                setTimeout(() => done(false), 10000);
            })""")
            check(took, "the newer service worker took over")
            async with page.expect_navigation():
                await tap(page, base + "/girl/8/admin002/")
            await page.wait_for_load_state("networkidle")
            s = await shown(page)
            check(await page.evaluate("window.marked") is None and path(page) == "/girl/8/admin002/" and s[0] == "static",
                  f"after, the next link loads whole: {path(page)} {s}")
            await page.evaluate("window.marked = 1")
            await page.keyboard.press("Escape")
            await page.wait_for_timeout(1000)
            await page.wait_for_load_state("networkidle")
            s = await shown(page)
            check(path(page) == "/girl/8/admin001/" and "ADMIN001" in s[1], f"and back still works: {path(page)} {s}")
            await ctx.close()

            print("the contents follow the reading")
            here = "(root) => document.querySelector(root + ' .toc a.here')?.textContent.trim()"
            # Scrolled to 120px down, again as the page draws in around it
            # (or as near as the end of the page lets it come).
            place = """async ([root, id]) => {
                const h = document.querySelector(root + ' [id="' + id + '"]');
                for (let i = 0; i < 10; i++) {
                    const y = scrollY;
                    scrollBy(0, h.getBoundingClientRect().top - 120);
                    await new Promise(r => setTimeout(r, 100));
                    if (Math.abs(h.getBoundingClientRect().top - 120) < 2 || scrollY === y) break;
                }
            }"""
            ctx, page = await fresh("/girl/8/zlongread/", width=1200)
            for k in (3, 7, 11):
                await page.evaluate(place, [".static-view", f"part{k:02d}"])
                await page.wait_for_timeout(300)
                got = await page.evaluate(here, ".static-view")
                check(got == f"PART{k:02d}", f"reading PART{k:02d}, it's marked: {got}")
            await ctx.close()
            # In the app, over a static page with a heading or two of the
            # same names.
            ctx, page = await fresh("/girl/8/admin001/", width=1200)
            await tap(page, base + "/girl/8/zlongread/")
            s = await shown(page)
            check(s[0] == "app", f"zlongread in the app: {s}")
            await page.evaluate(place, ["#app", "part05"])
            await page.wait_for_timeout(300)
            got = await page.evaluate(here, "#app")
            check(got == "PART05", f"and in the app: {got}")
            await ctx.close()

            print("a link in another case than its page's")
            ctx, page = await fresh("/girl/8/admin001/", width=1200)
            for want, href in [("admin002", "/girl/8/ADMIN002/"), ("SSL_thing001", "/girl/1ssl/ssl_thing001/")]:
                await tap(page, base + href)
                s = await shown(page)
                check(s[0] == "app" and want.upper() in s[1].upper() and page.url.endswith(f"/{want}/"),
                      f"{href} shows {want}: {s} at {page.url}")
            await ctx.close()
            # Looking the name up as written first is a 404, which is expected.
            errors[:] = [e for e in errors if not ("404" in e and ("/ADMIN002/" in e or "/ssl_thing001/" in e))]

            print("variants at their own addresses, and choosing a default")
            content = lambda pg: pg.evaluate("""() => { const routed = document.documentElement.classList.contains('routed');
              const app = document.querySelector('#app'), sv = document.querySelector('.static-view');
              const v = (routed && app && !app.hidden ? app : sv); return v ? v.querySelector('.content, .original')?.textContent || '' : '' }""")
            ctx, page = await fresh("/girl/8/admin050/", width=1200)
            check("girly admin050" in await content(page), f"girly at girl/: {(await content(page))[:60]!r}")
            row = page.locator(".static-view .meta[data-app-only]")
            check(await row.count() == 1 and await row.is_visible(), "the static page offers its variants, with the app")
            nf = await page.evaluate("[...document.querySelectorAll('.static-view .meta[data-app-only] a')].every(a => a.rel === 'nofollow')")
            check(nf, "their links are nofollow")
            await page.click("a.pill:has-text('pirate')")
            await shown(page)
            check("pirate admin050" in await content(page) and page.url.endswith("/pirate/8/admin050/"),
                  f"the pirate variant, at pirate/: {page.url}")
            await page.click("[data-prefer]")
            await page.wait_for_timeout(200)
            pref = await page.evaluate("localStorage.getItem('girlpages:variant')")
            check(pref == "pirate" and page.url.endswith("/pirate/8/admin050/"), f"made the default, in place: {pref!r} {page.url}")
            # Opening a girly page now goes to the pirate one, before anything girly shows.
            seen = []
            page.on("framenavigated", lambda f: f == page.main_frame and seen.append(f.url))
            await page.goto(base + "/girl/8/admin050/", wait_until="networkidle")
            await page.wait_for_timeout(1500)
            s = await shown(page)
            check(s[0] == "app" and "pirate admin050" in await content(page) and page.url.endswith("/pirate/8/admin050/"),
                  f"girl/ in pirate: {s} {page.url}")
            # A page without that variant shows what it has.
            await tap(page, base + "/girl/8/admin051/")
            s = await shown(page)
            check(s[0] == "app" and "admin051" in await content(page) and page.url.endswith("/pirate/8/admin051/"),
                  f"a page without pirate: {s} {page.url}")
            # Choosing girly on a page is girly, default or not.
            await tap(page, base + "/girl/8/admin050/")
            await shown(page)
            await page.click("a.pill[data-variant='girl']")
            await shown(page)
            check("girly admin050" in await content(page) and page.url.endswith("/girl/8/admin050/"), f"girly, chosen: {page.url}")
            # Undone: girl/ is girly again.
            await page.click("a.pill[data-variant='pirate']")
            await shown(page)
            await page.click("[data-prefer].on")
            await page.wait_for_timeout(200)
            pref = await page.evaluate("localStorage.getItem('girlpages:variant')")
            await page.goto(base + "/girl/8/admin050/", wait_until="networkidle")
            await page.wait_for_timeout(1000)
            check(pref is None and "girly admin050" in await content(page) and page.url.endswith("/girl/8/admin050/"),
                  f"back to girly: {pref!r} {page.url}")
            await ctx.close()

            print("a variant's address is the app's from the start")
            ctx, page = await fresh("/pirate/8/admin050/", width=1200)
            s = await shown(page)
            check(s[0] == "app" and "pirate admin050" in await content(page), f"pirate/: {s}")
            check(await page.evaluate("document.documentElement.dataset.shell !== undefined"), "it's the shell")
            check(await page.evaluate("!document.querySelector('.static-view .content')"), "with no girly page in it")
            home = await page.evaluate("document.querySelector('header a[href]').getAttribute('href')")
            check(home == "/pirate/", f"its header stays in pirate: {home!r}")
            await tap(page, base + "/pirate/section/8/")
            s = await shown(page)
            links = await page.evaluate("[...document.querySelectorAll('#app a[href*=\"admin05\"]')].map(a => a.getAttribute('href'))")
            check(s[0] == "app" and links and all(l.startswith("/pirate/8/") for l in links), f"its listing links pirate pages: {links[:2]}")
            await page.goto(base + "/man/8/admin050/", wait_until="networkidle")
            await page.wait_for_timeout(1500)
            s = await shown(page)
            check(s[0] == "app" and "admin050" in await content(page) and "slay bestie" not in await content(page) and "original" in s[1],
                  f"man/ is the original: {s}")
            r = await page.goto(base + "/witch/8/admin050/")
            check(r.status == 404, f"a variant there isn't: {r.status}")
            await ctx.close()
            errors[:] = [e for e in errors if not ("404" in e and "/witch/" in e)]  # (meant)

            print("without JavaScript, no links only the app can follow")
            nojs = await browser.new_context(java_script_enabled=False)
            p2 = await nojs.new_page()
            await p2.goto(base + "/girl/8/admin050/")
            check(not await p2.locator(".meta[data-app-only]").is_visible(), "the variants are hidden")
            check("girly admin050" in await p2.locator(".content").text_content(), "the girly page is there")
            await nojs.close()

            print("no script errors")
            for e in errors:
                check(False, e)
            if not errors:
                check(True, "none in any page")
            await browser.close()
    finally:
        if server:
            server.terminate()
        shutil.rmtree(tmp, ignore_errors=True)
    if failures:
        print(f"\n{len(failures)} failed:")
        for f in failures:
            print("  " + f)
        sys.exit(1)
    print("\nall navigation checks passed 💅")


asyncio.run(main())
