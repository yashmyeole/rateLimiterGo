// Package proxy forwards client requests to a backend and relays the response.
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

// New returns a reverse proxy that sends each request to the backend b picks. If that
// backend hasn't started answering within timeout, the client gets a 504.
func New(b Balancer, timeout time.Duration) *httputil.ReverseProxy {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout

	return &httputil.ReverseProxy{
		// Rewrite builds the outbound request and runs once per request, so every
		// request asks the balancer for a backend. ReverseProxy has already stripped any
		// X-Forwarded-* headers the client sent, so a client can't fake its IP;
		// SetXForwarded then records the real one.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(b.Pick())
			pr.SetXForwarded()
		},
		Transport:    transport,
		ErrorHandler: writeError,
	}
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
