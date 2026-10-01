package proxy

import (
	"errors"
	"net/url"
	"slices"
	"sync"
)

// Pool is the set of backends and which of them are healthy. Every request reads it;
// the health checker writes it every few seconds. sync.RWMutex suits that read-heavy
// pattern: any number of readers can hold RLock together, while Lock waits for them
// to finish and then has the pool to itself.
type Pool struct {
	all []*url.URL // fixed after NewPool

	mu      sync.RWMutex    // guards the fields below
	healthy map[string]bool // keyed by URL string
	up      []*url.URL      // healthy backends in their original order
}

// NewPool returns a pool over backends, which must not be empty. Every backend starts
// healthy so traffic flows before the first health check finishes.
func NewPool(backends []*url.URL) (*Pool, error) {
	if len(backends) == 0 {
		return nil, errors.New("pool needs at least one backend")
	}
	p := &Pool{
		all:     backends,
		healthy: make(map[string]bool, len(backends)),
		up:      slices.Clone(backends),
	}
	for _, u := range backends {
		p.healthy[u.String()] = true
	}
	return p, nil
}

// All returns every backend, healthy or not. It never changes, so no lock is needed.
func (p *Pool) All() []*url.URL {
	return p.all
}

// Healthy returns the backends that passed their last check. SetHealthy never edits
// this slice in place, it swaps in a new one, so callers can keep using what they got
// after the lock is released.
func (p *Pool) Healthy() []*url.URL {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.up
}

// SetHealthy records a check result and reports whether the backend's state changed.
// URLs that aren't in the pool are ignored.
func (p *Pool) SetHealthy(u *url.URL, ok bool) (changed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	was, known := p.healthy[u.String()]
	if !known || was == ok {
		return false
	}
	p.healthy[u.String()] = ok

	up := make([]*url.URL, 0, len(p.all))
	for _, b := range p.all {
		if p.healthy[b.String()] {
			up = append(up, b)
		}
	}
	p.up = up
	return true
}
