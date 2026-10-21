// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/archive"
)

// RunOptions configures Run.
type RunOptions struct {
	// Listen is the site's address; AdminListen, if set, the address of
	// /healthz, /readyz and /metrics.
	Listen, AdminListen string
	// Archive, if set, is the archive to serve; otherwise Dir, a site
	// directory, is served.
	Archive, Dir string
	// ReloadInterval is how often the archive is checked for a new
	// version (zero: only on SIGHUP).
	ReloadInterval time.Duration
	// AccessLog, if set, gets a line of JSON per request.
	AccessLog io.Writer
	// DrainDelay is how long to keep serving after being told to stop,
	// with /readyz failing, so load balancers stop sending requests
	// first; Drain is then how long in-flight requests get to finish.
	DrainDelay, Drain time.Duration
	// Log gets what happens (reloads, errors).
	Log io.Writer
	// Ready, if set, is told the addresses once it's listening.
	Ready func(site, admin net.Addr)
}

// Run serves until ctx is done, then shuts down gracefully.
func Run(ctx context.Context, o RunOptions) error {
	if o.Log == nil {
		o.Log = io.Discard
	}
	logf := func(format string, a ...any) { fmt.Fprintf(o.Log, format+"\n", a...) }
	var src Source
	var arch *archive.Archive
	if o.Archive != "" {
		arch = archive.New(o.Archive)
		if _, err := arch.Reload(); err != nil {
			// Not there yet (or broken): serve 503s until it is.
			logf("archive %s: %v; not ready until it can be loaded", o.Archive, err)
		} else {
			logf("archive %s: serving %s (%d files, written %s)", o.Archive, arch.Current().Index.SHA256[:12],
				arch.Current().Index.Entries, arch.Current().Index.Generated.Format(time.RFC3339))
		}
		defer arch.Close()
		src = ArchiveSource{arch}
	} else {
		src = DirSource(o.Dir)
	}
	draining := &atomic.Bool{}
	m := NewMetrics()

	ln, err := net.Listen("tcp", o.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           Handler(src, Options{AccessLog: o.AccessLog, Metrics: m}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		// No WriteTimeout: packs are hundreds of megabytes, and slow
		// clients are allowed to take their time.
	}
	errs := make(chan error, 2)
	go func() { errs <- srv.Serve(ln) }()
	var adm *http.Server
	var admAddr net.Addr
	if o.AdminListen != "" {
		aln, err := net.Listen("tcp", o.AdminListen)
		if err != nil {
			srv.Close()
			return err
		}
		admAddr = aln.Addr()
		adm = &http.Server{Handler: Admin(drainable{src, draining}, m, arch), ReadHeaderTimeout: 10 * time.Second}
		go func() { errs <- adm.Serve(aln) }()
	}
	if o.Ready != nil {
		o.Ready(ln.Addr(), admAddr)
	}

	if arch != nil {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)
		reload := make(chan struct{}, 1)
		go func() {
			for range hup {
				select {
				case reload <- struct{}{}:
				default:
				}
			}
		}()
		go arch.Watch(ctx, o.ReloadInterval, reload, func(changed bool, err error) {
			if err != nil {
				logf("archive reload failed (still serving what was): %v", err)
				return
			}
			s := arch.Current()
			logf("archive: now serving %s (%d files, written %s)", s.Index.SHA256[:12], s.Index.Entries, s.Index.Generated.Format(time.RFC3339))
		})
	}

	select {
	case err := <-errs:
		srv.Close()
		if adm != nil {
			adm.Close()
		}
		return err
	case <-ctx.Done():
	}
	draining.Store(true)
	if o.DrainDelay > 0 {
		logf("stopping: not ready, still serving for %s", o.DrainDelay)
		time.Sleep(o.DrainDelay)
	}
	drain := o.Drain
	if drain <= 0 {
		drain = 30 * time.Second
	}
	sctx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	err = srv.Shutdown(sctx)
	if adm != nil {
		adm.Shutdown(sctx)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		logf("stopping: %s wasn't long enough for every request to finish", drain)
		return nil
	}
	return err
}

// drainable is a source that isn't ready once it's draining.
type drainable struct {
	Source
	draining *atomic.Bool
}

func (d drainable) Ready() bool { return !d.draining.Load() && d.Source.Ready() }
