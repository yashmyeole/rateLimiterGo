# lab

Go exercises, one folder per concept. Run one with `go run ./lab/<folder>`.
`11_testing` is a library package, so use `go test -v ./lab/11_testing` for that one.

| Folder | Concept | Used later for |
|---|---|---|
| `01_hello` | a program, `go run` vs `go build` | |
| `02_fetch` | `net/http` client, `defer` | proxying |
| `03_structs` | structs, methods, zero values | token bucket |
| `04_slices` | slices, `append`, `range`, shared memory | round-robin balancer |
| `05_maps` | maps, comma-ok, `delete` | per-client buckets, idle eviction |
| `06_pointers` | `&`, `*`, pointer vs value receivers | every stateful type |
| `07_multiple_returns` | several return values, `_` | `Allow() (ok, retryAfter)` |
| `08_errors` | `error`, `%w` wrapping, `errors.Is` | everywhere, Redis failures |
| `09_defer` | `defer`, closures, last-in-first-out | request latency timing |
| `10_interfaces` | interfaces | `Balancer` and `Limiter` |
| `11_testing` | library packages, table-driven tests, `t.Run` | every package |
