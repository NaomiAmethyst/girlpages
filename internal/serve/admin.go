// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package serve

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NaomiAmethyst/girlpages/internal/archive"
)

// Metrics counts what a Handler served, for Prometheus.
type Metrics struct {
	start time.Time
	bytes atomic.Int64

	mu       sync.Mutex
	byStatus map[int]int64
	// buckets counts requests by how long they took (upper bounds in
	// seconds), Prometheus style: each counts those at or under it.
	buckets []float64
	counts  []int64
	sum     float64
	n       int64
}

// NewMetrics starts counting.
func NewMetrics() *Metrics {
	b := []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
	return &Metrics{start: time.Now(), byStatus: map[int]int64{}, buckets: b, counts: make([]int64, len(b))}
}

func (m *Metrics) observe(status int, bytes int64, d time.Duration) {
	m.bytes.Add(bytes)
	s := d.Seconds()
	m.mu.Lock()
	m.byStatus[status]++
	for i, b := range m.buckets {
		if s <= b {
			m.counts[i]++
		}
	}
	m.sum += s
	m.n++
	m.mu.Unlock()
}

// write writes the metrics in Prometheus's text format, with the
// archive's, if there is one.
func (m *Metrics) write(w io.Writer, a *archive.Archive) {
	fmt.Fprintf(w, "# HELP girlpages_http_requests_total Requests served, by status.\n# TYPE girlpages_http_requests_total counter\n")
	m.mu.Lock()
	codes := make([]int, 0, len(m.byStatus))
	for c := range m.byStatus {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	for _, c := range codes {
		fmt.Fprintf(w, "girlpages_http_requests_total{code=\"%d\"} %d\n", c, m.byStatus[c])
	}
	fmt.Fprintf(w, "# HELP girlpages_http_request_duration_seconds How long requests took.\n# TYPE girlpages_http_request_duration_seconds histogram\n")
	for i, b := range m.buckets {
		fmt.Fprintf(w, "girlpages_http_request_duration_seconds_bucket{le=\"%g\"} %d\n", b, m.counts[i])
	}
	fmt.Fprintf(w, "girlpages_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", m.n)
	fmt.Fprintf(w, "girlpages_http_request_duration_seconds_sum %g\ngirlpages_http_request_duration_seconds_count %d\n", m.sum, m.n)
	m.mu.Unlock()
	fmt.Fprintf(w, "# HELP girlpages_http_response_bytes_total Bytes of response bodies sent.\n# TYPE girlpages_http_response_bytes_total counter\ngirlpages_http_response_bytes_total %d\n", m.bytes.Load())
	fmt.Fprintf(w, "# HELP girlpages_start_time_seconds When the server started.\n# TYPE girlpages_start_time_seconds gauge\ngirlpages_start_time_seconds %d\n", m.start.Unix())
	if a == nil {
		return
	}
	reloads, failures := a.Counts()
	fmt.Fprintf(w, "# HELP girlpages_archive_reloads_total Reloads that changed what's served.\n# TYPE girlpages_archive_reloads_total counter\ngirlpages_archive_reloads_total %d\n", reloads)
	fmt.Fprintf(w, "# HELP girlpages_archive_reload_failures_total Reloads that failed (what was served still is).\n# TYPE girlpages_archive_reload_failures_total counter\ngirlpages_archive_reload_failures_total %d\n", failures)
	if s := a.Current(); s != nil {
		fmt.Fprintf(w, "# HELP girlpages_archive_generated_time_seconds When the archive's version being served was written.\n# TYPE girlpages_archive_generated_time_seconds gauge\ngirlpages_archive_generated_time_seconds %d\n", s.Index.Generated.Unix())
		fmt.Fprintf(w, "# HELP girlpages_archive_loaded_time_seconds When it was loaded.\n# TYPE girlpages_archive_loaded_time_seconds gauge\ngirlpages_archive_loaded_time_seconds %d\n", s.Loaded.Unix())
		fmt.Fprintf(w, "# HELP girlpages_archive_files Files in it.\n# TYPE girlpages_archive_files gauge\ngirlpages_archive_files %d\n", s.Index.Entries)
		fmt.Fprintf(w, "# HELP girlpages_archive_live_bytes Bytes of its segments it uses.\n# TYPE girlpages_archive_live_bytes gauge\ngirlpages_archive_live_bytes %d\n", s.Index.Live)
	}
}

// Admin serves what an orchestrator and a monitoring system ask for, on
// its own address (so it's not public): /healthz (the process is up),
// /readyz (it has a site to serve) and /metrics (Prometheus).
func Admin(src Source, m *Metrics, a *archive.Archive) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !src.Ready() {
			http.Error(w, "no site to serve yet", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ready")
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.write(w, a)
	})
	return mux
}
