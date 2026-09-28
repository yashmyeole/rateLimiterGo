package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// startProxy runs New(backendURL) on a real local port and returns its URL.
func startProxy(t *testing.T, backendURL string, timeout time.Duration) string {
	t.Helper()
	target, err := url.Parse(backendURL)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(target, timeout))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestForwardsRequestAndResponse(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "fake-1")
		w.WriteHeader(http.StatusTeapot) // an unusual code, to prove it's relayed as-is
		io.WriteString(w, "path="+r.URL.Path+" query="+r.URL.RawQuery)
	}))
	defer backend.Close()

	resp, err := http.Get(startProxy(t, backend.URL, time.Second) + "/api/users?limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
	if got := resp.Header.Get("X-Backend"); got != "fake-1" {
		t.Errorf("X-Backend = %q, want fake-1", got)
	}
	if want := "path=/api/users query=limit=2"; string(body) != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestSetsXForwardedForAndDropsSpoofedValue(t *testing.T) {
	// The backend echoes the header it received, so the test reads it from the response
	// instead of sharing a variable with the server's goroutine.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Header.Get("X-Forwarded-For"))
	}))
	defer backend.Close()

	req, _ := http.NewRequest("GET", startProxy(t, backend.URL, time.Second)+"/", nil)
	req.Header.Set("X-Forwarded-For", "6.6.6.6") // a client pretending to be someone else
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if gotXFF := string(body); gotXFF != "127.0.0.1" {
		t.Errorf("backend saw X-Forwarded-For %q, want 127.0.0.1 (the real client only)", gotXFF)
	}
}

func TestBackendErrors(t *testing.T) {
	// A server that is started and immediately closed: its port refuses connections.
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	// A server slower than the proxy's timeout. It stops early once the proxy gives up.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()

	tests := []struct {
		name       string
		backendURL string
		wantStatus int
		wantError  string
	}{
		{"backend down", down.URL, http.StatusBadGateway, "backend unavailable"},
		{"backend too slow", slow.URL, http.StatusGatewayTimeout, "backend timed out"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(startProxy(t, tc.backendURL, 100*time.Millisecond) + "/api/users")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			var body map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["error"] != tc.wantError {
				t.Errorf("error = %q, want %q", body["error"], tc.wantError)
			}
		})
	}
}
