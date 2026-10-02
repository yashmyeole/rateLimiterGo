.PHONY: build test vet bench soak backends proxy redis redis-stop replicas

build:
	go build -o bin/ ./cmd/...

# -race: the proxy runs handlers concurrently, so every test run checks for data races.
test:
	go test -race ./...

vet:
	go vet ./...

# Limiter benchmarks on 1, 4 and 10 CPUs (results in docs/RESULTS.md).
bench:
	go test -run '^$$' -bench . -benchmem -cpu 1,4,10 ./internal/limit

# Limiter memory over time with the janitor running: SOAK=10m make soak
soak:
	go test -run 'TestSoak/^janitor$$' -v -timeout 0 ./internal/limit

# Three backend instances on :9001-9003. Ctrl-C stops all of them: background jobs in a
# non-interactive shell ignore SIGINT, so the trap sends SIGTERM to the whole group.
backends: build
	@trap 'kill 0' INT TERM; \
	./bin/backend -name api-1 -addr 127.0.0.1:9001 & \
	./bin/backend -name api-2 -addr 127.0.0.1:9002 & \
	./bin/backend -name api-3 -addr 127.0.0.1:9003 & \
	wait

# The proxy on :8080, sending requests to the three backends in turn. Run `make backends` in another terminal.
proxy: build
	./bin/proxy

# Redis in Docker for shared rate limits, reachable from this machine only.
redis:
	docker run -d --rm --name ratelimiter-redis -p 127.0.0.1:6379:6379 redis:8.10-alpine

redis-stop:
	docker stop ratelimiter-redis

# Two proxy replicas on :8080 and :8081 sharing one fixed-window limit in Redis
# (10 requests per 10s per client). Needs `make redis` and `make backends`.
replicas: build
	@trap 'kill 0' INT TERM; \
	./bin/proxy -addr localhost:8080 -redis 127.0.0.1:6379 -algorithm fixed-window -rate 1 -window 10s & \
	./bin/proxy -addr localhost:8081 -redis 127.0.0.1:6379 -algorithm fixed-window -rate 1 -window 10s & \
	wait
