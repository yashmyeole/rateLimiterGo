// Package proxy forwards client requests to healthy backends and relays the response.
package proxy

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"time"
)

// New returns a handler that sends each request to the backend b picks, or answers 503
// if b has none. If the backend hasn't started answering within timeout, the client
// gets a 504.
func New(b Balancer, timeout time.Duration) http.Handler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := b.Pick()
		if target == nil {
			slog.Warn("no healthy backends", "method", r.Method, "path", r.URL.Path)
			writeJSONError(w, http.StatusServiceUnavailable, "no healthy backends")
			return
		}

		// A ReverseProxy is a small struct, so building one per request is cheap; the
		// Transport, which holds the pooled backend connections, is shared.
		rp := &httputil.ReverseProxy{
			// ReverseProxy has already stripped any X-Forwarded-* headers the client
			// sent, so a client can't fake its IP; SetXForwarded records the real one.
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.SetXForwarded()
			},
			Transport:    transport,
			ErrorHandler: writeError,
		}
		rp.ServeHTTP(w, r)
	})
}

// writeError answers when the backend can't be reached (502) or is too slow (504).
// The real error goes to the log only; clients shouldn't see internal addresses.
// r is the outbound request, so r.URL.Host is the backend that failed.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusBadGateway, "backend unavailable"
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		status, msg = http.StatusGatewayTimeout, "backend timed out"
	}
	slog.Warn("proxy error", "backend", r.URL.Host, "method", r.Method, "path", r.URL.Path, "status", status, "err", err)
	writeJSONError(w, status, msg)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
