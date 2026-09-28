# ratelimiter-go

A reverse proxy with a distributed rate limiter, written in Go. Built to learn Go and
backend fundamentals.

**Status:** work in progress. The fake backend services are done; the reverse proxy is next.

## Goal

A proxy that sits in front of backend services and:

- enforces per-client token-bucket limits stored in Redis, with a Lua script so the check stays atomic across proxy replicas
- falls back to in-memory limiting when Redis is unavailable
- health-checks backends and load-balances across the healthy ones
- exposes Prometheus metrics: p95/p99 latency, allowed vs rejected requests

## Running locally

Requires Go 1.27+.

```sh
make backends                        # 3 backend instances on :9001, :9002, :9003 (Ctrl-C stops all)
curl -i localhost:9001/api/users     # the X-Backend header names the instance that answered
```

Each instance serves `GET /api/users`, `/api/orders`, `/api/quotes` and `/healthz`.
To simulate a slow service, run one by hand with a delay:

```sh
./bin/backend -name api-2 -addr localhost:9002 -delay 300ms -jitter 100ms
```

`make test` and `make vet` run the tests and `go vet`.

## Layout

- `lab/`: small Go exercises, one folder per concept
- `cmd/backend/`: fake API service used as the proxy's target
- `cmd/proxy/`: the proxy (coming)
- `internal/`: proxy, limiter, and metrics packages (coming)
- `docs/`: design notes and benchmark results (coming)
