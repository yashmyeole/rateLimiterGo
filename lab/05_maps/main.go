package main

import "fmt"

func main() {
	// map[K]V is a hash table. The Phase 7 limiter is map[clientIP]*Bucket.
	// {} makes it ready to use; a bare `var m map[string]int` is nil, and writing to it panics.
	hits := map[string]int{}

	for _, ip := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "1.1.1.1", "3.3.3.3"} {
		hits[ip]++ // a missing key reads as the zero value (0), so ++ just works
	}
	fmt.Println(hits)

	// "comma ok": the second result says whether the key was really there.
	n, ok := hits["9.9.9.9"]
	fmt.Println("9.9.9.9 ->", n, ok) // 0 false: an unknown client, not one with 0 hits

	delete(hits, "2.2.2.2") // Phase 8's janitor does this to clients that went quiet
	fmt.Println(len(hits), "clients left")

	// Iteration order is not guaranteed. Go shuffles it on purpose so no code can rely on it.
	// A tiny map like this often repeats an order; with 10+ keys it changes almost every run.
	for ip, count := range hits {
		fmt.Println(ip, count)
	}
}
