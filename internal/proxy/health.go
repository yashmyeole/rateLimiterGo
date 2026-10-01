package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Checker probes each backend's /healthz on an interval and records the result in a Pool.
type Checker struct {
	pool     *Pool
	client   *http.Client
	interval time.Duration
	workers  int
}

// NewChecker returns a Checker that probes every interval, gives each probe up to
// timeout, and runs at most workers probes at once.
func NewChecker(pool *Pool, interval, timeout time.Duration, workers int) *Checker {
	return &Checker{
		pool:     pool,
		client:   &http.Client{Timeout: timeout},
		interval: interval,
		workers:  max(workers, 1),
	}
}

// Run checks every backend right away, then once per interval, until ctx is canceled.
// It blocks, so start it with `go checker.Run(ctx)`.
func (c *Checker) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		c.checkAll(ctx)
		select { // wait for whichever happens first
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// checkAll probes every backend with a bounded worker pool: a fixed number of goroutines
// take backends from a channel, so even hundreds of backends mean at most c.workers
// probes in flight. It returns once every backend has been checked.
func (c *Checker) checkAll(ctx context.Context) {
	jobs := make(chan *url.URL)

	var wg sync.WaitGroup
	for range c.workers {
		wg.Go(func() {
			for u := range jobs { // loops until jobs is closed and empty
				err := c.probe(ctx, u)
				if ctx.Err() != nil {
					continue // shutting down: a canceled probe says nothing about the backend
				}
				if !c.pool.SetHealthy(u, err == nil) {
					continue // no change, nothing to log
				}
				if err == nil {
					slog.Info("backend up", "backend", u.Host)
				} else {
					slog.Warn("backend down", "backend", u.Host, "err", err)
				}
			}
		})
	}

	for _, u := range c.pool.All() {
		jobs <- u // blocks until a worker is free to take it
	}
	close(jobs) // tells the workers there's nothing more coming
	wg.Wait()
}

// probe returns nil if u answers GET /healthz with 200 before the client's timeout.
func (c *Checker) probe(ctx context.Context, u *url.URL) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.JoinPath("healthz").String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // read to the end so the connection can be reused

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
