package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// httptest.NewRecorder captures what a handler writes, so routes can be tested
// without opening a real port.
func serve(method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	(&backend{name: "test-1"}).routes().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestRoutes(t *testing.T) {
	tests := []struct {
		method, path string
		wantStatus   int
	}{
		{"GET", "/api/users", http.StatusOK},
		{"GET", "/api/orders", http.StatusOK},
		{"GET", "/api/quotes", http.StatusOK},
		{"GET", "/healthz", http.StatusOK},
		{"HEAD", "/healthz", http.StatusOK}, // a GET pattern also accepts HEAD
		{"GET", "/api/unknown", http.StatusNotFound},
		{"POST", "/api/users", http.StatusMethodNotAllowed},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := serve(tc.method, tc.path)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusOK && rec.Header().Get("X-Backend") != "test-1" {
				t.Errorf("X-Backend = %q, want %q", rec.Header().Get("X-Backend"), "test-1")
			}
		})
	}
}

func TestUsersJSON(t *testing.T) {
	rec := serve("GET", "/api/users")

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got []user
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(got) != len(users) || got[0] != users[0] {
		t.Errorf("got %+v, want %+v", got, users)
	}
}

func TestLatencyStaysInRange(t *testing.T) {
	tests := []struct {
		name          string
		delay, jitter time.Duration
	}{
		{"no delay", 0, 0},
		{"delay only", 100 * time.Millisecond, 0},
		{"delay and jitter", 100 * time.Millisecond, 50 * time.Millisecond},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &backend{delay: tc.delay, jitter: tc.jitter}
			for range 1000 {
				got := b.latency()
				if got < tc.delay || got > tc.delay+tc.jitter {
					t.Fatalf("latency() = %v, want between %v and %v", got, tc.delay, tc.delay+tc.jitter)
				}
			}
		})
	}
}
