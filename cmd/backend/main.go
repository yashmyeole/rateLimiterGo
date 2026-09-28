// Command backend is a small fake API used as a target for the proxy.
// Run several copies on different ports: they serve identical routes, like
// replicas of one service, and each names itself in the X-Backend header.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// backend holds one instance's settings. Handlers are methods on it (handlers.go).
type backend struct {
	name   string
	delay  time.Duration
	jitter time.Duration
}

func main() {
	name := flag.String("name", "api-1", "instance name, sent back in the X-Backend header")
	addr := flag.String("addr", "localhost:9001", "listen address (use :9001 to accept connections from other machines)")
	delay := flag.Duration("delay", 0, "fixed delay added to every /api response, e.g. 200ms")
	jitter := flag.Duration("jitter", 0, "extra random delay, anywhere from 0 up to this value")
	flag.Parse()

	b := &backend{name: *name, delay: *delay, jitter: *jitter}

	srv := &http.Server{
		Addr:    *addr,
		Handler: b.routes(),
		// The zero value for every timeout is "wait forever", which lets a slow or
		// malicious client hold connections open indefinitely.
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		// WriteTimeout runs while the handler works, so it has to outlast the simulated delay.
		WriteTimeout: *delay + *jitter + 5*time.Second,
		IdleTimeout:  60 * time.Second,
	}

	slog.Info("backend listening", "name", b.name, "addr", *addr, "delay", b.delay, "jitter", b.jitter)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
