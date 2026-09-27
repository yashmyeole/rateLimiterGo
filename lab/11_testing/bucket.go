// Package bucket is a library, not a program: it has no main, so you `go test` it
// instead of `go run`. Tests live next to the code in bucket_test.go.
package bucket

// Bucket holds up to Capacity tokens; each allowed request spends one.
type Bucket struct {
	Capacity int
	Tokens   int
}

// New returns a full bucket. A New function is Go's convention for a constructor.
func New(capacity int) *Bucket {
	return &Bucket{Capacity: capacity, Tokens: capacity}
}

// Take spends one token if there is one, and reports whether it did.
func (b *Bucket) Take() bool {
	if b.Tokens == 0 {
		return false
	}
	b.Tokens--
	return true
}

// Refill adds n tokens but never goes above Capacity.
func (b *Bucket) Refill(n int) {
	b.Tokens = min(b.Tokens+n, b.Capacity) // min is built into the language
}
