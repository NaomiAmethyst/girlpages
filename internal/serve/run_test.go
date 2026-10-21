// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package serve

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/archive/build"
)

func status(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The server starts before there's an archive, picks up the first one
// written, and stops when it's told to.
func TestRun(t *testing.T) {
	site, dir := t.TempDir(), filepath.Join(t.TempDir(), "archive")
	put(t, site, "index.html", "<!doctype html><p>hi bestie</p>")
	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(chan [2]net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, RunOptions{Listen: "127.0.0.1:0", AdminListen: "127.0.0.1:0", Archive: dir,
			ReloadInterval: 20 * time.Millisecond, Drain: time.Second,
			Ready: func(s, a net.Addr) { addrs <- [2]net.Addr{s, a} }})
	}()
	a := <-addrs
	site0, admin := "http://"+a[0].String(), "http://"+a[1].String()
	if c, _ := status(t, admin+"/readyz"); c != 503 {
		t.Errorf("ready with no archive: %d", c)
	}
	if c, _ := status(t, site0+"/"); c != 503 {
		t.Errorf("serving with no archive: %d", c)
	}
	if _, err := build.Write(context.Background(), site, dir, build.Options{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, _ := status(t, admin+"/readyz"); c == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c, body := status(t, site0+"/"); c != 200 || body != "<!doctype html><p>hi bestie</p>" {
		t.Errorf("site: %d %q", c, body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("didn't stop")
	}
}
