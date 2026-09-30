package proxy

import (
	"net/url"
	"sync"
	"testing"
)

func testBackends() []*url.URL {
	return []*url.URL{
		{Scheme: "http", Host: "b1"},
		{Scheme: "http", Host: "b2"},
		{Scheme: "http", Host: "b3"},
	}
}

func TestRoundRobinOrder(t *testing.T) {
	rr, err := NewRoundRobin(testBackends())
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"b1", "b2", "b3", "b1", "b2", "b3", "b1"} {
		if got := rr.Pick().Host; got != want {
			t.Errorf("pick %d = %s, want %s", i+1, got, want)
		}
	}
}

func TestRoundRobinRejectsEmpty(t *testing.T) {
	if _, err := NewRoundRobin(nil); err == nil {
		t.Error("NewRoundRobin(nil) returned no error")
	}
}

// Concurrent requests call Pick at the same time. Every counter value is handed out
// exactly once, so 9000 picks over 3 backends must be exactly 3000 each. With a
// non-atomic counter, picks get lost and `go test -race` reports a data race.
func TestRoundRobinConcurrentPicksAreEven(t *testing.T) {
	rr, err := NewRoundRobin(testBackends())
	if err != nil {
		t.Fatal(err)
	}
	const workers, picksEach = 9, 1000

	perWorker := make([]map[string]int, workers) // each goroutine writes only its own slot
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() { // runs the function in a new goroutine and tracks it
			counts := map[string]int{}
			for range picksEach {
				counts[rr.Pick().Host]++
			}
			perWorker[w] = counts
		})
	}
	wg.Wait() // blocks until every goroutine started with wg.Go has returned

	total := map[string]int{}
	for _, counts := range perWorker {
		for host, n := range counts {
			total[host] += n
		}
	}
	want := workers * picksEach / 3
	for _, host := range []string{"b1", "b2", "b3"} {
		if total[host] != want {
			t.Errorf("%s picked %d times, want %d (all: %v)", host, total[host], want, total)
		}
	}
}
