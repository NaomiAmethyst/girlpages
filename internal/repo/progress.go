// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import "sync/atomic"

// Progress is told how a long job is going: what it's doing (a stage, such
// as "reading listings"), how much of it is done, and of how much (0 if
// that's not known). It may be called from many goroutines at once.
type Progress func(stage string, done, total int)

// counter reports progress through a stage, safely from many goroutines.
type counter struct {
	p     Progress
	stage string
	total int
	done  atomic.Int64
}

func (s *Store) counter(stage string, total int) *counter {
	c := &counter{p: s.Progress, stage: stage, total: total}
	c.report(0)
	return c
}

func (c *counter) add() { c.report(int(c.done.Add(1))) }

func (c *counter) report(done int) {
	if c.p != nil {
		c.p(c.stage, done, c.total)
	}
}
