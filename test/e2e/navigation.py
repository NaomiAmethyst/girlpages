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
import subprocess
import sys
import tempfile
import time

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
    site = os.path.join(tmp, "site")
    run = lambda *a: subprocess.run([GIRLREPO, "-d", site, *a], check=True, capture_output=True)
    run("ingest", man)
    run("publish", "--precompress", "none")
    return site


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

            async def fresh(url, width=390):
                ctx = await browser.new_context(viewport={"width": width, "height": 800}, is_mobile=width < 600, has_touch=width < 600)
                page = await ctx.new_page()
                await page.goto(base + url, wait_until="networkidle")
                await page.wait_for_timeout(1500)  # the app loads in idle time
                return ctx, page

            path = lambda page: page.url.replace(base, "")

            print("back and forward between the static page and the app")
            ctx, page = await fresh("/section/1/")
            steps = []
            for href in ["/man/1/atool0000/", "/", "/section/8/", "/man/8/admin001/"]:
                await tap(page, base + href)
                steps.append((href, await shown(page)))
            for href, s in steps:
                check(s[0] == "app", f"{href} shown by the app")
            want = ["/section/8/", "/", "/man/1/atool0000/", "/section/1/"]
            for w in want:
                await page.go_back()
                s = await shown(page)
                check(path(page) == w and (s[0] == "static") == (w == "/section/1/"), f"back to {w}: {path(page)} {s}")
            for w in reversed(["/man/8/admin001/", "/section/8/", "/", "/man/1/atool0000/"]):
                await page.go_forward()
                s = await shown(page)
                check(path(page) == w and s[0] == "app", f"forward to {w}: {path(page)} {s}")
            await ctx.close()

            print("going back lands where the view was scrolled to")
            ctx, page = await fresh("/man/1/atool0000/")
            await tap(page, base + "/section/8/")
            await shown(page)
            await page.mouse.wheel(0, 900)
            await page.wait_for_timeout(500)
            y = await page.evaluate("scrollY")
            await tap(page, base + "/man/8/admin002/")
            await shown(page)
            check(await page.evaluate("scrollY") < 50, "a new view starts at the top")
            await page.go_back()
            await shown(page)
            y2 = await page.evaluate("scrollY")
            check(abs(y2 - y) < 30, f"back to the listing at {y2}, scrolled to {y} before")
            await ctx.close()

            print("quick taps: the last one wins")
            ctx, page = await fresh("/man/1/atool0000/")
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
            await tap(page, base + f"/man/1/{target}/")
            for _ in range(5):
                await page.mouse.wheel(0, 2500)
                await page.wait_for_timeout(60)
            await page.wait_for_timeout(2500)
            s = await shown(page)
            check(path(page) == f"/man/1/{target}/" and s[0] == "app" and target.upper() in s[1], f"landed on the page: {path(page)} {s}")
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
            ctx, page = await fresh("/man/1/atool0000/")
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
            ctx, page = await fresh("/man/1/atool0000/")
            await tap(page, base + "/section/1/")
            await shown(page)
            await page.locator("#app .letters a", has_text="M").first.click()
            await page.wait_for_timeout(1200)
            top = await page.evaluate("""() => { const h = document.querySelector('#app [id="letter-M"]');
                return h ? Math.round(h.getBoundingClientRect().top) : null }""")
            check(top is not None and 0 <= top < 200, f"M's heading is at the top ({top})")
            await ctx.close()

            print("search goes straight to a page it names, and back comes back")
            ctx, page = await fresh("/man/8/admin003/")
            await tap(page, base + "/search/?q=" + "1+atool0000")
            await page.wait_for_timeout(1500)
            s = await shown(page)
            check(path(page) == "/man/1/atool0000/" and "ATOOL0000" in s[1], f"straight to the page: {path(page)} {s}")
            await page.go_back()
            s = await shown(page)
            check(path(page) == "/man/8/admin003/" and s[0] == "static", f"back past the search: {path(page)} {s}")
            await ctx.close()

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
