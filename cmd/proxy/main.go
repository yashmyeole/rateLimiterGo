// Command proxy is the reverse proxy in front of the backend services.
// For now it forwards every request to a single backend.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/yashmyeole/ratelimiter-go/internal/proxy"
)

func main() {
	addr := flag.String("addr", "localhost:8080", "listen address")
	backend := flag.String("backend", "http://localhost:9001", "backend URL to forward requests to")
	timeout := flag.Duration("timeout", 5*time.Second, "how long to wait for the backend to start answering before returning 504")
	flag.Parse()

	// url.Parse accepts almost anything ("localhost:9001" parses with scheme "localhost"),
	// so check the parts that matter.
	target, err := url.Parse(*backend)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		slog.Error("invalid -backend, want a URL like http://localhost:9001", "backend", *backend)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           proxy.New(target, *timeout),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Must outlast the backend timeout, or the proxy would cut off its own 504.
		WriteTimeout: *timeout + 5*time.Second,
		IdleTimeout:  60 * time.Second,
	}

	slog.Info("proxy listening", "addr", *addr, "backend", target.String(), "timeout", *timeout)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
