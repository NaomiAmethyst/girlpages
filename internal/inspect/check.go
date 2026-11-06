// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package inspect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// decode decodes a file as what it should be, reporting if it can't. A field
// it doesn't know is only odd, as readers have to cope with those.
func decode(data []byte, v any, report Report) bool {
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(v); err == nil {
		return true
	} else if lenient := json.Unmarshal(data, v); lenient != nil {
		report(false, "it isn't what it should be: %v", lenient)
		return false
	} else {
		report(true, "%v", err)
		return true
	}
}

// Verify checks n itself: not what it refers to.
func (n *Node) Verify(ctx context.Context, report Report) {
	var data []byte
	if !n.big && (n.File != "" || n.data != nil) {
		var err error
		if data, err = n.JSON(ctx); err != nil {
			report(false, "%v", err)
			return
		}
		if n.Hash != "" && repo.Hash(data) != n.Hash {
			report(false, "it doesn't match its hash: it's %s", repo.Hash(data))
		}
		if n.Bytes != 0 && int64(len(data)) != n.Bytes {
			report(false, "it's %d bytes, not %d", len(data), n.Bytes)
		}
		var doc struct {
			Format int `json:"format"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			report(false, "it isn't JSON: %v", err)
			return
		}
		if doc.Format != repo.Format {
			report(false, "it's format %d, not %d", doc.Format, repo.Format)
		}
	}
	if n.check != nil {
		n.check(ctx, data, report)
	}
}

// CheckOptions says how far to check.
type CheckOptions struct {
	// Recursive checks what's under something too.
	Recursive bool
	// Sample, if it's not 0, keeps to this many of what's under anything, at
	// random.
	Sample int
	// Packs goes through packs too, as they're big.
	Packs bool
	// Jobs is how many to check at once.
	Jobs int
	// Problem is told each thing found wrong with something.
	Problem func(n *Node, warn bool, message string)
	// Checked is told as each thing's done.
	Checked func(n *Node)
}

// CheckStats says what was found.
type CheckStats struct {
	Checked, Problems, Warnings int64
}

// Check checks n, and if asked to, what's under it. It goes depth first, so
// that what's waiting its turn is only what's under the few things being
// checked, not most of the repository.
func (r *Repo) Check(ctx context.Context, n *Node, o CheckOptions) CheckStats {
	if o.Jobs < 1 {
		o.Jobs = 1
	}
	var st CheckStats
	var seen sync.Map
	visit := func(c *Node) []*Node {
		// What many things refer to is only checked once.
		if c.File != "" {
			if _, dup := seen.LoadOrStore(c.Kind+" "+c.File, true); dup {
				return nil
			}
		}
		c.Verify(ctx, func(warn bool, format string, a ...any) {
			if warn {
				atomic.AddInt64(&st.Warnings, 1)
			} else {
				atomic.AddInt64(&st.Problems, 1)
			}
			if o.Problem != nil {
				o.Problem(c, warn, fmt.Sprintf(format, a...))
			}
		})
		atomic.AddInt64(&st.Checked, 1)
		if o.Checked != nil {
			o.Checked(c)
		}
		if !o.Recursive || c.NoWalk {
			return nil
		}
		var under []*Node
		err := c.Children(ctx, func(u *Node) error {
			// (A part of something, with nothing to check of its own, has
			// been checked with it: there are a lot of words.)
			if u.View || (u.Kind == "pack" && !o.Packs) || (u.File == "" && u.check == nil && u.children == nil) {
				return nil
			}
			under = append(under, u)
			return nil
		})
		if err != nil && ctx.Err() == nil {
			atomic.AddInt64(&st.Problems, 1)
			if o.Problem != nil {
				o.Problem(c, false, "what's under it: "+err.Error())
			}
		}
		if o.Sample > 0 && len(under) > o.Sample {
			rand.Shuffle(len(under), func(i, j int) { under[i], under[j] = under[j], under[i] })
			under = under[:o.Sample]
		}
		return under
	}

	var mu sync.Mutex
	more := sync.NewCond(&mu)
	waiting := []*Node{n}
	busy := 0
	var wg sync.WaitGroup
	wg.Add(o.Jobs)
	for range o.Jobs {
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			for {
				for len(waiting) == 0 && busy > 0 {
					more.Wait()
				}
				if len(waiting) == 0 || ctx.Err() != nil {
					waiting = nil
					more.Broadcast()
					return
				}
				c := waiting[len(waiting)-1]
				waiting[len(waiting)-1] = nil
				waiting = waiting[:len(waiting)-1]
				busy++
				mu.Unlock()
				under := visit(c)
				mu.Lock()
				busy--
				// In reverse, so they're taken in order.
				for _, u := range slices.Backward(under) {
					waiting = append(waiting, u)
				}
				more.Broadcast()
			}
		}()
	}
	wg.Wait()
	return st
}

// Dump writes n's JSON, and if depth is more than 0, what's that many levels
// under it, each as a line of JSON saying what it is.
func (r *Repo) Dump(ctx context.Context, n *Node, depth int, write func(addr, kind, file string, doc []byte) error) error {
	doc, err := n.JSON(ctx)
	if err != nil {
		return err
	}
	if err := write(n.Addr, n.Kind, n.File, doc); err != nil {
		return err
	}
	if depth <= 0 {
		return nil
	}
	return n.Children(ctx, func(c *Node) error {
		if c.View {
			return nil
		}
		return r.Dump(ctx, c, depth-1, write)
	})
}
