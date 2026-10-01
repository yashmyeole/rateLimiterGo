package proxy

import (
	"net/url"
	"slices"
	"sync"
	"testing"
)

// testPool returns a pool over three fake backends: b1, b2, b3.
func testPool(t *testing.T) *Pool {
	t.Helper()
	pool, err := NewPool([]*url.URL{
		{Scheme: "http", Host: "b1"},
		{Scheme: "http", Host: "b2"},
		{Scheme: "http", Host: "b3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func hosts(urls []*url.URL) []string {
	var hs []string
	for _, u := range urls {
		hs = append(hs, u.Host)
	}
	return hs
}

func TestNewPoolRejectsEmpty(t *testing.T) {
	if _, err := NewPool(nil); err == nil {
		t.Error("NewPool(nil) returned no error")
	}
}

func TestSetHealthy(t *testing.T) {
	pool := testPool(t)
	b2 := pool.All()[1]

	steps := []struct {
		name        string
		u           *url.URL
		ok          bool
		wantChanged bool
		wantHealthy []string
	}{
		{"b2 goes down", b2, false, true, []string{"b1", "b3"}},
		{"b2 still down", b2, false, false, []string{"b1", "b3"}},
		{"b2 comes back, order kept", b2, true, true, []string{"b1", "b2", "b3"}},
		{"same URL, different pointer", &url.URL{Scheme: "http", Host: "b2"}, false, true, []string{"b1", "b3"}},
		{"unknown backend is ignored", &url.URL{Scheme: "http", Host: "nope"}, false, false, []string{"b1", "b3"}},
	}

	// Steps build on each other, so they run in order inside one test.
	for _, s := range steps {
		if got := pool.SetHealthy(s.u, s.ok); got != s.wantChanged {
			t.Errorf("%s: changed = %v, want %v", s.name, got, s.wantChanged)
		}
		if got := hosts(pool.Healthy()); !slices.Equal(got, s.wantHealthy) {
			t.Errorf("%s: healthy = %v, want %v", s.name, got, s.wantHealthy)
		}
	}
}

// One goroutine flips b2 up and down while others read, like the health checker and
// request goroutines do. Under -race this fails if any access skips the lock.
func TestPoolConcurrentReadersAndWriter(t *testing.T) {
	pool := testPool(t)
	b2 := pool.All()[1]

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 1000 {
			pool.SetHealthy(b2, i%2 == 1)
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 1000 {
				got := hosts(pool.Healthy())
				if !slices.Equal(got, []string{"b1", "b3"}) && !slices.Equal(got, []string{"b1", "b2", "b3"}) {
					t.Errorf("reader saw %v", got)
					return
				}
			}
		})
	}
	wg.Wait()
}
