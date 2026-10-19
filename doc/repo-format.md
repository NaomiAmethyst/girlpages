# The girlpages repository format

A girlpages repository holds man pages: their original text and rewritten
versions of them (*variants*: the girly one, and in future others), for
many versions of each page from many distributions. It's a tree of JSON
files, made to be served by any static web server and read by anything
that can fetch a URL: `girl`, the girlpages website, or your own tools.

This document describes **format 1**. `girlrepo` (in this repository) is
the reference implementation for writing repositories, and
`internal/repo` for reading them; where this document and the code
disagree, that's a bug in one of them.

* [Principles](#principles)
* [Conventions](#conventions)
* [Identifiers](#identifiers): source hashes, names, sections, sources,
  variants
* [The files](#the-files)
* [Which version is the latest](#which-version-is-the-latest)
* [Variants and their syntaxes](#variants-and-their-syntaxes), including
  [girlpages markup](#girlpages-markup)
* [The search index](#the-search-index)
* [Packs](#packs): the repository in a few big files, for mirroring
* [Recipes](#recipes)
* [Publishing and caching](#publishing-and-caching)
* [Compatibility](#compatibility)

## Principles

* **Everything is static.** There's no server-side code: a repository is
  files, and a client reads them by path.
* **Most files are named after their own SHA-256**, so they never change
  and can be cached forever. Only a few small files change in place:
  `manifest.json`, which is the root; and *pointers*, which name the
  immutable files that hold the data. A client that starts from a fresh
  `manifest.json` (or pointer) sees a consistent repository, however long
  it has cached everything else.
* **A page version is identified by its roff source**: the
  [source hash](#source-hashes) of the page as installed. Someone with a
  page installed can find its conversion without knowing its name, its
  package or its distribution, in one or two requests.
* **One version, stored once.** The same text from many packages,
  distributions or alias names is one version, with a list of everywhere
  it was found.

## Conventions

### JSON

Every file is UTF-8 JSON: an object, compactly encoded with map keys sorted,
`<`, `>` and `&` not escaped, and a trailing newline (as Go's
`json.Encoder` with `SetEscapeHTML(false)` writes it). Writers should
encode the same way, so that the same document always has the same hash;
readers must accept any valid JSON.

Every document has a `format` field, which is `1`. A reader should refuse a
document with a higher format: it may not mean what the reader thinks. A
reader must ignore fields it doesn't know (new optional fields don't
change the format).

Times are RFC 3339 strings in UTC (`"2026-10-11T05:04:50Z"`). The zero time
(`"0001-01-01T00:00:00Z"`) means unknown.

### Hashes

A *hash* is the SHA-256 of a file's exact bytes, as 64 lower-case hex
digits. `ab` in paths below is the first two digits of the hash in the same
path.

Files named after their own hash (`…/<sha>.json`, `…-<sha>.json`) can be
checked: hash what you fetched and compare. Readers should, as `girl` does,
when the content matters.

### Tables

Indexes (chapter indexes, shard manifests, the page table) are made to be
small, so lists of records in them are *tables*: the fields are named once,
in `keys`, and `data` holds each record's values in turn:

```json
{"keys": ["first", "last", "sha256", "bytes"],
 "data": ["#endif", "0000001c", "x}Gd…", 66688,
          "0000001d", "abort", "Q9k(…", 65201]}
```

That's two records: `{first: "#endif", last: "0000001c", …}` and so on.
Readers find fields by name, and must ignore ones they don't know.

Hashes in tables are written in *basE91* (40 characters rather than 64),
with the alphabet
``ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#$%&()*+,./:;<=>?@[]^_`{|}~'``
(basE91's own, with `'` in place of `"`, so they need no escaping in JSON),
encoding the hash's 32 bytes as basE91 does: taking 13 bits at a time
from a little-endian bit queue (or 14, when the 13-bit value is 88 or
less) and writing each value as two characters, the low one (value mod 91)
first. Paths still have them in hex. `internal/repo/compact.go` and
`decode91` in `internal/site/static/girlpages.js` are implementations.

### Paths

All paths are relative to the repository's root, which is wherever its
`manifest.json` is: `https://girlpages.dev/repo/` for girlpages.dev, where
the website is the directory above. They use `/`.

Page names and sections appear in paths *escaped*: every byte other than
ASCII letters, digits and `_+-.,:@`, and a `.` at the very start, becomes
`%` and two upper-case hex digits. So `ls` is `ls`, `File::Spec` is
`File::Spec`, `[` is `%5B`, `.hidden` is `%2Ehidden`, and `50%` is `50%25`.
Escaped names are file names: in a URL they must be percent-encoded again
(`%5B.json` is requested as `%255B.json`).

### Compression

Repositories are best served with pre-compressed copies of their files
(`girlrepo publish` writes `.zst`, `.br` and `.gz` copies for this). A
reader over HTTP should send `Accept-Encoding: zstd, br, gzip` (or what it
supports) and decode according to `Content-Encoding`. It should limit how
much a response may *decompress* to, not just how much is sent: `girl`
refuses documents over 64 MiB. The repository is the same whatever the
encoding.

## Identifiers

### Source hashes

A page version's *source* is its roff source, made independent of how it
was packaged: decompressed, and with `.so` requests (which include other
files) replaced by what they include. Its *source hash* is the hash of
those bytes. A symlink, an alias stub (a file that is just `.so man1/ls.1`)
and the page it points to all have the same source hash. To find a page in
a repository, compute the source hash of the file `man -w` gives you.

To compute the source of a file:

1. **Read it, decompressing** by its contents (not its name): gzip
   (`1f 8b`), bzip2 (`BZh`), xz (`fd 37 7a 58 5a 00`), zstd (`28 b5 2f fd`),
   `compress` (`1f 9d`); `.lzma` and `.lz` files by their names. Anything
   else is used as it is. Follow symlinks.
2. **Expand `.so` requests.** Split the bytes into lines, each keeping its
   `\n` (the last line may have none). A line is a request if, with its
   `\n` and then one `\r` (if there is one) removed, it matches the
   regular expression `^\.[ \t]*so[ \t]+(\S+)[ \t]*$`. Every line that
   isn't a request is kept exactly. A request is replaced by the
   **source** of the file it names (recursively; give up after 8 levels),
   followed by `\n` if the request line ended in `\n` and the included
   source doesn't end in `\n` (an empty one counts as not ending in `\n`).
3. **Finding the included file.** The name in a request is tried relative
   to, in order: the page's *manual root* (the directory above its
   `man1`-style directory: `/usr/share/man` for
   `/usr/share/man/man1/ls.1.gz`); any other manual roots the tool knows
   (the rest of the manpath); and the including file's own directory. An
   absolute name is used as it is. In each place the name is tried as it
   is, then with `.gz`, `.bz2`, `.xz`, `.zst`, `.lzma`, `.lz`, `.Z` and
   `.z` added; the first regular file found is used. If none is found, the
   page has no source (it can't be rendered by `man` either).

Nothing else is changed: line endings, encodings and trailing whitespace
all count.

### Names and sections

A page is named and sectioned as `man` does it: `ls` in section `1`,
`printf` in `3`, `SSL_new` in `3ssl`. Names may contain almost anything,
hence the [escaping](#paths).

A section's *chapter* is the part of the manual it belongs to: for a
section starting with a digit, that digit (`3` for `3`, `3p` and `3ssl`);
any other section is its own chapter (`n`, `l`). Counts by section in the
[manifest](#manifestjson)'s sources are by chapter.

Sections that don't start with a digit and are longer than a letter are
*series* of documents named by number: `rfc` holds RFCs, RFC 9110 being
the page named `9110` in section `rfc`. Its original is the RFC's
published text (cleaned of page breaks and running headers and footers),
as a [text variant](#text) whose first section, with no heading, is the
title block; its [source hash](#source-hashes) is the hash of the
published text, as fetched, and its package's `distro` is `ietf`, with
the date (`2022-06`) as its `version`. Names in a series sort by number.

### Sources

A *source* is where pages come from: a distribution (`debian`), a release
of one (`debian/trixie`), or `upstream` for pages from git repositories.
These *source keys* appear in listings, chapter indexes and the search
index. A package (see [Listing](#pagepage-absha-json--listing)) belongs to
the source named by its `distro`, with `upstream` for packages with a
`url` and no `distro`; and to `distro/release` too if it has a `release`
(upstream ones don't).

### Variants

A *variant* is one rendering of a page version, named by a short lower-case
word matching `^[a-z][a-z0-9-]{0,31}$`:

* **`man`**: the original, as `man` renders it. Every version has one.
* **`girl`**: rewritten in the voice of a Gen Z girl, every detail kept.

There may be others (made with other prompts); readers must cope with
names they don't know, and may ignore them.

Each variant of a version may be made more than once: each time is a
*generation*, numbered from 1. The latest generation is the one with the
highest number.

## The files

| Path | Changes? | What |
|---|---|---|
| `manifest.json` | yes | [The root](#manifestjson) |
| `page/name-<section>/<name>.json` | yes | [Pointer to a page's listing](#pointers) |
| `page/page-ab/<sha>.json` | no | [Listing: every version of a page](#pagepage-absha-json--listing) |
| `contents/ab/<source>/variants.json` | yes | [A version's variants and their generations](#contentsabsourcevariantsjson) |
| `contents/ab/<source>/<variant>-<sha>.json` | no | [One generation of a variant](#contentsabsourcevariant-shajson) |
| `section/<chapter>.json` | yes | [Pointer to a chapter's index](#pointers) |
| `section/<chapter>-<sha>.json` | no | [Chapter index: every page in a chapter](#sectionchapter-shajson) |
| `section/shard-ab/<sha>.json` | no | ″ the rest of it, in shards |
| `index/variants/<variant>/manifest-<sha>.json` | no | [Which versions have a variant](#variant-indexes) |
| `index/variants/<variant>/shard-ab/<sha>.json` | no | ″ |
| `index/words/manifest-<sha>.json` | no | [Search index](#the-search-index): words |
| `index/words/shard-ab/<sha>.json` | no | ″ |
| `index/grams/manifest-<sha>.json` | no | ″ trigrams |
| `index/grams/shard-ab/<sha>.json` | no | ″ |
| `index/names/manifest-<sha>.json` | no | ″ the page table: names |
| `index/names/shard-ab/<sha>.json` | no | ″ |
| `index/docs/manifest-<sha>.json` | no | ″ the page table: the rest |
| `index/docs/shard-ab/<sha>.json` | no | ″ |
| `packs/<sha>.tar.zst` | no | [Packs](#packs) of everything in `page/` and `contents/` |

`<section>` and `<name>` are [escaped](#paths); `<source>` is a source
hash, `<sha>` the file's own hash, and `ab` the first two digits of the
hash that follows it. Anything else in a repository (a `.lock` file,
`.tmp-*` files being written, compressed copies) isn't part of it.

### manifest.json

The root. Fetch it fresh to see the repository as it is now.

```json
{
  "format": 1,
  "generated": "2026-10-11T09:38:14Z",
  "pages": 7452,
  "variants": {"girl": 7438, "man": 7452},
  "sections": [
    {"section": "1", "sha256": "64d8…", "pages": 1963, "variants": {"girl": 1958, "man": 1963}},
    …
  ],
  "index": {
    "words": "4532…", "grams": "15b1…", "names": "90a2…", "docs": "6e78…",
    "variants": {"girl": "7c1d…", "man": "a09e…"}
  },
  "sources": [
    {"distro": "ubuntu", "pages": 7452, "sections": {"1": 2088, "3": 2488, …}},
    {"distro": "ubuntu", "release": "questing", "pages": 7452, "sections": {…}}
  ]
}
```

| Field | |
|---|---|
| `generated` | When it was published. |
| `pages` | How many pages (name and section pairs) there are. |
| `variants` | For each variant, how many pages' [latest versions](#which-version-is-the-latest) have it. |
| `sections` | Each section (exact, like `3ssl`), sorted, with counts as above. |
| `chapters` | Each [chapter](#names-and-sections), sorted: the hash of its [index](#sectionchapter-shajson), and how many pages it has. |
| `index.words`, `.grams`, `.names`, `.docs` | Hashes of the [search index](#the-search-index) manifests. |
| `index.variants` | For each variant, the hash of its [variant index](#variant-indexes) manifest. |
| `sources` | Each [source](#sources), sorted by distro then release (a distro's entry, with no `release`, first): how many pages have a version from it, in all and by [chapter](#names-and-sections). |
| `packs` | Optional: the repository's [packs](#packs), in order. |

### Pointers

`page/name-<section>/<name>.json` names a page's current listing, and
`section/<chapter>.json` a chapter's current index:

```json
{"format": 1, "name": "ls", "section": "1", "sha256": "f0e6…"}
{"format": 1, "section": "1", "sha256": "64d8…"}
```

### page/page-ab/&lt;sha&gt;.json — Listing

Every version of one page, and where each was found.

```json
{
  "format": 1,
  "name": "tar",
  "section": "1",
  "current": "3a85…",
  "sources": {"arch": "3a85…", "arch/rolling": "3a85…", "rocky": "366c…", …},
  "versions": [
    {
      "source": "3a85…",
      "title": "TAR(1)",
      "description": "an archiving utility",
      "added": "2026-10-11T05:14:59Z",
      "version": "1.35",
      "variants": {
        "girl": {"sha256": "b347…", "size": 9817, "generation": 1, "prompt_version": 1},
        "man":  {"sha256": "c0ff…", "size": 31877, "generation": 1}
      },
      "packages": [
        {"distro": "arch", "release": "rolling", "name": "tar", "version": "1.35-5", "arch": "x86_64"},
        {"distro": "debian", "release": "trixie", "name": "tar", "version": "1.35+dfsg-3.1", "arch": "amd64"}
      ]
    },
    …
  ]
}
```

| Field | |
|---|---|
| `current` | The source hash of the [latest version](#which-version-is-the-latest), the one to show by default. |
| `sources` | For each [source key](#sources) the page has versions from, the source hash of the version that source has (its latest, if it has several). |
| `versions` | Every version, newest added first (ties by source hash). |
| `versions[].source` | The version's [source hash](#source-hashes). |
| `.title` | The page's own title, from man's header line (`TAR(1)`); it can differ from the name (Debian's `gnutrue(1)` is `TRUE(1)`). |
| `.description` | The one-line summary from its NAME section. |
| `.added` | When this version was first added to the repository. |
| `.version` | Its upstream version number, if its packages say (see [below](#which-version-is-the-latest)). |
| `.variants` | For each variant, its latest generation: the hash and size of its file, its generation, and the prompt version that made it (if it says). |
| `.packages` | Everywhere it was found: `distro`, `release`, package `name`, package `version` and `arch`, and `url` for git repositories (with `version` like `3.5a (549c35b06165)`, tag and commit). Any may be missing. |

Readers shouldn't need to work out `current`, `sources` or `version`
themselves; they're there so clients don't have to reimplement version
ordering.

### contents/ab/&lt;source&gt;/variants.json

Every generation of every variant of one page version. This is how to find
a version's variants from its source hash alone.

```json
{
  "format": 1,
  "source": "0003…",
  "variants": {
    "girl": [
      {"sha256": "dcf4…", "size": 5465, "generation": 1, "prompt_version": 1}
    ],
    "man": [
      {"sha256": "762d…", "size": 5550, "generation": 1}
    ]
  }
}
```

Each variant's list holds every generation that has been made (none are
removed); use the one with the highest `generation`. Its file is
`contents/ab/<source>/<variant>-<sha256>.json`.

### contents/ab/&lt;source&gt;/&lt;variant&gt;-&lt;sha&gt;.json

One generation of one variant of one page version.

```json
{
  "format": 1,
  "variant": "girl",
  "source": "0003…",
  "title": "LS(1)",
  "syntax": "girlpages-markup",
  "sections": [
    {"heading": "", "body": "okay bestie, this is the ls(1) manual 💅\n\n"},
    {"heading": "✨ NAME ✨", "body": "\n`ls` — list directory contents 📂\n\n"},
    …
  ],
  "generator": {"backend": "openai", "model": "anthropic/claude-haiku-5.5", "prompt_version": 1, "generated": "2026-10-10T07:39:33Z"}
}
```

| Field | |
|---|---|
| `variant`, `source` | What it is: check these match what you asked for. |
| `title` | The page's title, if known. |
| `syntax` | How `sections` are written: [`text` or `girlpages-markup`](#variants-and-their-syntaxes). Show a syntax you don't know as plain text. |
| `header`, `footer` | For `text`: man's first and last lines (`LS(1)  User Commands  LS(1)`). |
| `sections` | The page, cut at its top-level headings: each `heading` (`""` for anything before the first) and `body`, the lines after it, each ending in `\n`, exactly as they were. |
| `generator` | What made it, for rewritten variants: `backend`, `model`, `prompt_version` (of that backend's prompt for this variant), and when. |

### section/&lt;chapter&gt;-&lt;sha&gt;.json

Every page in a [chapter](#names-and-sections) (all its sections: `3`,
`3ssl`, `3pm`…), in the order its listing shows them: by name ignoring
case, numbers by value, then by section. So a listing reads it in order,
and it's split into shards that start with letters where that's near
enough, so a listing shows its start, or a letter's, from one small
file. The index holds the first shard's pages itself:

```json
{
  "format": 1,
  "chapter": "1",
  "count": 56682,
  "pages": {
    "keys": ["name", "section", "title", "description", "page", "version", "versions", "sources", "variants", "girl"],
    "sections": ["1", "1ssl"],
    "sources": ["arch", "arch/rolling", "debian", "debian/trixie", "rocky"],
    "variants": ["girl", "man"],
    "versions": ["", "1.35", …],
    "data": ["tar", 0, "TAR(1)", "an archiving utility", "Ak9}…", 1, 2, 31, 3, 15, …]
  },
  "shards": {"keys": ["sha256", "bytes", "count", "first", "last", "prev"],
             "data": ["Lw]u…", 262144, 1702, "aa-enabled", "azure", "#", …]},
  "letters": {"keys": ["letter", "chunk"], "data": ["#", 1, "A", 2, …]}
}
```

`pages` is a [table](#tables) whose records refer to the lists beside its
keys, of the values that appear in it (sorted):

| Field | |
|---|---|
| `name`, `title`, `description` | The page's name, and its latest version's title and description. |
| `section` | Its exact section, as an index into `sections`. |
| `page` | The hash of the page's [listing](#pagepage-absha-json--listing), in basE91. |
| `version` | Its latest version's upstream version, as an index into `versions` (whose `""` is unknown). |
| `versions` | How many versions it has (`0` for one). |
| `sources` | The [source keys](#sources) it has versions from, as a bitmask: bit *i* (the number's 2<sup>*i*</sup>) for `sources[i]`. |
| `variants` | Its latest version's variants, as a bitmask over `variants`. |
| `girl` | The sources whose version of it has a girly variant, as a bitmask over `sources`. |

Bitmasks stay below 2<sup>53</sup>, so a file has at most 53 sources;
readers in JavaScript must decode them with arithmetic, not its 32-bit
bitwise operators.

`shards` are the rest of the pages, in order, each
`section/shard-ab/<sha>.json` holding a `pages` table like the index's
(with its own lists):

```json
{"format": 1, "chapter": "1", "pages": {"keys": [...], "sections": [...], …, "data": [...]}}
```

Each shard's entry gives its size, how many pages it has, its first and
last pages' names, and `prev`, the letter of the page before it, so a
shard shown on its own knows whether its first letter carries on. A
letter is a name's first letter, upper case, `#` if that isn't an ASCII
letter, or for a number, `1–999` or its thousand (`9000s`). Counting the
index's own pages as chunk 1 and the shards as 2 on, `letters` gives the
chunk each letter starts in.

Shards are cut by size, ending with a letter where that's near: the first
(the index's own pages) aims at 64 KiB and the others at 256 KiB, but one
goes on to the end of a letter if that's no more than 20% over, or else
stops at its start if that's no less than half; after one that ends with a
letter, the next aims at 64 KiB again, so a letter's first shard is small.

Readers must read the shards (in order) for every page.

### Variant indexes

`index/variants/<variant>/manifest-<sha>.json` lists the shards of an index
of the versions that have that variant, by key range:

```json
{"format": 1, "shards": {"keys": ["first", "last", "sha256", "bytes"], "data": ["0003…", "7fff…", "s?nP…", 1040873, …]}}
```

That's a *shard manifest*: a [table](#tables) of shards by the range of
keys each holds (sorted, not overlapping; keys compare as UTF-8 bytes),
with their hashes (in basE91) and sizes, so clients can say how big fetching
every shard would be. The search index's words and trigrams have these too.

Each shard, `index/variants/<variant>/shard-ab/<sha>.json`, holds the first
`length` (16) hex digits of the source hash of each version with that
variant, sorted:

```json
{"format": 1, "length": 16, "sources": ["0003…", "000b…", …]}
```

To check whether a version has a variant, find the shard whose range holds
the first 16 digits of its source hash (shards are sorted and don't
overlap; compare as strings) and look for them in it. A match means the
variant is there, all but certainly (a false match only costs a request
that finds nothing); no match, or no shard, means it isn't. A repository
with no `index.variants` entry for a variant has no index for it: ask for
each version instead.

## Which version is the latest

A page's latest version (its `current`) is the one with the highest
*upstream version number*, not the one added last:

1. **A version's upstream version** is the highest of its packages'
   upstream versions. A package version's upstream version is found by:
   removing ` (…)` and everything after it (git: `3.5a (549c35b)` →
   `3.5a`, which also marks it as git); removing an epoch (`^[0-9]+:`);
   for anything but git, removing everything from the last `-` if what
   follows it has a digit (the Debian revision, or the rpm or pacman
   release); taking what follows `+really` if there is one; removing
   `[+~](dfsg|ds|repack|nmu|deb|ubuntu|build)[0-9.]*.*$`; and removing
   anything before the first digit (`v3.5a` → `3.5a`). If what's left has
   no digit, or is seven or more hex digits (a bare commit), there's no
   upstream version. So `1:1.35+dfsg-3.1build1`, `1.35-5` and
   `2:1.35-13.el9` are all `1.35`.
2. **Versions compare** with rpm's `rpmvercmp` on their upstream versions
   (runs of digits compare as numbers, runs of letters as strings, digits
   beat letters, `~` sorts before everything including the end, `^` after
   the end but before anything else). A version with an upstream version
   beats one without.
3. **Ties** (as with a distro's patched copy of the same release) go to the
   version found in the most packages, then the one added last, then the
   higher source hash.

The version a *source* has is its latest by the same rules, among the
versions with packages from it.

## Variants and their syntaxes

### text

`man`'s rendering, at 80 columns, with hyphenation and justification off,
and no formatting. To show it, reassemble the page: `header` and a newline
(if there's a header or footer); then each section's `heading` and a
newline (unless it's `""`) and its `body`; then `footer` and a newline (again, if there's a header or footer).
That gives back man's output exactly.

### girlpages markup

A small dialect of Markdown, made to be rendered in a terminal as well as in
HTML. To reassemble a page: each section's `"# " + heading + "\n"` (unless
it's `""`) and its `body`. The reference renderers are `internal/render`
(ANSI and HTML) and `internal/site/static/render.js` (the same HTML, in the
browser).

**Before rendering, remove control characters** other than newlines and
tabs (bytes below 0x20, and 0x7f to 0x9f): a page must not be able to drive
a terminal. Escape everything that goes into HTML.

Blocks, a line at a time:

* `#` to `######` and a space: a heading. Trailing `#`s are dropped.
* A line of three or more `-`, `*` or `_` (spaces allowed between): a
  divider.
* ```` ``` ```` or `~~~` (indented or not): a fenced code block, shown
  verbatim, to a line starting with the same three characters. Tabs become
  four spaces, and the fence's own indentation is removed from each line.
* `>`: a quote, joined with the `>` lines that follow.
* `|`: a table, joined with the `|` lines that follow; a separator row
  (`|---|:-:|`) is dropped, and the first row is a header if there are
  others.
* `-`, `*`, `+` or `•`, or a number of up to three digits and `.` or `)`,
  then a space: a list item. Two spaces of indentation (a tab counts as
  four) is a level of nesting, up to six. Following lines that don't start
  a block join it.
* A blank line ends a paragraph or item; anything else is a paragraph,
  joined with the lines that follow until one starts a block.

Inline, within any block but code:

* `` `code` ``, with any number of backticks to fence backticks inside.
* `**bold**` and `*italic*`. A `*` only opens italics at the start of a word
  (so `2*3` and `*.c` are left alone) and only with a closing one to match.
* Colour tags: `<pink>`, `<hotpink>`, `<purple>`, `<lavender>`, `<cyan>`,
  `<mint>`, `<yellow>`, `<peach>`, `<red>`, `<blue>`, `<green>` and
  `<rainbow>`, each with its closing tag. They nest. Any other `<…>` is
  text.
* `\` before ASCII punctuation is a literal.
* `name(section)` (a section starting with a digit, or `n` or `l`) refers to
  another page, and `http://…` and `https://…` are links; renderers may
  link them.

## The search index

The website searches a repository from the browser, fetching only the
parts a query needs. The index covers every page from every source and the
text of all its versions.

**The page table.** Pages are numbered: their *doc ids*. A page keeps its
id from one publish to the next, new pages taking the next ids, so the
parts of the table that don't change keep their hashes (and clients' copies
of them stay good); ids of pages that have gone stay unused, until there
are so many that everything's numbered afresh, in name order.

The table is in two parts, each in shards of fixed runs of ids (about a
megabyte each): the names (`index/names/…`), which searches fetch as soon
as they start, so results can be named as soon as they're found; and the
rest (`index/docs/…`). Each part's manifest lists its shards by the ids
they hold, as a [table](#tables):

```json
{"format": 1, "count": 143820,
 "shards": {"keys": ["first", "last", "sha256", "bytes"], "data": [0, 49999, "Fq2*…", 1251177, 50000, 99999, "c%Wm…", 1244019, …]}}
```

`count` is one more than the highest id. A shard of names
(`index/names/shard-ab/<sha>.json`) has the names and sections of the
pages from id `first` on; a shard of docs, their descriptions and
[source keys](#sources), space-separated:

```json
{"format": 1, "first": 0, "names": {"keys": ["name", "section"], "data": ["[", "1", "2to3", "1", …]}}
{"format": 1, "first": 0, "docs": {"keys": ["description", "sources"], "data": ["check file types and compare values", "debian debian/trixie", …]}}
```

An unused id has the name `""`.

**Words.** `index/words/manifest-<sha>.json` is a
[shard manifest](#variant-indexes) of word ranges, its shards small (about
64 KB: a search fetches the ones its words are in); each
`index/words/shard-ab/<sha>.json` maps words to postings, doc id and score
in turn, best first (at most 400 per word):

```json
{"format": 1, "words": {"directory": [4120, 140, 5121, 61, …], …}}
```

Words are *tokens*: runs of Unicode letters, decimal digits and `_`,
lower-cased, of 2 to 40 characters. A page's score for a word adds up:

| Where | Score |
|---|---|
| The page's whole name, lower-cased (even if it isn't a token, like `git-commit` or `[`) | 1000, plus 25 × (13 − the section's place in man's order, `1 n l 8 3 0 2 5 4 9 6 7`, counting from 0, by the section's start; 12 for anything else) |
| A token of the name | 100 |
| A token of the description (not a stopword) | 60 |
| A token of the text (not a stopword) | 1 + 4 × log₂(times it appears), at most 20 |

Words of the text that are in more than 30% of pages (with at least 20
pages) only keep postings that score more than 20 (from the name or
description). Stopwords: `a an and are as at be but by for from has have if
in into is it its no not of on or so such that the their then there these
they this to was were will with you your can may must should which when
where who also only than other any all each more most some`.

**Trigrams.** `index/grams/manifest-<sha>.json` lists shards of trigram
ranges; each shard maps trigrams to the words containing them, and, in
step, how many pages each is in:

```json
{"format": 1, "grams": {"^gr": ["grep", "group", …]}, "pages": {"^gr": [212, 380, …]}}
```

A word's trigrams are the distinct runs of three characters of `^` + word
+ `$` (so `grep` gives `^gr`, `gre`, `rep`, `ep$`). Words of names and
descriptions are included, and words of the text found in more than one
page that aren't mostly digits.

**Searching**, as `internal/repo/search.go` and
`internal/site/static/search.js` do it (other tools may do better; the
scores are only advice):

1. Tokenize the query; drop repeats.
2. For each token: its postings, at their scores; the postings of up to 50
   other words it's a prefix of (in sorted order, from the shards whose
   ranges could hold them), at half of the lower of their score and 100;
   and, if it has no postings of its own and is 3 or more characters, the
   postings of up to 5 words within 1 typo (for up to 5 characters, else
   2; an edit, an insertion, a deletion or swapping two neighbours each
   count as one) found through the trigram index (the query word's
   trigrams, plus `^` with its first and third characters, and `^` with
   its second and first), closest first, then in most pages, then alphabetically, at 0.3 of
   their score. A page's score for a token is the best of these.
3. Each page's score is the sum over the tokens it has. Pages with every
   token come first; if none has them all, pages with any. If the query is
   one word (no spaces), its lower-cased whole is also looked up as a word,
   and that score added (this finds `git-commit`).
4. Order by score, then doc id. To keep to one source, skip pages without
   it among their source keys (looking at most 2000 pages down).

The website shows the count at once, and the results a screenful at a
time, each named as soon as its names shard is in and described when its
docs shard is. A query that names a page exactly (`2 open`, `open(2)`,
`rfc 9110`: a section, or every section of a chapter in turn, and a name)
goes straight to it, if it's there, without searching.

## Packs

A repository is most of two million small files, which is a lot of
requests to mirror. *Packs* hold the same files, in a few big ones: a
repository may publish them alongside its files, and a *pack-only*
repository has nothing but them (and a `manifest.json` listing them).

### The log

Packs are a log. Each pack is a list of changes to `page/` and
`contents/`: *puts* (this path now holds these bytes) and *deletes* (this
path is gone). Replaying every pack in order, from nothing, gives every
file in `page/` and `contents/` as of the newest pack: everything but the
[indexes](#sectionchapter-shajson) and the [search index](#the-search-index),
which are worked out from those files (`girlrepo publish` does). A
repository that publishes packs adds one each time it publishes, so the log
always ends where its `manifest.json` does.

Every change sets a path to some bytes or removes it, so replaying a pack
again, or from part of the way through the log, does no harm. In one pack
a path is put or deleted at most once.

Most packs are *full*: about 256 MiB, and once written, never changed or
removed. Each publish adds a *tail* pack with what's changed since the
last pack, which is small; once the tail packs come to a full pack's worth,
they're *replaced* by full packs holding the same changes (and any since),
and the last of what's left is a new tail. A replaced pack stays published
for a day or so, for clients part of the way through fetching it, and then
goes.

### packs/&lt;sha&gt;.tar.zst

A pack is a POSIX (pax) tar file, compressed with
[zstd](https://www.rfc-editor.org/rfc/rfc8878) (with a window of no more
than 128 MiB, which readers must accept), and named after the hash of the
compressed file. Its first member is `pack.json`; then each put follows, in
the order `pack.json` lists them, as a member named by its path.

```json
{
  "format": 1,
  "seq": 3,
  "prev": "9f1c…",
  "replaces": ["e2b0…", "77d4…"],
  "generated": "2026-10-19T09:38:14Z",
  "puts": {"keys": ["path", "sha256", "bytes"],
           "data": ["page/name-1/ls.json", "x}Gd…", 128, …]},
  "deletes": ["page/name-1/old.json", …]
}
```

| Field | |
|---|---|
| `seq` | Its place in the log: 1 for the first, and one more for each after. |
| `prev` | The hash of the pack before it in the log (`""` for the first). Replacing tail packs makes the first of their replacements follow the pack before them. |
| `replaces` | The tail packs it replaces, if any (on the first of a replacement's packs). |
| `puts` | Each path put, with the hash and size of its bytes (a [table](#tables)). |
| `deletes` | Each path deleted. |

Packs are ordered page by page (a page's pointer and listing, then every
version's `variants.json` and variants), so the near-copies of a page from
different distributions are together, which compresses well: the
girlpages repository's 8 GiB of files come to about 540 MiB of packs.

In `manifest.json`, `packs` lists the packs in the log as it is now, in
order:

```json
"packs": [
  {"path": "packs/9f1c….tar.zst", "sha256": "9f1c…", "bytes": 268435000, "seq": 1, "tail": false},
  {"path": "packs/c3a8….tar.zst", "sha256": "c3a8…", "bytes": 1840221, "seq": 2, "tail": true}
]
```

`path` is where it is (its extension says how it's compressed: a reader
skips packs it can't read, and falls back to fetching files), `bytes` its
size, and `tail` whether it's a tail pack, which may be replaced.

A pack-only repository's `manifest.json` has just `format`, `generated`
and `packs`.

### Syncing

To mirror a repository, or keep up with one, keep the packs you've applied
(or their hashes):

1. Fetch `manifest.json`.
2. Fetch each pack it lists that you haven't applied, and check its hash
   and `prev`.
3. Apply them in order: write each put's bytes (checking its hash) and
   remove each delete.
4. Forget packs that a listed pack `replaces`. If you've applied a pack
   that's neither listed nor replaced by one that is, the log has started
   again: start again from nothing.

`girlrepo import` does this (merging what the packs hold into a repository
of your own), `girlrepo unpack` does it with pack files you already have,
and `girlrepo export` writes a pack-only repository.

## Recipes

**Find the girl version of a page installed here** (what `girl` does):

1. Compute the page's [source hash](#source-hashes).
2. Fetch `contents/ab/<source>/variants.json`. If it's not there (404),
   the repository doesn't have that version.
3. Take the `girl` generation with the highest `generation`, fetch
   `contents/ab/<source>/girl-<sha256>.json`, and check its hash.

**Find many** (what `girl --bulk` and `girl --import` do): first check each
against the [girl variant index](#variant-indexes), and only fetch those
it has.

**Read a page by name**, latest or as a source has it (what the website
does): fetch `page/name-<section>/<name>.json`, then the listing it names;
pick the version (`current`, or `sources[<key>]`); and fetch its variant by
the hash the listing gives, `contents/ab/<source>/<variant>-<sha>.json`.

**List a section**: fetch `manifest.json`, then its chapter's index (and
its shards, as they're scrolled to), keeping just that section's pages if
it's one of several (`3ssl` of `3`, `3p`, `3ssl`…).

**Mirror a repository**: fetch `manifest.json`; every chapter index; every
listing they name, every `variants.json` for every version in them, and
every generation those list; and the index manifests and their shards.
Skip anything you already have whose name is its hash. Write
`manifest.json` last. Or, if it has [packs](#packs), sync them: a few big
requests rather than millions. `girlrepo import` does either, merging into
a repository of your own.

## Publishing and caching

Publish in an order that never leaves a client following a pointer to
something that isn't there: immutable files first, then pointers, then
`manifest.json` last. Remove what nothing refers to any more only after
clients' caches of the old pointers have expired (a few minutes after the
upload, with the lifetimes below), never in the same step.

Recommended `Cache-Control`s (what `girlrepo serve` and the configurations
in `dist/hosting/` send):

| Files | |
|---|---|
| Named after their own hash: `page/page-*/…`, `section/*-<sha>.json`, `contents/*/*/<variant>-<sha>.json`, `index/…`, `packs/…` | `public, max-age=31536000, immutable` |
| `manifest.json`, `page/name-*/…`, `section/<section>.json`, `contents/*/*/variants.json` | `public, max-age=300` |

## Compatibility

Within format 1, a repository may gain:

* new optional fields in any document (readers ignore what they don't
  know);
* new variants, with names readers don't know (readers ignore them or show
  them by their `syntax`);
* new syntaxes (readers show unknown ones as plain text);
* new sources, sections and generations;
* [packs](#packs), and packs compressed other ways (with their own file
  extensions; readers ignore packs they can't read, and fetch the files
  instead).

Anything that would make a format 1 reader misread a repository (removing
or changing the meaning of a field, moving files, changing the source hash)
needs a new format number.
