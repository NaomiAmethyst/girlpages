# The girlpages archive format

An archive holds a published girlpages site (the website and the
repository in it) in a few big files, to be served straight off a network
file system, where a site's millions of small files are slow to write and
to serve. `girlrepo publish --archive` and `girlrepo archive` write one;
`girlserve` and `girlrepo serve --archive` serve it.

It's built for serving:

- Each file is stored in every form it's sent in (as it is, and zstd,
  brotli and gzip where those are smaller), so a request is one read of the
  bytes to send, with no decompression.
- A version of the site is an index of where every file's bytes are, and
  the archive names its current version in a small file servers watch, so
  switching versions is one atomic rename.
- Versions share bytes: a new one stores only the files whose content
  wasn't there already, and points at the rest where they are.

## Files

| Path | Changes? | What |
| --- | --- | --- |
| `current` | yes | [The current version](#current) |
| `index-<sha>.idx` | no | [An index](#indexes): one version of the site |
| `segments/<sha>.seg` | no | [A segment](#segments): files' bytes |
| `.lock` | | Held by a writer while it writes |
| `.tmp-*`, `segments/.tmp-*` | | Being written; left by writers that stopped, removed later |

`<sha>` is the lower-case hex SHA-256 of the whole file. Indexes and
segments never change once written, so they can be copied (rsync, an
object store) and cached forever; only `current` changes.

## current

JSON, replaced atomically (written to a temporary file, synced, renamed):

```json
{
  "format": 1,
  "index": "index-0f3a….idx",
  "generated": "2026-10-21T20:08:25Z"
}
```

| Field | Meaning |
| --- | --- |
| `format` | 1. Readers refuse other formats. |
| `index` | The current version's index, in the archive's directory. |
| `generated` | When it was written. |

A server reads `current` now and then (by default every 10 seconds); when
it names an index other than the one being served, the server reads that
index, opens its segments, checks their sizes, and switches to it. If any of
that fails it goes on serving what it was.

## Segments

A segment starts with the 8 bytes `GPASEG1\n`; the rest is files' bytes,
concatenated, in whatever order they were added. Nothing in a segment says
what its bytes are: indexes do. A writer starts a new segment when one would
pass a size limit (by default 1 GiB).

## Indexes

An index file is the 8 bytes `GPAIDX1\n`, then one zstd frame of:

1. the header's length, as an unsigned
   [varint](https://protobuf.dev/programming-guides/encoding/#varints);
2. the header, JSON;
3. the records, one per file, sorted by path (byte order).

### The header

```json
{
  "format": 1,
  "generated": "2026-10-21T20:08:25Z",
  "segments": [{"sha256": "f369…", "bytes": 1073741824}, …],
  "entries": 221589,
  "live": 7340032000
}
```

| Field | Meaning |
| --- | --- |
| `format` | 1 |
| `generated` | When the version was written. |
| `segments` | The segments records' bytes are in (each by hash, with its size); records refer to them by their place in this list. |
| `entries` | How many records there are. |
| `live` | How many bytes of the segments the records use (each range once). |

### Records

Each record is, with numbers as unsigned varints:

| Field | Size | Meaning |
| --- | --- | --- |
| shared | varint | How many bytes of the path are the same as the record before's |
| length | varint | How many bytes of the path follow |
| suffix | length | The rest of the path |
| hash | 16 | The first 16 bytes of the SHA-256 of the file (as it is) |
| mask | 1 | Which forms it has: bit 0 as it is (always set), 1 zstd, 2 brotli, 3 gzip |
| ranges | | For each form in the mask, in that order: segment (its place in the header's list), offset (from the segment's start), length |

The path is the file's slash-separated path from the site's root
(`man/1/ls/index.html`, `repo/manifest.json`). Every 16th record (the
first, the 17th, …) has `shared` 0, so a reader can binary search those and
read at most 16 records from one to find a path. Directories aren't
recorded: a server serves `dir/index.html` for `dir/` (and redirects `dir`
to `dir/`).

The hash identifies a file's content: two paths with the same content share
their bytes, and a new version reuses the bytes of any file whose content
the previous version had. Servers use it, with the form, as a strong ETag
(`"<hash>-br"`).

A compressed form is stored only if it's smaller than the file. A server
sends the smallest form a client accepts.

## Writing

A writer, holding `.lock`:

1. reads the current index, if there is one, to know what content the
   archive has and where;
2. reads every file of the site, leaving out hidden files (names starting
   with `.`) and compressed copies a publish left beside their files
   (`index.html.br`), as the archive makes its own;
3. for content it doesn't have, compresses it (if it's a kind worth it:
   HTML, JSON, JavaScript, CSS, XML, SVG, text, web manifests, of at least
   256 bytes) and appends its forms to new segments, which are synced and
   named by their hashes when they're finished;
4. if less than half of the bytes of the segments the new version uses are
   in use, compacts: copies what's used into new segments;
5. writes the new index (synced, then renamed into place), then `current`.

Then it collects garbage: it keeps the current index, the newest few
(by default 5) and any written within a while (by default an hour), and
removes other indexes; and removes segments none of the kept indexes use
that haven't changed for that while, so servers still reading an older
version have time to switch.

## Serving

`girlserve` (and `girlrepo serve --archive`) keep the current index in
memory and the segments open, and answer each request with one read of the
bytes it sends: `Content-Encoding` the smallest form the client accepts,
`Cache-Control` by the kind of file (as `girlrepo(1)` describes), the ETag
above, and range requests. A request is served from the version it started
on, even if a reload happens meanwhile, and segments a new version doesn't
use stay open for a while for requests still reading them.
