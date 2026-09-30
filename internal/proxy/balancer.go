package proxy

import (
	"errors"
	"net/url"
	"sync/atomic"
)

// Balancer chooses the backend for the next request. The proxy depends only on this
// interface, so other strategies (least connections, weighted) can be swapped in.
type Balancer interface {
	Pick() *url.URL
}

// Compile-time check that *RoundRobin satisfies Balancer. If a method goes missing,
// the build fails here instead of somewhere far away.
var _ Balancer = (*RoundRobin)(nil)

// RoundRobin hands out backends in turn: 1, 2, 3, 1, 2, 3, ...
type RoundRobin struct {
	backends []*url.URL
	next     atomic.Uint64
}

// NewRoundRobin returns a RoundRobin over backends, which must not be empty.
func NewRoundRobin(backends []*url.URL) (*RoundRobin, error) {
	if len(backends) == 0 {
		return nil, errors.New("round robin needs at least one backend")
	}
	return &RoundRobin{backends: backends}, nil
}

// Pick is called from many request goroutines at once. A plain counter++ is a
// read-modify-write that two goroutines can interleave (both read 5, both write 6,
// one pick is lost); the atomic Add makes it one indivisible step.
func (rr *RoundRobin) Pick() *url.URL {
	n := rr.next.Add(1) - 1 // Add returns the new value; -1 so the first pick is index 0
	return rr.backends[n%uint64(len(rr.backends))]
}
