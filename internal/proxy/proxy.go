// Package proxy forwards client requests to a backend and relays the response.
package proxy

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// New returns a reverse proxy that sends every request to target. If the backend
// hasn't started answering within timeout, the client gets a 504.
func New(target *url.URL, timeout time.Duration) *httputil.ReverseProxy {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout

	return &httputil.ReverseProxy{
		// Rewrite builds the outbound request. ReverseProxy has already stripped any
		// X-Forwarded-* headers the client sent, so a client can't fake its IP;
		// SetXForwarded then records the real one.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
		},
		Transport:    transport,
		ErrorHandler: writeError,
	}
}

// writeError answers when the backend can't be reached (502) or is too slow (504).
// The real error goes to the log only; clients shouldn't see internal addresses.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusBadGateway, "backend unavailable"
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		status, msg = http.StatusGatewayTimeout, "backend timed out"
	}
	slog.Warn("proxy error", "method", r.Method, "path", r.URL.Path, "status", status, "err", err)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
