// Package limit decides whether a client may make another request, and provides the
// HTTP middleware that enforces it.
package limit

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Decision is a limiter's answer for one request.
type Decision struct {
	Allowed    bool
	Limit      int           // the most requests a client can make in a burst
	Remaining  int           // whole requests left right now, after this one
	RetryAfter time.Duration // when not allowed: how long until the next request would be
}

// Limiter decides whether the client identified by key may make a request now.
// The context and error are for limiters backed by a network store such as Redis;
// an in-memory limiter ignores the context and never fails.
type Limiter interface {
	Allow(ctx context.Context, key string) (Decision, error)
}

// Middleware rejects requests over the limit with 429 before they reach next.
// Responses carry X-RateLimit-Limit and X-RateLimit-Remaining when the limit is known; a
// 429 also carries Retry-After in whole seconds. If the limiter can't decide at all
// (Resilient in FailClosed mode while Redis is down), the request gets a 503.
func Middleware(l Limiter, keyOf func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d, err := l.Allow(r.Context(), keyOf(r))
			if err != nil {
				// Not a 429: the client did nothing wrong. Whether to fail open, closed or
				// to a local limiter is the limiter's policy (see Resilient); an error
				// that reaches here means closed.
				w.Header().Set("Retry-After", "1")
				writeJSONError(w, http.StatusServiceUnavailable, "rate limiter unavailable")
				return
			}

			if d.Limit > 0 { // 0 means no limit is known, e.g. while failing open
				w.Header().Set("X-RateLimit-Limit", strconv.Itoa(d.Limit))
				w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(d.Remaining))
			}
			if !d.Allowed {
				w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(d.RetryAfter)))
				writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP keys requests by the IP address of the connection. It ignores
// X-Forwarded-For on purpose: this proxy is the front door, and any client can put
// any value in that header to get a fresh limit on every request.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// retrySeconds converts a wait into Retry-After's whole seconds, rounding up so a
// client that waits exactly that long is never refused again.
func retrySeconds(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
