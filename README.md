# ratelimiter-go

A reverse proxy with a distributed rate limiter, written in Go. Built to learn Go and
backend fundamentals.

**Status:** work in progress. The proxy load-balances across healthy backends and shuts down
gracefully; rate limiting is next.

## Goal

A proxy that sits in front of backend services and:

- enforces per-client token-bucket limits stored in Redis, with a Lua script so the check stays atomic across proxy replicas
- falls back to in-memory limiting when Redis is unavailable
- health-checks backends and load-balances across the healthy ones
- exposes Prometheus metrics: p95/p99 latency, allowed vs rejected requests

## Running locally

Requires Go 1.27+.

```sh
make backends                        # terminal 1: backends on :9001, :9002, :9003 (Ctrl-C stops all)
make proxy                           # terminal 2: proxy on :8080, sending requests to the backends in turn
curl -i localhost:8080/api/users     # the X-Backend header names the instance that answered

# six requests: api-1, api-2, api-3, api-1, api-2, api-3
for i in 1 2 3 4 5 6; do curl -s -o /dev/null -D - localhost:8080/api/users | grep X-Backend; done
```

Each backend serves `GET /api/users`, `/api/orders`, `/api/quotes` and `/healthz`.

The proxy picks backends round robin (set them with `-backends`, comma-separated URLs) and
passes requests through unchanged. It sets `X-Forwarded-For` to the real client IP, ignoring
any value the client sent, and returns a JSON `502` if the chosen backend is down or a `504`
if it doesn't start answering within `-timeout` (default 5s).

Every `-health-interval` (default 2s) the proxy probes each backend's `/healthz` and stops
sending traffic to any that fail; they rejoin once a probe passes. If no backend is healthy
it answers `503`. Ctrl-C (or SIGTERM) stops new connections and lets in-flight requests
finish before exiting.

To simulate a slow service, run one by hand with a delay:

```sh
./bin/backend -name api-2 -addr 127.0.0.1:9002 -delay 300ms -jitter 100ms
```

`make test` runs the tests with the race detector; `make vet` runs `go vet`.

## Layout

- `lab/`: small Go exercises, one folder per concept
- `cmd/backend/`: fake API service used as the proxy's target
- `cmd/proxy/`: the proxy binary (flags, HTTP server)
- `internal/proxy/`: request forwarding, round-robin load balancing, health checks, backend error handling
- `internal/`: limiter and metrics packages (coming)
- `docs/`: design notes and benchmark results (coming)
