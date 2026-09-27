package main

import "fmt"

func main() {
	// A slice is Go's growable list. []string means "slice of strings".
	backends := []string{"users:9001", "orders:9002"}
	backends = append(backends, "quotes:9003") // append may return a new slice, so always reassign

	fmt.Println(len(backends), "backends:", backends)

	// range yields (index, value) pairs. Use _ for one you don't need.
	for i, addr := range backends {
		fmt.Println(i, addr)
	}

	// Phase 5's round-robin in one line: request n goes to backends[n % len].
	for n := range 6 { // ranging over an int counts 0..5
		fmt.Println("request", n, "->", backends[n%len(backends)])
	}

	// Slicing: s[low:high] is elements low..high-1, and it SHARES memory with the
	// original: writing through the small slice changes the big one too.
	firstTwo := backends[:2]
	firstTwo[0] = "users:9999"
	fmt.Println(backends[0]) // users:9999

	// backends[3] would crash with "index out of range"; Go checks every index.
}
