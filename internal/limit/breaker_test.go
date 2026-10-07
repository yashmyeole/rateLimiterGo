package limit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestBreaker(threshold int, cooldown time.Duration) (*breaker, *fakeClock) {
	b := newBreaker(threshold, cooldown)
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	b.now = clock.Now
	return b, clock
}

func TestBreakerCycle(t *testing.T) {
	b, clock := newTestBreaker(3, 2*time.Second)

	const none, ok, fail = 0, 1, 2
	steps := []struct {
		name      string
		advance   time.Duration
		wantAllow bool
		result    int // what to record if allowed
		wantState breakerState
	}{
		{"closed: calls go through", 0, true, fail, closed},
		{"2 failures in a row", 0, true, fail, closed},
		{"a success resets the count", 0, true, ok, closed},
		{"failure 1 of 3", 0, true, fail, closed},
		{"failure 2 of 3", 0, true, fail, closed},
		{"failure 3 of 3 opens it", 0, true, fail, open},
		{"open: calls skipped", 0, false, none, open},
		{"still open 1ms before the cooldown ends", 1999 * time.Millisecond, false, none, open},
		{"cooldown over: one trial call", time.Millisecond, true, none, halfOpen},
		{"trial in flight: others skip", 0, false, none, halfOpen},
	}
	for _, s := range steps {
		clock.Advance(s.advance)
		if got := b.allow(); got != s.wantAllow {
			t.Fatalf("%s: allow() = %v, want %v", s.name, got, s.wantAllow)
		}
		if s.wantAllow && s.result != none {
			b.record(s.result == ok)
		}
		if b.state != s.wantState {
			t.Fatalf("%s: state %v, want %v", s.name, b.state, s.wantState)
		}
	}

	// The trial fails: open for a whole new cooldown.
	if from, to := b.record(false); from != halfOpen || to != open {
		t.Fatalf("failed trial: %v -> %v, want half-open -> open", from, to)
	}
	clock.Advance(time.Second)
	if b.allow() {
		t.Fatal("allowed 1s into the second cooldown")
	}
	clock.Advance(time.Second)
	if !b.allow() {
		t.Fatal("no trial after the second cooldown")
	}
	if from, to := b.record(true); from != halfOpen || to != closed {
		t.Fatalf("successful trial: %v -> %v, want half-open -> closed", from, to)
	}
	if !b.allow() {
		t.Error("closed again but allow() = false")
	}
}

// When the cooldown ends, many requests arrive at once; only one may try Redis.
func TestBreakerOneTrialAtATime(t *testing.T) {
	b, clock := newTestBreaker(1, time.Second)
	b.allow()
	b.record(false) // open
	clock.Advance(time.Second)

	var trials atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if b.allow() {
				trials.Add(1)
			}
		})
	}
	wg.Wait()
	if got := trials.Load(); got != 1 {
		t.Errorf("%d trial calls allowed at once, want 1", got)
	}
}
