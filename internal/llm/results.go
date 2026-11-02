// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
)

// A batch's results are saved as they're read, beside the batch (in
// <id>.results, a JSON object a line), before anything's done with them:
// some APIs keep a result only a while after first giving it (Sail, ten
// minutes), so one read and lost, by a run that stopped, is gone.

type savedResult struct {
	ID    string `json:"id"`
	Reply string `json:"reply,omitempty"`
	Error string `json:"error,omitempty"`
}

func (b *Batcher) resultsPath(id string) string {
	return strings.TrimSuffix(b.statePath(id), ".json") + ".results"
}

// loadResults reads what's been saved of a batch's results.
func (b *Batcher) loadResults(id string) (map[string]BatchResult, error) {
	out := map[string]BatchResult{}
	f, err := os.Open(b.resultsPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		var r savedResult
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue // a line cut short when a run stopped
		}
		br := BatchResult{Reply: r.Reply}
		switch r.Error {
		case "":
		case ErrExpired.Error():
			br.Err = ErrExpired // (so it isn't redone live)
		default:
			br.Err = errors.New(r.Error)
		}
		out[r.ID] = br
	}
	return out, sc.Err()
}

// resultSaver appends results to a batch's file as they come.
type resultSaver struct {
	mu  sync.Mutex
	f   *os.File
	err error
}

func (b *Batcher) saveResults(id string) (*resultSaver, error) {
	f, err := os.OpenFile(b.resultsPath(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &resultSaver{f: f}, nil
}

func (s *resultSaver) save(id string, r BatchResult) {
	sr := savedResult{ID: id, Reply: r.Reply}
	if r.Err != nil {
		sr.Error = r.Err.Error()
	}
	line, err := json.Marshal(sr)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		_, err = s.f.Write(append(line, '\n'))
	}
	if err != nil && s.err == nil {
		s.err = err
	}
}

func (s *resultSaver) close() error {
	err := s.f.Close()
	if s.err != nil {
		return s.err
	}
	return err
}
