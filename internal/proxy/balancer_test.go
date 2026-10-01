package proxy

import (
	"slices"
	"sync"
	"testing"
)

func picks(b Balancer, n int) []string {
	var got []string
	for range n {
		if u := b.Pick(); u != nil {
			got = append(got, u.Host)
		} else {
			got = append(got, "<nil>")
		}
	}
	return got
}

func TestRoundRobinOrder(t *testing.T) {
	got := picks(NewRoundRobin(testPool(t)), 7)
	if want := []string{"b1", "b2", "b3", "b1", "b2", "b3", "b1"}; !slices.Equal(got, want) {
		t.Errorf("picks = %v, want %v", got, want)
	}
}

func TestRoundRobinSkipsUnhealthy(t *testing.T) {
	pool := testPool(t)
	rr := NewRoundRobin(pool)
	b2 := pool.All()[1]

	pool.SetHealthy(b2, false)
	if got := picks(rr, 4); slices.Contains(got, "b2") {
		t.Errorf("picked unhealthy b2: %v", got)
	}

	pool.SetHealthy(b2, true)
	if got := picks(rr, 3); !slices.Contains(got, "b2") {
		t.Errorf("b2 is healthy again but wasn't picked: %v", got)
	}
}

func TestRoundRobinNoneHealthy(t *testing.T) {
	pool := testPool(t)
	for _, u := range pool.All() {
		pool.SetHealthy(u, false)
	}
	if u := NewRoundRobin(pool).Pick(); u != nil {
		t.Errorf("Pick() = %v, want nil", u)
	}
}

// Concurrent requests call Pick at the same time. Every counter value is handed out
// exactly once, so 9000 picks over 3 backends must be exactly 3000 each. With a
// non-atomic counter, picks get lost and `go test -race` reports a data race.
func TestRoundRobinConcurrentPicksAreEven(t *testing.T) {
	rr := NewRoundRobin(testPool(t))
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
