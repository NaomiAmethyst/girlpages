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

## Configure

```toml
# ~/.config/girlpages/config.toml (or /etc/girlpages/config.toml)
backend = "auto"   # auto, claude or openai

[claude]
model = "sonnet"

[openai]
base_url = "http://localhost:11434/v1"   # Ollama, for example
model = "qwen3:32b"
```

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
lives in `internal/llm/prompt.go`.

## License

Copyright © 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

girlpages is free software: you can redistribute it and/or modify it under the
terms of the GNU General Public License as published by the Free Software
Foundation, either version 3 of the License, or (at your option) any later
version. See [LICENSE](LICENSE).
