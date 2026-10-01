package proxy

import (
	"net/url"
	"sync/atomic"
)

// Balancer chooses the backend for the next request, or returns nil if there is none
// to use. The proxy depends only on this interface, so other strategies (least
// connections, weighted) can be swapped in.
type Balancer interface {
	Pick() *url.URL
}

// Compile-time check that *RoundRobin satisfies Balancer. If a method goes missing,
// the build fails here instead of somewhere far away.
var _ Balancer = (*RoundRobin)(nil)

// RoundRobin hands out the pool's healthy backends in turn: 1, 2, 3, 1, 2, 3, ...
// When one goes down, the rotation continues over the rest.
type RoundRobin struct {
	pool *Pool
	next atomic.Uint64
}

func NewRoundRobin(pool *Pool) *RoundRobin {
	return &RoundRobin{pool: pool}
}

// Pick is called from many request goroutines at once. A plain counter++ is a
// read-modify-write that two goroutines can interleave (both read 5, both write 6,
// one pick is lost); the atomic Add makes it one indivisible step.
func (rr *RoundRobin) Pick() *url.URL {
	up := rr.pool.Healthy()
	if len(up) == 0 {
		return nil
	}
	n := rr.next.Add(1) - 1 // Add returns the new value; -1 so the first pick is index 0
	return up[n%uint64(len(up))]
}
