// Command proxy is the reverse proxy in front of the backend services. It rate-limits
// each client IP, spreads requests across the healthy backends in turn (round robin),
// probes each backend's /healthz in the background, and shuts down gracefully on
// Ctrl-C or SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/yashmyeole/ratelimiter-go/internal/limit"
	"github.com/yashmyeole/ratelimiter-go/internal/proxy"
)

// healthWorkers caps how many health probes run at once.
const healthWorkers = 4

func main() {
	addr := flag.String("addr", "localhost:8080", "listen address")
	// 127.0.0.1, not localhost: localhost resolves to IPv6 ::1 first, so any other program
	// listening on [::]:9001 (a Docker container, say) would get the traffic instead of
	// the backend, which listens on IPv4 only.
	backends := flag.String("backends", "http://127.0.0.1:9001,http://127.0.0.1:9002,http://127.0.0.1:9003",
		"comma-separated backend URLs, used in turn")
	timeout := flag.Duration("timeout", 5*time.Second, "how long to wait for a backend to start answering before returning 504")
	healthInterval := flag.Duration("health-interval", 2*time.Second, "how often to probe each backend's /healthz")
	healthTimeout := flag.Duration("health-timeout", time.Second, "how long a probe may take before the backend counts as down")
	rate := flag.Float64("rate", 10, "requests per second allowed per client IP; 0 turns rate limiting off")
	burst := flag.Int("burst", 20, "requests a client may make at once before -rate applies")
	flag.Parse()

	targets, err := parseBackends(*backends)
	if err != nil {
		slog.Error("invalid -backends", "err", err)
		os.Exit(1)
	}
	pool, err := proxy.NewPool(targets)
	if err != nil {
		slog.Error("invalid -backends", "err", err)
		os.Exit(1)
	}

	// ctx is canceled on Ctrl-C (SIGINT) or SIGTERM, which is what `docker stop`,
	// Kubernetes and most hosting platforms send before killing a process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go proxy.NewChecker(pool, *healthInterval, *healthTimeout, healthWorkers).Run(ctx)

	// The limiter wraps the proxy, so a request over the limit gets its 429 before any
	// backend is picked or contacted.
	var handler http.Handler = proxy.New(proxy.NewRoundRobin(pool), *timeout)
	if *rate > 0 {
		limiter, err := limit.NewTokenBucket(*rate, *burst)
		if err != nil {
			slog.Error("invalid -rate or -burst", "err", err)
			os.Exit(1)
		}
		handler = limit.Middleware(limiter, limit.ClientIP)(handler)
	} else {
		slog.Warn("rate limiting is off (-rate 0)")
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Must outlast the backend timeout, or the proxy would cut off its own 504.
		WriteTimeout: *timeout + 5*time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// ListenAndServe blocks until the server stops, so it runs in its own goroutine
	// and reports how it ended on a channel.
	slog.Info("proxy listening", "addr", *addr, "backends", *backends, "timeout", *timeout,
		"health_interval", *healthInterval, "rate", *rate, "burst", *burst)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	select {
	case err := <-serveErr: // it never started, e.g. the port is already in use
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	case <-ctx.Done():
	}
	stop() // restore default signal handling: a second Ctrl-C now kills immediately

	// Shutdown stops accepting connections, then waits for in-flight requests to finish.
	// The limit matches WriteTimeout, the longest any request can legitimately take.
	slog.Info("shutting down, waiting for in-flight requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), *timeout+5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown did not finish cleanly", "err", err)
		os.Exit(1)
	}
	slog.Info("stopped")
}

// parseBackends turns "http://a:1,http://b:2" into URLs. url.Parse accepts almost
// anything ("localhost:9001" parses with scheme "localhost"), so check the parts that matter.
func parseBackends(s string) ([]*url.URL, error) {
	var targets []*url.URL
	for _, raw := range strings.Split(s, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%q is not a backend URL like http://127.0.0.1:9001", raw)
		}
		targets = append(targets, u)
	}
	if len(targets) == 0 {
		return nil, errors.New("no backends given")
	}
	return targets, nil
}
