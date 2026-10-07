# ratelimiter-go

A reverse proxy with a distributed rate limiter, written in Go. Built to learn Go and
backend fundamentals.

**Status:** work in progress. The proxy rate-limits each client IP, in memory or shared across
replicas in Redis (token bucket, sliding window or fixed window, as atomic Lua scripts),
keeps serving when Redis fails, load-balances across healthy backends and shuts down
gracefully. Next: Prometheus metrics and a Grafana dashboard.

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

Each client IP gets a token bucket: up to `-burst` requests at once (default 20), refilling at
`-rate` requests per second (default 10; `-rate 0` turns limiting off). Over the limit the proxy
answers `429` with `Retry-After`, without contacting a backend. Every response carries
`X-RateLimit-Limit` and `X-RateLimit-Remaining`. The client is identified by the connection's
IP; `X-Forwarded-For` sent by a client is ignored, since anyone can set it. Every 30s a
background sweep deletes buckets that have refilled completely (a full bucket behaves exactly
like a new one), so memory stays flat no matter how many clients come and go.

```sh
# 30 quick requests: about 20 pass, the rest get 429
for i in $(seq 30); do curl -s -o /dev/null -w "%{http_code} " localhost:8080/api/users; done; echo
```

### Sharing limits across replicas

With several proxy replicas, each one limiting on its own gives a client the full limit at
every replica. `-redis` moves the count into Redis so all replicas share it:

```sh
make redis                           # Redis 8.10 in Docker, published on 127.0.0.1:6379 only
make backends                        # terminal 1
make replicas                        # terminal 2: proxies on :8080 and :8081 sharing one limit
for i in $(seq 20); do curl -s -o /dev/null -w "%{http_code} " localhost:$((8080 + i % 2))/api/users; done; echo
make replicas ALGORITHM=sliding-window   # or fixed-window
make redis-stop
```

With `-redis`, `-algorithm` picks one of three, all sharing `-rate`:

| algorithm        | limit                                                  | flags     |
|------------------|--------------------------------------------------------|-----------|
| `token-bucket`   | bursts of `-burst`, then `-rate` per second (default)  | `-burst`  |
| `sliding-window` | `-rate` x `-window` requests in any window-long span   | `-window` |
| `fixed-window`   | `-rate` x `-window` requests per clock window          | `-window` |

The token bucket and sliding window run as Lua scripts inside Redis, so each check is atomic
and uses Redis's clock rather than each proxy's. The fixed window lets a client through twice
at a window boundary; it stays for comparison. Measurements and trade-offs are in
[docs/decisions.md](docs/decisions.md) and [docs/RESULTS.md](docs/RESULTS.md).

### When Redis goes down

Each check in Redis has a time budget (`-redis-timeout`, default 50 ms). After 3 failures in
a row a circuit breaker stops calling Redis for 2 seconds, then lets one request try again.
Meanwhile `-redis-fallback` decides:

| `-redis-fallback` | while Redis is down                                                     |
|-------------------|-------------------------------------------------------------------------|
| `local` (default) | each replica limits alone with `1/-replicas` of the limit               |
| `open`            | every request is allowed                                                |
| `closed`          | every request gets `503` with `Retry-After`                             |

The proxy also starts when Redis is unreachable, on the fallback. In a drill with two
replicas under steady load, stopping Redis for 8 seconds caused no failed requests and the
limit held at 10 per second throughout ([docs/RESULTS.md](docs/RESULTS.md)).

```sh
make replicas                                   # with make redis and make backends running
docker stop ratelimiter-redis                   # requests keep flowing, limits stay in place
make redis                                      # shared limits resume
```

Every `-health-interval` (default 2s) the proxy probes each backend's `/healthz` and stops
sending traffic to any that fail; they rejoin once a probe passes. If no backend is healthy
it answers `503`. Ctrl-C (or SIGTERM) stops new connections and lets in-flight requests
finish before exiting.

To simulate a slow service, run one by hand with a delay:

```sh
./bin/backend -name api-2 -addr 127.0.0.1:9002 -delay 300ms -jitter 100ms
```

`make test` runs the tests with the race detector; `make vet` runs `go vet`. `make bench` runs
the limiter benchmarks and `SOAK=10m make soak` a memory soak; measured numbers are in
[docs/RESULTS.md](docs/RESULTS.md).

## Layout

- `lab/`: small Go exercises, one folder per concept
- `cmd/backend/`: fake API service used as the proxy's target
- `cmd/proxy/`: the proxy binary (flags, HTTP server)
- `internal/proxy/`: request forwarding, round-robin load balancing, health checks, backend error handling
- `internal/limit/`: `Limiter` interface, in-memory token bucket with idle eviction, Redis
  token bucket and sliding window (Lua) and fixed window, circuit breaker and fallback,
  rate-limit middleware
- `internal/`: metrics package (coming)
- `docs/RESULTS.md`: measured numbers and how to reproduce them
- `docs/decisions.md`: design decisions and their trade-offs
