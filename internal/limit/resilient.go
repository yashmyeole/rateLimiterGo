package limit

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

// ErrUnavailable is returned in FailClosed mode while the shared limiter is down.
var ErrUnavailable = errors.New("rate limiter unavailable")

// FailMode is what Resilient does while the shared limiter (Redis) is unavailable.
type FailMode int

const (
	// FallbackLocal limits with a per-replica in-memory limiter. Each replica gets its
	// share of the global limit, so the total stays about the same.
	FallbackLocal FailMode = iota
	// FailOpen allows every request: availability over protection.
	FailOpen
	// FailClosed refuses every request with ErrUnavailable: protection over availability.
	FailClosed
)

var _ Limiter = (*Resilient)(nil)

// Resilient wraps a shared limiter so that a slow or dead Redis can't take the proxy
// down with it. Each check gets a time budget; repeated failures open a circuit breaker
// so later requests skip Redis entirely; while Redis is unavailable, mode decides.
type Resilient struct {
	shared   Limiter
	local    Limiter // used in FallbackLocal mode
	mode     FailMode
	budget   time.Duration
	breaker  *breaker
	degraded atomic.Int64 // decisions made without the shared limiter
}

// NewResilient wraps shared. local is required for FallbackLocal and ignored otherwise.
// Each call to shared gets budget; threshold failures in a row open the breaker for
// cooldown.
func NewResilient(shared, local Limiter, mode FailMode, budget time.Duration, threshold int, cooldown time.Duration) (*Resilient, error) {
	if mode == FallbackLocal && local == nil {
		return nil, errors.New("FallbackLocal needs a local limiter")
	}
	if budget <= 0 || threshold < 1 || cooldown <= 0 {
		return nil, errors.New("resilient limiter needs budget > 0, threshold >= 1 and cooldown > 0")
	}
	return &Resilient{
		shared:  shared,
		local:   local,
		mode:    mode,
		budget:  budget,
		breaker: newBreaker(threshold, cooldown),
	}, nil
}

func (r *Resilient) Allow(ctx context.Context, key string) (Decision, error) {
	if r.breaker.allow() {
		callCtx, cancel := context.WithTimeout(ctx, r.budget)
		d, err := r.shared.Allow(callCtx, key)
		cancel()

		from, to := r.breaker.record(err == nil)
		r.logTransition(from, to, err)
		if err == nil {
			return d, nil
		}
	}
	return r.withoutShared(ctx, key)
}

// Degraded returns how many decisions were made without the shared limiter so far.
func (r *Resilient) Degraded() int64 {
	return r.degraded.Load()
}

func (r *Resilient) withoutShared(ctx context.Context, key string) (Decision, error) {
	r.degraded.Add(1)
	switch r.mode {
	case FailOpen:
		return Decision{Allowed: true}, nil // Limit 0: no limit is known, so no headers
	case FailClosed:
		return Decision{}, ErrUnavailable
	default:
		return r.local.Allow(ctx, key)
	}
}

func (r *Resilient) logTransition(from, to breakerState, err error) {
	switch {
	case from == to:
	case to == open && from == closed:
		slog.Warn("shared rate limiter unavailable, using fallback", "mode", r.mode, "err", err)
	case to == open: // a half-open trial failed
		slog.Warn("shared rate limiter still unavailable", "err", err)
	case to == closed:
		slog.Info("shared rate limiter back", "degraded_decisions_so_far", r.degraded.Load())
	}
}

func (m FailMode) String() string {
	return [...]string{"local", "open", "closed"}[m]
}
