package main

import (
	"fmt"
	"time"
)

type Bucket struct{ Tokens int }

// Functions can return several values. This is the exact shape of the Phase 7 limiter:
// "may this request pass?" plus "if not, how long should the client wait?"
func (b *Bucket) Allow() (ok bool, retryAfter time.Duration) {
	if b.Tokens > 0 {
		b.Tokens--
		return true, 0
	}
	return false, 500 * time.Millisecond
}

func main() {
	b := &Bucket{Tokens: 2}

	for range 4 { // range over an int with no loop variable: "do this 4 times"
		ok, wait := b.Allow() // := declares both variables at once
		if !ok {
			fmt.Println("429 Too Many Requests, Retry-After:", wait)
			continue
		}
		fmt.Println("200 OK")
	}

	// Don't need one of the values? Discard it with the blank identifier _.
	ok, _ := b.Allow()
	fmt.Println("one more?", ok)
}
