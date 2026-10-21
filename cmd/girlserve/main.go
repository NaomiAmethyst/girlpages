// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Command girlserve serves a published girlpages site, from an archive
// (as girlrepo publish --archive writes) or a directory: the website and
// repository, compressed copies by Accept-Encoding, with health checks,
// metrics and reloads when the archive changes. It's girlrepo serve on its
// own, for production: small and static, with nothing else in it.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/serve"
	"github.com/spf13/pflag"
)

// version is set by the Makefile.
var version = ""

func getVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

const usage = `girlserve serves a published girlpages site.

usage: girlserve [options] (--archive DIR | --dir DIR)

options:
`

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	fs := pflag.NewFlagSet("girlserve", pflag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	arch := fs.String("archive", os.Getenv("GIRLSERVE_ARCHIVE"), "the archive to serve ($GIRLSERVE_ARCHIVE)")
	dir := fs.String("dir", "", "a site directory to serve, instead")
	listen := fs.String("listen", envOr("GIRLSERVE_LISTEN", ":8080"), "address to serve the site on ($GIRLSERVE_LISTEN)")
	admin := fs.String("admin-listen", envOr("GIRLSERVE_ADMIN_LISTEN", ":9090"), "address for /healthz, /readyz and /metrics, or \"\" for none ($GIRLSERVE_ADMIN_LISTEN)")
	reload := fs.Duration("reload-interval", 10*time.Second, "how often to check the archive for a new version (0: only on SIGHUP)")
	accessLog := fs.Bool("access-log", false, "write a line of JSON per request to standard output")
	drain := fs.Duration("drain", 30*time.Second, "how long requests in flight get to finish when stopping")
	drainDelay := fs.Duration("drain-delay", 5*time.Second, "when stopping, keep serving this long first, with /readyz failing")
	showVersion := fs.Bool("version", false, "print the version")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 1
	}
	if *showVersion {
		fmt.Println("girlserve", getVersion())
		return 0
	}
	if (*arch == "") == (*dir == "") || fs.NArg() > 0 {
		fs.Usage()
		return 1
	}
	o := serve.RunOptions{Listen: *listen, AdminListen: *admin, Archive: *arch, Dir: *dir,
		ReloadInterval: *reload, Drain: *drain, DrainDelay: *drainDelay, Log: os.Stderr}
	if *accessLog {
		o.AccessLog = os.Stdout
	}
	o.Ready = func(site, adm net.Addr) {
		fmt.Fprintf(os.Stderr, "girlserve %s: serving on %s", getVersion(), site)
		if adm != nil {
			fmt.Fprintf(os.Stderr, ", admin on %s", adm)
		}
		fmt.Fprintln(os.Stderr)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve.Run(ctx, o); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "girlserve: %v\n", err)
		return 2
	}
	return 0
}

func envOr(name, def string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return def
}
