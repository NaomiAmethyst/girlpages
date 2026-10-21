// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package build

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// PutPacks replays the log of packs in dir into the stage, under prefix
// (the repository's place in the site: "repo/"), as unpacking them into an
// empty repository would; and puts the packs themselves there too, with
// their log, as a repository unpacked from them and published with
// publish --packs lists them. It returns the log.
func (st *Stage) PutPacks(ctx context.Context, dir, prefix string) ([]repo.PackEntry, error) {
	entries, files, err := repo.PackFiles(dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type job struct {
		path string
		data []byte
	}
	jobs := make(chan job, st.o.Jobs*4)
	var (
		workers, pending sync.WaitGroup
		failed           atomic.Pointer[error]
		done, total      atomic.Int64
	)
	fail := func(err error) {
		if failed.CompareAndSwap(nil, &err) {
			cancel()
		}
	}
	for w := 0; w < st.o.Jobs; w++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := range jobs {
				if ctx.Err() == nil {
					if err := st.Put(prefix+j.path, j.data); err != nil {
						fail(err)
					}
				}
				if n := done.Add(1); n%1000 == 0 {
					st.report("unpacking into the archive", int(n), int(total.Load()))
				}
				pending.Done()
			}
		}()
	}
	_, err = repo.ReplayPacks(ctx, dir, func(i int, e repo.PackEntry, h *repo.PackHeader) error {
		// What the packs before put is all in before this one's deletes
		// and puts, which come after.
		pending.Wait()
		total.Add(int64(h.Puts))
		for _, d := range h.Deletes {
			st.Delete(prefix + d)
		}
		return nil
	}, func(path string, data []byte) error {
		pending.Add(1)
		select {
		case jobs <- job{path, data}:
			return nil
		case <-ctx.Done():
			pending.Done()
			return ctx.Err()
		}
	})
	pending.Wait()
	close(jobs)
	workers.Wait()
	if p := failed.Load(); p != nil {
		return nil, *p
	}
	if err != nil {
		return nil, err
	}

	for i, e := range entries {
		b, err := os.ReadFile(files[i])
		if err != nil {
			return nil, err
		}
		if repo.Hash(b) != e.SHA256 {
			return nil, &fs.PathError{Op: "read", Path: files[i], Err: fs.ErrInvalid}
		}
		if err := st.Put(prefix+e.Path, b); err != nil {
			return nil, err
		}
	}
	log, err := json.MarshalIndent(repo.PackLog{Format: repo.Format, Packs: entries}, "", "  ")
	if err != nil {
		return nil, err
	}
	return entries, st.Put(prefix+repo.PackLogPath(), append(log, '\n'))
}

// Files is the stage, under a prefix, as where a repository store or a
// site build keeps its files.
func (st *Stage) Files(prefix string) repo.Files { return stageFiles{st, prefix} }

type stageFiles struct {
	st     *Stage
	prefix string
}

func (f stageFiles) ReadFile(rel string) ([]byte, error)  { return f.st.Get(f.prefix + rel) }
func (f stageFiles) WriteFile(rel string, b []byte) error { return f.st.Put(f.prefix+rel, b) }
func (f stageFiles) Exists(rel string) bool               { return f.st.Has(f.prefix + rel) }
func (f stageFiles) ReadDir(rel string) ([]fs.DirEntry, error) {
	return f.st.ReadDir(f.prefix + rel)
}
