package limit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// limited wraps a handler that answers 200 "ok" in the middleware, keyed by ClientIP.
func limited(l Limiter) http.Handler {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	return Middleware(l, ClientIP)(next)
}

func request(h http.Handler, remoteAddr, xff string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/users", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMiddlewareAllowsThenRejects(t *testing.T) {
	tb, _ := newTestBucket(t, 1, 2) // the fake clock never moves: no refill
	h := limited(tb)

	for i, wantRemaining := range []string{"1", "0"} {
		rec := request(h, "1.2.3.4:5000", "")
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("request %d: got %d %q, want 200 from the next handler", i+1, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("X-RateLimit-Remaining"); got != wantRemaining {
			t.Errorf("request %d: X-RateLimit-Remaining = %s, want %s", i+1, got, wantRemaining)
		}
		if got := rec.Header().Get("X-RateLimit-Limit"); got != "2" {
			t.Errorf("request %d: X-RateLimit-Limit = %s, want 2", i+1, got)
		}
	}

	rec := request(h, "1.2.3.4:5000", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request: status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body["error"] != "rate limit exceeded" {
		t.Errorf("body = %v (err %v), want error \"rate limit exceeded\"", body, err)
	}
}

func TestMiddlewareKeysByConnectionNotHeader(t *testing.T) {
	tb, _ := newTestBucket(t, 1, 1)
	h := limited(tb)

	request(h, "1.2.3.4:5000", "") // spends the only token
	if rec := request(h, "1.2.3.4:6000", "9.9.9.9"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("same IP, new port and a fake X-Forwarded-For: got %d, want 429", rec.Code)
	}
	if rec := request(h, "5.6.7.8:5000", ""); rec.Code != http.StatusOK {
		t.Errorf("a different client IP: got %d, want 200", rec.Code)
	}
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string) (Decision, error) {
	return Decision{}, errors.New("store unreachable")
}

func TestMiddlewareLimiterErrorIs503(t *testing.T) {
	rec := request(limited(failingLimiter{}), "1.2.3.4:5000", "")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Errorf("limiter error: got %d with Retry-After %q, want 503 with Retry-After 1",
			rec.Code, rec.Header().Get("Retry-After"))
	}
}

type openLimiter struct{}

func (openLimiter) Allow(context.Context, string) (Decision, error) {
	return Decision{Allowed: true}, nil // no limit known, as when failing open
}

func TestMiddlewareOmitsHeadersWithoutALimit(t *testing.T) {
	rec := request(limited(openLimiter{}), "1.2.3.4:5000", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if h := rec.Header().Get("X-RateLimit-Limit"); h != "" {
		t.Errorf("X-RateLimit-Limit = %q with no known limit, want no header", h)
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct{ remoteAddr, want string }{
		{"1.2.3.4:5678", "1.2.3.4"},
		{"[::1]:8080", "::1"},
		{"[2001:db8::7]:443", "2001:db8::7"},
		{"no-port", "no-port"},
	}
	for _, tc := range tests {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = tc.remoteAddr
		if got := ClientIP(req); got != tc.want {
			t.Errorf("ClientIP(%q) = %q, want %q", tc.remoteAddr, got, tc.want)
		}
	}
}

func TestRetrySeconds(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want int
	}{
		{0, 1},
		{time.Millisecond, 1},
		{time.Second, 1},
		{1200 * time.Millisecond, 2},
		{10 * time.Second, 10},
	}
	for _, tc := range tests {
		if got := retrySeconds(tc.in); got != tc.want {
			t.Errorf("retrySeconds(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
