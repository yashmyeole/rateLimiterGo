package bucket

import "testing"

// A table-driven test: each row is one scenario, and one loop runs them all.
// Adding a case means adding a line. This is the standard shape of Go tests.
func TestTake(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		takes    int  // how many times to call Take
		wantLast bool // what the final Take should return
		wantLeft int  // tokens left afterwards
	}{
		{"first take from a full bucket", 3, 1, true, 2},
		{"drain it exactly", 3, 3, true, 0},
		{"one past empty is refused", 3, 4, false, 0},
		{"zero capacity refuses at once", 0, 1, false, 0},
	}

	for _, tc := range tests {
		// t.Run makes each row a named subtest that passes or fails on its own.
		// (Older code copies `tc := tc` first; since Go 1.22 that's unnecessary.)
		t.Run(tc.name, func(t *testing.T) {
			b := New(tc.capacity)
			var got bool
			for range tc.takes {
				got = b.Take()
			}
			if got != tc.wantLast || b.Tokens != tc.wantLeft {
				t.Errorf("after %d takes: got (%v, %d left), want (%v, %d left)",
					tc.takes, got, b.Tokens, tc.wantLast, tc.wantLeft)
			}
		})
	}
}

func TestRefillStopsAtCapacity(t *testing.T) {
	b := New(5)
	b.Take()
	b.Take()
	b.Refill(100)
	if b.Tokens != 5 {
		t.Errorf("Tokens = %d, want 5: refill must never overfill the bucket", b.Tokens)
	}
}
