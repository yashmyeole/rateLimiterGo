.PHONY: build test vet backends

build:
	go build -o bin/backend ./cmd/backend

test:
	go test ./...

vet:
	go vet ./...

# Three backend instances on :9001-9003. Ctrl-C stops all of them: background jobs in a
# non-interactive shell ignore SIGINT, so the trap sends SIGTERM to the whole group.
backends: build
	@trap 'kill 0' INT TERM; \
	./bin/backend -name api-1 -addr localhost:9001 & \
	./bin/backend -name api-2 -addr localhost:9002 & \
	./bin/backend -name api-3 -addr localhost:9003 & \
	wait
