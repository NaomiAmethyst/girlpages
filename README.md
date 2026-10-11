# girlpages 💅

`man`, but she's ✨ that girl ✨.

`girl` is a drop-in for `man(1)` that shows every manual page rewritten in
the voice of a Gen Z teenage girl, with colour and emoji, in your terminal.
The vibe changes; the facts don't. Every option, argument, default, file,
example and caveat from the original is kept, and code and flags are copied
exactly.

```
$ girl ls
ls(1)                         ✨ girl manual ✨                         ls(1)

💖✨ ls(1) ✨💖

       bestie this one lives in the General Commands Manual 🎀 aka the
       everyday-icons section

🌸 NAME 🌸

       ls - List directory contents. Ignore files and directories starting
       with a '.' by default 🙈

       okay so ls is literally the first command you ever learn and she is
       still the main character, no cap 👑 …
```

(Picture that in pink, purple and mint.)

## How it works

1. `girl` finds the page with `man -w`, so names, sections and search order
   are exactly man's.
2. If it's cached, it's shown right away.
3. Otherwise the page is rendered to plain text, sent to an LLM (long pages
   go in parts, split at sections), and the reply is cached. If the model
   drops any command-line options, that part is retried once.
4. The result is laid out like a man page and shown in your pager.

The LLM can be the [Claude Code](https://claude.com/claude-code) CLI
(`claude -p`) or anything that speaks the OpenAI chat completions API:
OpenAI, OpenRouter, Ollama, llama.cpp, vLLM, LM Studio and friends.

## Install

You need Go 1.24+, `man-db`, and either the `claude` CLI or an
OpenAI-compatible endpoint.

### Just for you

```sh
make install-user
```

This puts `girl` in `~/.local/bin`, its man page in `~/.local/share/man`, a
config file in `~/.config/girlpages/config.toml`, and (with systemd) a daily
user timer that refreshes pages you've converted. Pages are cached in
`~/.cache/girlpages`.

Or just `go install github.com/NaomiAmethyst/girlpages/cmd/girl@latest`.

### System-wide

```sh
make
sudo make install
```

This installs to `/usr/local` (change with `PREFIX=`), with the config in
`/etc/girlpages/config.toml` and a shared cache in `/var/cache/girlpages`
that every user reads from. It also installs:

- `girlpages-update.service`, which runs `girl --bulk --update --prune`;
- a hook for each package manager it finds that starts that service when man
  pages change: an apt `DPkg::Post-Invoke` hook, a pacman hook, and a dnf
  post-transaction action (`HOOKS="apt pacman dnf"` or `HOOKS=none` to
  choose);
- `girlpages-update.timer`, a daily run for pages that arrive any other way.

The hooks don't convert anything new until you opt in by girlifying the whole
system once:

```sh
sudo girl --bulk --dry-run   # how many pages is that, exactly
sudo girl --bulk -j 8        # go off, queen
```

After that, new and updated pages are converted automatically. Heads up:
a typical system has thousands of man pages, so a full run is a lot of LLM
calls. You can do it a section at a time (`sudo girl --bulk 1 8`), and since
it's incremental you can interrupt it and pick up where you left off.

`make uninstall` / `make uninstall-user` undo it all (config and caches are
left alone). Packagers: see the top of the `Makefile` for `DESTDIR` and
friends.

## In your browser

```sh
girl --serve                  # http://localhost:1989/
girl --html ls > ls.html      # one page, as a self-contained web page
```

`--serve` lets you browse pages by section, search names and descriptions,
and read them with a table of contents, clickable references to other pages,
and a link to the boring original. It's pink and white in light mode and
purple and black in dark mode, following your browser (or the 🌗 button).

Open a page that isn't girlified yet and it's converted on the spot, with a
waiting page that refreshes itself until she's ready. Everyone asking for the
same page shares one conversion, and no more than `jobs` requests hit the
model at once.

There's no login, so anyone who can reach it can spend your LLM budget: keep
it on localhost (the default), or use `--cached-only` to only serve what's
already converted (`sudo girl --bulk` first, say).

## Repositories

Someone already girlified the page you want? A girlpages repository is a pile
of ready-made pages on a static web host, and `girl` can read from one before
bothering a model:

```toml
[repo]
url = "https://example.org/repo"
```

Pages are matched by the hash of their roff source, so you only ever get a
conversion of exactly the version you have installed; anything else is
converted as usual. With a repo set, `girl --bulk` works even with no model
at all, caching everything the repo has. `girl --import URL-or-dir` does the
same for a repo you name, and `girl --export DIR` writes your own cache out as
a repo, with the package each page came from, for other machines to import
or for `girlrepo` to publish.

### Running one

`girlrepo` (`make girlrepo`, documented in `girlrepo(1)`) builds them:

```sh
girlrepo ingest --distro debian --release trixie pool/*.deb   # or .pkg.tar.zst, .rpm, dirs
girlrepo ingest --system                                      # or this machine, with its packages
girlrepo ingest git+https://github.com/tmux/tmux#3.5a          # or a git repository
girlrepo crawl apt https://deb.debian.org/debian trixie        # or a whole package repository
girlrepo crawl rpm https://dl.rockylinux.org/pub/rocky/9/BaseOS/x86_64/os
girlrepo crawl pacman https://geo.mirror.pkgbuild.com/core/os/x86_64
girlrepo girlify -n --price 0.10/0.50                         # what would it cost?
girlrepo import https://example.org/repo                       # merge in someone else's repo
girlrepo girlify -j 8                                         # convert what isn't yet
girlrepo publish --url https://example.org --gc               # index it and build the site
girlrepo serve                                                # preview at localhost:1990
```

That leaves a directory to upload anywhere static: the repo in `repo/`, and
a website around it. The static part is what search engines see: a page for
the latest version (by version number) of every man page, girlified, with
OpenGraph, JSON-LD and a sitemap, plus section listings. Everything else runs
in the browser straight from the repo: originals, older versions, browsing
just Debian, just Debian trixie, just Arch or upstream git, and search over
every page and version from every source (typos and all: `grpe` finds
`grep`). The site installs as an app (a PWA) and works offline: pages you've
read are kept by a service worker, and every repo file fetched is kept in
localStorage, so searches you've done before work without a network. When it
notices a newly published repository it clears out the files that aren't
used any more, in the background. Each page version is stored once however
many packages, distros or aliases share it, and everything but a few pointer
files is content-addressed, so it caches forever and syncs cheaply. The format is in `girlrepo(1)`.

## Configure

```toml
# ~/.config/girlpages/config.toml (or /etc/girlpages/config.toml)
backend = "auto"   # auto, claude or openai

[claude]
model = "haiku"   # the default

[openai]
base_url = "http://localhost:11434/v1"   # Ollama, for example
model = "qwen3:32b"

[repo]
url = ""   # a girlpages repository to try first
```

Converting thousands of pages? Batch APIs are about half the price, and
`girl --bulk` (and `girlrepo girlify`) can use OpenRouter's or Anthropic's:

```toml
batch = "anthropic"            # or "openrouter", which uses [openai]

[anthropic]
api_key_file = "/etc/girlpages/anthropic-key"   # or ANTHROPIC_API_KEY
model = "claude-haiku-5-5"
```

Results come back within a day. Runs save what they sent, so Ctrl-C is fine
and `--no-wait` just sends them off; the next run collects them.

`girl --doctor` shows what it's actually using. Everything is documented in
`dist/config.toml` and in `girl girl`. Want a different vibe? Set
`extra_instructions`.

## Usage

All the `man` you know:

```sh
girl ls                 # a page
girl 3 printf           # from a section
girl -a printf          # every match
girl -k compress        # apropos (straight to man -k)
girl -l ./thing.1       # a local file
```

Plus:

```sh
girl --refresh tar      # convert again
girl --model opus tar   # with a different model
girl --cached-only ls   # never call the LLM
girl --repo URL ls      # try a girlpages repository first
girl --raw ls           # the stored markup
girl --doctor           # config, backend and cache status
girl --html ls          # a standalone HTML page
girl --serve            # the web app
girl --bulk [sections]  # convert everything
```

## Hacking

```sh
make check   # tests, vet, gofmt
```

Pages are stored as a small Markdown dialect with colour tags (`<pink>…</pink>`
and friends, plus `<rainbow>`); `internal/render` turns that into ANSI or
HTML, and `internal/web` is the server, templates and stylesheet. The prompt
lives in `internal/llm/prompt.go`. Repositories are `internal/repo` (format,
store, client, search index), `internal/ingest` and `internal/site`; the
browser search in `internal/site/static/search.js` must match
`internal/repo/search.go`, which a test checks with node.

## License

Copyright © 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

girlpages is free software: you can redistribute it and/or modify it under the
terms of the GNU General Public License as published by the Free Software
Foundation, either version 3 of the License, or (at your option) any later
version. See [LICENSE](LICENSE).
