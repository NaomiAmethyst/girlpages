// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package ingest

import (
	"os"
	"path/filepath"

	"github.com/NaomiAmethyst/girlpages/internal/llm"
)

// partDir keeps converted parts of long pages in a directory, one file per
// part, until their page is done.
type partDir string

func (d partDir) path(key string) string { return filepath.Join(string(d), key[:2], key+".part") }

func (d partDir) Part(key string) (string, bool) {
	b, err := os.ReadFile(d.path(key))
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}

func (d partDir) PutPart(key, out string) error {
	path := d.path(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(out); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// forget removes a finished page's parts.
func (d partDir) forget(b llm.Backend, parts []llm.Part) {
	for _, p := range parts {
		_ = os.Remove(d.path(llm.PartKey(b, p)))
	}
}
