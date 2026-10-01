package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// waitFor polls cond until it's true, failing the test after 2s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCheckAllMarksEachBackend(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("probe hit %s, want /healthz", r.URL.Path)
		}
	}))
	defer ok.Close()

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done(): // the probe gave up
		}
	}))
	defer slow.Close()

	down := httptest.NewServer(http.NotFoundHandler())
	down.Close() // its port now refuses connections

	pool, err := NewPool([]*url.URL{
		mustParse(t, ok.URL), mustParse(t, failing.URL), mustParse(t, slow.URL), mustParse(t, down.URL),
	})
	if err != nil {
		t.Fatal(err)
	}
	NewChecker(pool, time.Hour, 100*time.Millisecond, 2).checkAll(context.Background())

	if got, want := hosts(pool.Healthy()), []string{mustParse(t, ok.URL).Host}; !slices.Equal(got, want) {
		t.Errorf("healthy = %v, want only the 200 backend %v", got, want)
	}
}

// Ten backends and three workers: no more than three probes may ever be in flight.
func TestCheckAllRespectsWorkerLimit(t *testing.T) {
	var mu sync.Mutex
	inFlight, peak := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()

		time.Sleep(20 * time.Millisecond) // hold the slot so probes overlap

		mu.Lock()
		inFlight--
		mu.Unlock()
	}))
	defer srv.Close()

	var backends []*url.URL
	for i := range 10 {
		backends = append(backends, mustParse(t, srv.URL).JoinPath("b", strconv.Itoa(i))) // /b/0 ... /b/9
	}
	pool, err := NewPool(backends)
	if err != nil {
		t.Fatal(err)
	}
	NewChecker(pool, time.Hour, time.Second, 3).checkAll(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if peak != 3 {
		t.Errorf("peak probes in flight = %d, want 3", peak)
	}
}

func TestRunDetectsFailureAndRecovery(t *testing.T) {
	var failing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	pool, err := NewPool([]*url.URL{mustParse(t, srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { NewChecker(pool, 20*time.Millisecond, time.Second, 1).Run(ctx) })

	failing.Store(true)
	waitFor(t, "backend marked down", func() bool { return len(pool.Healthy()) == 0 })
	failing.Store(false)
	waitFor(t, "backend marked up", func() bool { return len(pool.Healthy()) == 1 })

	cancel()
	wg.Wait() // hangs, and the test times out, if Run ignores cancellation
}
