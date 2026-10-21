// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package site

import (
	"net/http"

	"github.com/NaomiAmethyst/girlpages/internal/serve"
)

// Cache lifetimes for each kind of file on a published site (see
// package serve, which serves them).
const (
	CacheImmutable  = serve.CacheImmutable
	CachePointer    = serve.CachePointer
	CacheSite       = serve.CacheSite
	CacheRevalidate = serve.CacheRevalidate
)

// CacheControl is the Cache-Control a file on the site should be served
// with.
func CacheControl(rel string) string { return serve.CacheControl(rel) }

// Handler serves a built site in dir the way its production server
// should (see serve.Handler).
func Handler(dir string) http.Handler { return serve.Handler(serve.DirSource(dir), serve.Options{}) }
