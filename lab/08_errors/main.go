package main

import (
	"errors"
	"fmt"
	"strconv" // string <-> number conversions
)

// A "sentinel" error: a named value that callers can check for with errors.Is.
var ErrBadLimit = errors.New("limit must be positive")

// parseLimit turns config text like "10" into a number. Go has no exceptions:
// failure is just a second return value of type error, and nil means "no error".
func parseLimit(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		// %w wraps the original error: we add context, and the cause stays inspectable.
		return 0, fmt.Errorf("parse limit %q: %w", s, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("parse limit %q: %w", s, ErrBadLimit)
	}
	return n, nil
}

func main() {
	for _, in := range []string{"10", "ten", "-5"} {
		n, err := parseLimit(in)
		if err != nil { // the most common line in any Go codebase
			fmt.Println("error:", err)
			if errors.Is(err, ErrBadLimit) { // looks through the wrapping for ErrBadLimit
				fmt.Println("  -> a bad value, not a typo")
			}
			continue
		}
		fmt.Println("limit =", n)
	}
}
