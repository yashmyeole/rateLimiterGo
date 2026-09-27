package main

import (
	"fmt"
	"time"
)

// handle pretends to serve one request and times it. Phase 12's metrics middleware
// is this exact pattern: start a stopwatch, defer "record how long it took".
func handle(path string) {
	start := time.Now()

	// defer schedules a call for when handle returns, whichever return that is.
	// Same pattern as `defer resp.Body.Close()` in 02_fetch.
	// func() { ... }() is an anonymous function (a closure): it can read start and path.
	defer func() {
		fmt.Printf("  %s took %v\n", path, time.Since(start).Round(time.Millisecond))
	}()

	time.Sleep(30 * time.Millisecond) // pretend to wait on a slow backend
	if path == "/broken" {
		fmt.Println("  error, returning early")
		return // the deferred timer still runs
	}
	fmt.Println("  served", path)
}

func main() {
	handle("/api/users")
	handle("/broken")

	// Several defers run last-in, first-out, like a stack of plates.
	defer fmt.Println("deferred 1st -> runs last")
	defer fmt.Println("deferred 2nd -> runs first")

	// Gotcha: a deferred call's ARGUMENTS are evaluated right away; only the call waits.
	// That's why handle wraps time.Since in a closure: `defer fmt.Println(time.Since(start))`
	// would measure ~0s. (go vet catches that one: "call to time.Since is not deferred".)
	n := 1
	defer fmt.Println("n when deferred:", n) // prints 1, not 100
	n = 100
	fmt.Println("n now:", n)
}
