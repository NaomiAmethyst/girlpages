// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package inspect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// packsGroup fills in the group of packs.
func (r *Repo) packsGroup(n *Node) {
	n.value = func(ctx context.Context) (any, error) {
		m, err := r.Manifest(ctx)
		if err != nil {
			return nil, err
		}
		return m.Packs, nil
	}
	n.children = func(ctx context.Context, yield func(*Node) error) error {
		m, err := r.Manifest(ctx)
		if err != nil {
			return err
		}
		for _, p := range m.Packs {
			if err := yield(r.pack(p)); err != nil {
				return err
			}
		}
		if _, err := r.fetch(ctx, repo.PackLogPath()); err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				return nil
			}
			return err
		}
		return yield(r.packLog())
	}
}

// packLog is packs/index.json, of a repository that writes its own packs.
func (r *Repo) packLog() *Node {
	return &Node{
		r: r, Kind: "pack-log", Addr: "packs/log", File: repo.PackLogPath(), Summary: "the packs, and replaced ones not gone yet",
		describe: func(ctx context.Context, w io.Writer) error {
			var l repo.PackLog
			if err := r.get(ctx, repo.PackLogPath(), &l); err != nil {
				return err
			}
			fmt.Fprintf(w, "packs: %d\nreplaced, not gone yet: %d\n", len(l.Packs), len(l.Retired))
			return nil
		},
		check: func(_ context.Context, data []byte, report Report) {
			var l repo.PackLog
			decode(data, &l, report)
		},
	}
}

func packSummary(e repo.PackEntry) string {
	s := size(e.Bytes)
	if e.Tail {
		s += ", tail"
	}
	return s
}

// scanPack reads a pack from the start, with functions as for repo.ScanPack.
func (r *Repo) scanPack(ctx context.Context, e repo.PackEntry, header func(*repo.PackHeader) error, put func(string, int64, io.Reader) error) error {
	f, err := r.C.Open(ctx, e.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	return repo.ScanPack(ctx, f, header, put)
}

func (r *Repo) packHeader(ctx context.Context, e repo.PackEntry) (*repo.PackHeader, error) {
	var h *repo.PackHeader
	err := r.scanPack(ctx, e, func(got *repo.PackHeader) error {
		h = got
		return repo.ErrStopScan
	}, nil)
	return h, err
}

// pack is a pack: what the manifest says of it, and its header, as it isn't
// JSON itself. What's in it is what the rest of a repository holds, and to get
// at any of it means reading through it from the start, so only checking it
// does that.
func (r *Repo) pack(e repo.PackEntry) *Node {
	return &Node{
		r: r, Kind: "pack", Addr: fmt.Sprintf("packs/%d", e.Seq), File: e.Path, Hash: e.SHA256, Bytes: e.Bytes, big: true,
		Summary: packSummary(e),
		value: func(ctx context.Context) (any, error) {
			h, err := r.packHeader(ctx, e)
			if err != nil {
				return nil, err
			}
			return map[string]any{"entry": e, "header": h}, nil
		},
		describe: func(ctx context.Context, w io.Writer) error {
			h, err := r.packHeader(ctx, e)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "seq: %d\ngenerated: %s\ntail: %v\nputs: %d files\ndeletes: %d\n", h.Seq, h.Generated.Format(time.RFC3339), h.Tail, h.Puts, len(h.Deletes))
			if h.Prev != "" {
				fmt.Fprintf(w, "prev: %s\n", h.Prev)
			}
			for _, x := range h.Replaces {
				fmt.Fprintf(w, "replaces: %s\n", x)
			}
			for _, d := range h.Deletes {
				fmt.Fprintf(w, "  deletes %s\n", d)
			}
			return nil
		},
		check: func(ctx context.Context, _ []byte, report Report) {
			f, err := r.C.Open(ctx, e.Path)
			if err != nil {
				report(false, "%v", err)
				return
			}
			defer f.Close()
			sum := sha256.New()
			var read countingWriter
			tee := io.TeeReader(f, io.MultiWriter(sum, &read))
			var h *repo.PackHeader
			err = repo.ScanPack(ctx, tee, func(got *repo.PackHeader) error {
				h = got
				return nil
			}, func(path string, _ int64, body io.Reader) error {
				if want := namedHash(path); want != "" {
					b, err := io.ReadAll(body)
					if err != nil {
						return err
					}
					if repo.Hash(b) != want {
						report(false, "%s doesn't match its hash", path)
					}
				}
				return nil
			})
			if err != nil {
				report(false, "%v", err)
				return
			}
			// What's left, after the tar file's end.
			if _, err := io.Copy(io.Discard, tee); err != nil {
				report(false, "%v", err)
				return
			}
			if e.SHA256 != "" && hex.EncodeToString(sum.Sum(nil)) != e.SHA256 {
				report(false, "it doesn't match its hash")
			}
			if e.Bytes != 0 && int64(read) != e.Bytes {
				report(false, "it's %d bytes, not %d", read, e.Bytes)
			}
			if h != nil && e.Seq != 0 && h.Seq != e.Seq {
				report(false, "its header says it's pack %d", h.Seq)
			}
		},
	}
}

type countingWriter int64

func (c *countingWriter) Write(b []byte) (int, error) {
	*c += countingWriter(len(b))
	return len(b), nil
}

// hashed matches the files named after their hashes.
var hashed = regexp.MustCompile(`(?:^|[-/])([0-9a-f]{64})\.json$`)

// namedHash is the hash a file's named after, if it is.
func namedHash(path string) string {
	if strings.HasSuffix(path, ".tar.zst") {
		return strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".tar.zst")
	}
	if m := hashed.FindStringSubmatch(path); m != nil && !strings.HasSuffix(path, "/variants.json") {
		// (contents/ab/<source>/variants.json isn't, and doesn't match.)
		return m[1]
	}
	return ""
}
