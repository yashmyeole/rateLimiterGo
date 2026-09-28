package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"
)

// routes maps URL patterns to handlers. A method in the pattern ("GET /api/users")
// makes the mux answer other methods with 405 and unknown paths with 404 on its own.
func (b *backend) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/users", b.list(users))
	mux.HandleFunc("GET /api/orders", b.list(orders))
	mux.HandleFunc("GET /api/quotes", b.list(quotes))
	mux.HandleFunc("GET /healthz", b.healthz)
	return mux
}

// list returns a handler that waits the simulated latency, then writes data as JSON.
// Returning a function that closes over data is the same shape middleware uses later.
func (b *backend) list(data any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		time.Sleep(b.latency())

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend", b.name)
		if err := json.NewEncoder(w).Encode(data); err != nil {
			slog.Error("write response", "path", r.URL.Path, "err", err)
			return
		}
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "took", time.Since(start))
	}
}

// healthz is what the proxy's health checker will poll. It doesn't log, so probes
// every couple of seconds don't flood the output.
func (b *backend) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Backend", b.name)
	fmt.Fprintln(w, "ok")
}

// latency is the simulated processing time: the fixed delay plus a random part of jitter.
func (b *backend) latency() time.Duration {
	if b.jitter <= 0 {
		return b.delay // rand.N panics on 0
	}
	return b.delay + rand.N(b.jitter)
}
