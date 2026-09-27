# ratelimiter-go

A reverse proxy with a distributed rate limiter, written in Go. Built to learn Go and
backend fundamentals.

**Status:** work in progress. Currently on Go fundamentals (`lab/`).

## Goal

A proxy that sits in front of backend services and:

- enforces per-client token-bucket limits stored in Redis, with a Lua script so the check stays atomic across proxy replicas
- falls back to in-memory limiting when Redis is unavailable
- health-checks backends and load-balances across the healthy ones
- exposes Prometheus metrics: p95/p99 latency, allowed vs rejected requests

## Layout

- `lab/`: small Go exercises, one folder per concept
- `cmd/`: proxy and backend binaries (coming)
- `internal/`: proxy, limiter, and metrics packages (coming)
- `docs/`: design notes and benchmark results (coming)
