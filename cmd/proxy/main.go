// Command proxy is the reverse proxy in front of the backend services.
// It spreads requests across the backends in turn (round robin).
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/yashmyeole/ratelimiter-go/internal/proxy"
)

func main() {
	addr := flag.String("addr", "localhost:8080", "listen address")
	backends := flag.String("backends", "http://localhost:9001,http://localhost:9002,http://localhost:9003",
		"comma-separated backend URLs, used in turn")
	timeout := flag.Duration("timeout", 5*time.Second, "how long to wait for a backend to start answering before returning 504")
	flag.Parse()

	targets, err := parseBackends(*backends)
	if err != nil {
		slog.Error("invalid -backends", "err", err)
		os.Exit(1)
	}
	balancer, err := proxy.NewRoundRobin(targets)
	if err != nil {
		slog.Error("invalid -backends", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           proxy.New(balancer, *timeout),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Must outlast the backend timeout, or the proxy would cut off its own 504.
		WriteTimeout: *timeout + 5*time.Second,
		IdleTimeout:  60 * time.Second,
	}

	slog.Info("proxy listening", "addr", *addr, "backends", *backends, "timeout", *timeout)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
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
			return nil, fmt.Errorf("%q is not a backend URL like http://localhost:9001", raw)
		}
		targets = append(targets, u)
	}
	if len(targets) == 0 {
		return nil, errors.New("no backends given")
	}
	return targets, nil
}
