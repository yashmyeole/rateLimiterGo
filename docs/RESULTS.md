# Results

Measured numbers for this project, with the commands to reproduce them. They come from one
laptop, so they show how the pieces behave relative to each other, not what a production
deployment would do.

## Machine

- Apple M4, 10 cores (4 performance, 6 efficiency), 16 GB RAM, macOS 26.6.2, on AC power
- Go 1.27.1
- Measured 2026-10-01

## Rate limiter: one lock vs. the alternatives

`BenchmarkAllow` in `internal/limit/bench_test.go` compares three designs. All three use the
same token-bucket math and differ only in how a request finds its client's bucket:

- **mutex**: one `sync.Mutex` around one map. This is what the proxy uses.
- **syncmap**: a `sync.Map` for lookups and a mutex per bucket.
- **sharded16**: 16 maps, each with its own mutex, picked by a hash of the key.

Each runs under parallel load with every goroutine hitting one client, and with goroutines
spread over 10,000 clients. The time is wall-clock time per decision across all goroutines,
so lower means more throughput. Median of 5 runs (`-count 5`), in nanoseconds per decision:

| design    | traffic     | 1 CPU | 4 CPUs | 10 CPUs |
|-----------|-------------|------:|-------:|--------:|
| mutex     | 1 client    |  48.0 |  113.7 |   143.6 |
| syncmap   | 1 client    |  50.5 |  105.7 |   134.2 |
| sharded16 | 1 client    |  50.7 |  107.4 |   139.3 |
| mutex     | 10k clients |  52.3 |  102.8 |   144.0 |
| syncmap   | 10k clients |  56.7 |   16.6 |    13.0 |
| sharded16 | 10k clients |  58.4 |   23.1 |    28.9 |

None of the designs allocates memory per decision.

What the numbers say:

- **One busy client.** Every design has to serialize on that client's bucket, so more cores
  only add contention. No data structure fixes this.
- **Many clients.** The single mutex gets slower as cores are added (52 to 144 ns) because
  every request takes the same lock. `sync.Map` and sharding scale; at 10 CPUs `sync.Map` is
  about 11 times faster.
- **Where the time goes.** A CPU profile of `mutex/10k-clients` on 10 CPUs puts about 87% of
  samples in lock waiting (`runtime.usleep`, `pthread_cond_wait`, `pthread_cond_signal`). The
  map lookup is about 1% and reading the clock about 3%.
- **False sharing.** The first sharded version used 16-byte shards, so 8 of them shared one
  128-byte cache line, and cores locking different shards still fought over the line. It ran
  at 31.9 ns on 4 CPUs and 59.7 ns on 10, slower with more cores. Padding each shard to
  128 bytes brought that to 23.1 and 28.9 ns.

**Decision: keep the single mutex for now.** At 144 ns under 10-way contention it still makes
about 7 million decisions a second, and one map keeps the idle sweep below simple. Whether
that ceiling matters depends on the rest of the request path, which gets measured under load
later. If the limiter shows up in a profile of the whole proxy, `sync.Map` is the first thing
to try.

## Idle sweep pause

The janitor holds the lock while it scans for idle buckets, so every request waits for the
scan. `BenchmarkEvictIdleScan` measures one sweep over a map where nothing is idle yet (the
worst steady case: full scan, nothing removed). Median of 5 runs on 1 CPU:

| clients   | pause per sweep | per client |
|----------:|----------------:|-----------:|
| 10,000    |         0.08 ms |     7.6 ns |
| 100,000   |         0.73 ms |     7.3 ns |
| 1,000,000 |        18.2 ms  |    18.2 ns |

The cost per client more than doubles at a million clients. With the 30-second sweep
interval, that is one pause of up to 18 ms every 30 seconds at a million active clients.

## Memory over time

`TestSoak` in `internal/limit/soak_test.go` feeds the limiter (the proxy's defaults: rate 10,
burst 20) 5,000 new client IPs a second, three requests each, and logs the live heap after a
garbage collection every 10 seconds. Three runs:

- **no janitor**: buckets are never deleted (stopped after 2 minutes)
- **janitor**: idle buckets deleted every 30 seconds, as first written
- **janitor + rebuild**: the current code, which also replaces the map once it has shrunk to
  a quarter of its peak size

| time  | no janitor          | janitor           | janitor + rebuild |
|------:|---------------------|-------------------|-------------------|
| 0 s   | 500 / 0.9 MB        | 500 / 1.1 MB      | 500 / 1.1 MB      |
| 10 s  | 50,000 / 5.5 MB     | 50,000 / 5.4 MB   | 50,000 / 5.4 MB   |
| 30 s  | 150,000 / 15.6 MB   | 1,990 / 9.2 MB    | 2,000 / 1.4 MB    |
| 60 s  | 300,000 / 30.0 MB   | 1,628 / 9.2 MB    | 2,000 / 1.3 MB    |
| 120 s | 600,000 / 62.1 MB   | 1,911 / 9.2 MB    | 2,000 / 1.4 MB    |
| 300 s |                     | 1,999 / 9.3 MB    | 1,508 / 1.3 MB    |
| 600 s |                     | 1,992 / 9.3 MB    | 2,000 / 1.3 MB    |

Each cell is buckets in the map / heap in use. Samples at multiples of 30 seconds land just
after a sweep; between sweeps the map refills with new clients and the heap rises (up to
13.7 MB without the rebuild, 9.9 MB with it), then drops at the next sweep.

- **Without the janitor** memory grows in a straight line, about 100 bytes per client ever
  seen (31 MB a minute at this rate), with no sign of leveling off.
- **With the janitor** both 10-minute runs saw 3,000,000 distinct clients and held a flat
  band from the first sweep to the end.
- **Why the rebuild.** A Go map does not give memory back when keys are deleted. Measured
  separately: a map of 1,000,000 buckets used 115 MB, still used 68 MB after every key was
  deleted, and 0.6 MB once replaced with a new map. That is the 9.2 MB floor in the middle
  column: the map stays sized for the ~100,000 clients it held before each sweep. With the
  rebuild the floor is 1.3 MB.
- **After a spike.** With the current code, 1,000,000 clients took the heap to 117.8 MB; once
  they went idle, one sweep brought it back to 0.8 MB.

## Reproducing

```sh
make bench                                                          # BenchmarkAllow and BenchmarkEvictIdleScan, -cpu 1,4,10
go test -run '^$' -bench 'BenchmarkAllow' -benchmem -cpu 1,4,10 -count 5 ./internal/limit
go test -run '^$' -bench 'BenchmarkEvictIdleScan' -cpu 1 -count 5 ./internal/limit

# CPU profile of the single mutex under contention
go test -run '^$' -bench 'BenchmarkAllow/mutex/10k' -cpu 10 -benchtime 3s \
    -cpuprofile cpu.out -o limit.test ./internal/limit
go tool pprof -top limit.test cpu.out

SOAK=2m  go test -run 'TestSoak/no-janitor' -v -timeout 0 ./internal/limit   # the leak
SOAK=10m make soak                                                          # with the janitor
```
