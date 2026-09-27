package main

import "fmt"

// A struct groups related values under one name. Go has no classes; structs + methods
// cover the same ground. This Bucket is the seed of the Phase 7 rate limiter.
type Bucket struct {
	Capacity int // the most tokens it can ever hold
	Tokens   int // how many are left right now
}

// Take is a method: a function attached to a type. (b *Bucket) is the "receiver":
// like self/this, but named explicitly. Why the * is there: see 06_pointers.
func (b *Bucket) Take() bool {
	if b.Tokens == 0 {
		return false // bucket empty: request refused
	}
	b.Tokens--
	return true
}

func main() {
	b := Bucket{Capacity: 3, Tokens: 3} // struct literal: set fields by name

	for i := 1; i <= 5; i++ { // for is Go's only loop keyword
		ok := b.Take()
		fmt.Println("request", i, "allowed:", ok, "| tokens left:", b.Tokens)
	}

	var empty Bucket           // declared, never set: every field starts at its "zero value" (0 for int)
	fmt.Printf("%+v\n", empty) // %+v prints field names too
}
