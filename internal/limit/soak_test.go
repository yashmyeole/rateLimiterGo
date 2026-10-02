package limit

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestSoak feeds the limiter a steady stream of new client IPs (5,000 a second, three
// requests each) and logs memory every 10s, with and without the janitor. It is skipped
// unless SOAK is set to a duration:
//
//	SOAK=2m  go test -run 'TestSoak/no-janitor' -v -timeout 0 ./internal/limit
//	SOAK=10m go test -run 'TestSoak/^janitor$' -v -timeout 0 ./internal/limit
func TestSoak(t *testing.T) {
	d, err := time.ParseDuration(os.Getenv("SOAK"))
	if err != nil {
		t.Skip("set SOAK to a duration to run, e.g. SOAK=10m")
	}
	t.Run("no-janitor", func(t *testing.T) { soak(t, d, false) })
	t.Run("janitor", func(t *testing.T) { soak(t, d, true) })
}

func soak(t *testing.T, d time.Duration, janitor bool) {
	tb, err := NewTokenBucket(10, 20) // the proxy's defaults
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if janitor {
		go tb.RunJanitor(ctx, 30*time.Second) // the proxy's interval
	}

	const newClientsPerTick = 500 // every 100ms: 5,000 a second
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	start := time.Now()
	nextSample := start
	clients := 0
	for time.Since(start) < d {
		<-ticker.C
		for range newClientsPerTick {
			ip := fmt.Sprintf("10.%d.%d.%d", clients>>16&255, clients>>8&255, clients&255)
			for range 3 {
				tb.Allow(ctx, ip)
			}
			clients++
		}

		if time.Since(nextSample) >= 0 {
			runtime.GC() // measure live memory, not garbage waiting to be collected
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			t.Logf("t=%4.0fs  clients seen %8d  buckets %8d  heap in use %6.1f MB",
				time.Since(start).Seconds(), clients, size(tb), float64(ms.HeapInuse)/1e6)
			nextSample = nextSample.Add(10 * time.Second)
		}
	}
}
