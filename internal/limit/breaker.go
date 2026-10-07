package limit

import (
	"sync"
	"time"
)

// breakerState is where a circuit breaker is in its cycle.
type breakerState int

const (
	closed   breakerState = iota // calls go through
	open                         // calls are skipped until the cooldown ends
	halfOpen                     // one trial call is in flight; its result decides
)

func (s breakerState) String() string {
	return [...]string{"closed", "open", "half-open"}[s]
}

// breaker stops calling something that keeps failing. After threshold failures in a row
// it opens and skips calls for cooldown, so a dead Redis costs nothing per request
// instead of a timeout each. Then it lets exactly one trial call through: success closes
// it again, failure opens it for another cooldown.
type breaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu       sync.Mutex
	state    breakerState
	failures int // consecutive, while closed
	openedAt time.Time
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	return &breaker{threshold: threshold, cooldown: cooldown, now: time.Now}
}

// allow reports whether to make the call now. When it returns true the caller must
// report the outcome with record.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case closed:
		return true
	case open:
		if b.now().Sub(b.openedAt) < b.cooldown {
			return false
		}
		b.state = halfOpen // this caller makes the trial call
		return true
	default: // halfOpen: a trial is already in flight, everyone else waits for it
		return false
	}
}

// record reports a call's outcome and returns the state before and after, so the
// caller can log transitions.
func (b *breaker) record(ok bool) (from, to breakerState) {
	b.mu.Lock()
	defer b.mu.Unlock()

	from = b.state
	switch {
	case ok:
		b.state, b.failures = closed, 0
	case b.state == halfOpen:
		b.state, b.openedAt = open, b.now() // the trial failed: wait another cooldown
	default:
		b.failures++
		if b.failures >= b.threshold {
			b.state, b.openedAt, b.failures = open, b.now(), 0
		}
	}
	return from, b.state
}
