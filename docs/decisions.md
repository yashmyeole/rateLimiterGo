# Design decisions

Short records of the choices that shaped the code: what was decided, why, and what it
costs. Measured numbers come from the machine described in [RESULTS.md](RESULTS.md).

## Token bucket and sliding window run as Lua scripts inside Redis

**Decision.** The Redis token bucket and sliding window are Lua scripts
(`internal/limit/tokenbucket.lua`, `slidingwindow.lua`). Each request runs one script that
reads the client's state, does the math, and writes it back.

**Why.** Both algorithms need a read, a calculation and a write. Done as separate commands
from Go, two requests can read the same state and both act on it. Measured with a burst of
10 and 200 requests over two replicas (details in [RESULTS.md](RESULTS.md)): the
read-then-write version allowed 50 to 67 when 10 workers sent 20 requests each, and all 200
when they arrived at once. Redis runs a script as one step with nothing in between, and the
Lua version allowed exactly 10 every time.

The proxy sends `EVALSHA` with the script's hash, not the source. If Redis no longer has the
script cached (after a restart or `SCRIPT FLUSH`), go-redis's `Script.Run` resends it with
`EVAL`; `TestScriptsSurviveScriptFlush` covers that. The token bucket script is checked
against the in-memory Go version: both get the same random traffic on the same clock and
every decision must match (`TestRedisTokenBucketMatchesInMemory`).

**Cost.** One Redis round trip per request: about 0.4 ms on the development machine, nearly
all of it network. The scripts themselves are cheap.

## Time comes from Redis, and each client's state is one key

**Decision.** The scripts read the time with Redis's `TIME` command instead of receiving it
from the proxy. The sliding window keeps a client's state (window start, current count,
previous count) in one hash and moves to a new window inside the script.

**Why.** Proxy replicas' clocks drift apart. If each proxy decided which window a request
belongs to, replicas would disagree near every boundary (the second flaw of the fixed window
below). The usual sliding-window design uses one key per window, which means the caller
names the key from its own clock; keeping one key per client and rotating inside the script
avoids that. Keys look like `rl:tb:{1.2.3.4}`: the braces make Redis Cluster put everything
for one client on the same node, so the scripts would also work on a cluster.

## The sliding window is an estimate

**Decision.** The sliding window is a sliding-window counter, not a sliding log.

**Why.** A sliding log stores a timestamp for every request and counts those in the last
window: exact, but memory grows with traffic. The counter stores two numbers per client and
estimates the last window as the previous window's count, weighted by how much of it still
overlaps, plus the current count. It assumes the previous window's requests were spread
evenly, which can be off when they were bunched up.

**Result.** It removes the fixed window's boundary burst. With two replicas and 10 requests
per 10 seconds, 10 requests just before a boundary were allowed and the 10 sent 0.45 s later,
just after it, were all refused (the fixed window allowed all 20).

## Fixed window in Redis is the first distributed limiter, and it has a known flaw

**Decision.** The first limiter shared across proxy replicas is a fixed window in Redis:
one counter per client per window, `INCR` on every request, expiring with the window. It is
the simplest correct way to share a count, and it shows why the token bucket and sliding
window exist.

**The flaw: bursts at the window boundary.** The window resets all at once, so a client
can spend a full quota just before a boundary and another full quota just after it.
Reproduced against real Redis with two replicas and a limit of 10 requests per 10 seconds:

| time         | requests | allowed |
|--------------|---------:|--------:|
| 23:12:59.600 |       10 |      10 |
| 23:13:00.051 |       10 |      10 |

20 requests were allowed within 465 ms, twice the limit, and all of it legal by the rules
of a fixed window. `TestFixedWindowBoundaryBurst` keeps this behavior pinned down in code.

**Second flaw: each proxy's clock decides the window.** The window a request falls in is
computed from the proxy's own clock. Replicas whose clocks disagree by a few hundred
milliseconds also disagree on where windows start, which widens the boundary problem.

**What replaced it.** The token bucket and sliding window above, both computed inside Redis
from Redis's own clock. The fixed window stays available (`-algorithm fixed-window`) for
comparison.

## Rate limits live in Redis, shared by all replicas

**Decision.** With more than one proxy replica, counts must live in one shared place.

**Why.** Each replica limiting on its own gives a client the full limit at every replica.
Measured with two replicas, an in-memory limit of 10, and 40 requests alternating between
them: 20 allowed (10 per replica). With the fixed window in Redis, the same 40 requests got
exactly 10, and 200 requests sent at once across both replicas also got exactly 10, because
`INCR` is atomic in Redis.

**Cost.** Every request now makes a network round trip to Redis (about 0.4 ms here), and a
Redis outage becomes a rate-limiter outage. For now the middleware fails open (requests are allowed when
the limiter errors); a fallback for when Redis is down comes later.

## INCR and EXPIRE are sent together in one transaction

**Decision.** The fixed window sends `INCR` and `EXPIRE` inside one `MULTI`/`EXEC`.

**Why.** As two separate commands, a crash or network error between them leaves a counter
with no expiry. Each window has its own key, so this would not block a client, but every
such key stays in Redis forever. In one transaction both happen or neither does.

## Clients are identified by connection IP, not X-Forwarded-For

**Decision.** The rate-limit key is the IP address of the TCP connection. A client's own
`X-Forwarded-For` header is ignored (the proxy also strips it before forwarding).

**Why.** This proxy is the front door. Any client can put any value in that header, so
trusting it would let one client get a fresh limit on every request by changing it.
`TestMiddlewareKeysByConnectionNotHeader` covers this.

**Cost.** Behind another load balancer (most hosting platforms), every request arrives from
that balancer's IP. That case needs a list of trusted proxies whose `X-Forwarded-For` is
believed, and is not handled yet.

## The limiter interface takes a context and returns an error

**Decision.** `Allow(ctx, key) (Decision, error)`, even though the in-memory token bucket
never fails and ignores the context.

**Why.** Limiters backed by Redis need both: a deadline for the network call, and a way to
report that Redis is unreachable. Designing for that from the start meant adding the Redis
limiter changed no callers.

## The in-memory limiter keeps a single mutex

**Decision.** One `sync.Mutex` around one map, although `sync.Map` was about 11 times
faster with 10,000 clients on 10 CPUs.

**Why.** Even under that contention the mutex makes about 7 million decisions a second, and
one map keeps the idle-bucket sweep simple. Numbers and profile in
[RESULTS.md](RESULTS.md). Revisit if the limiter shows up in a profile of the whole proxy.

## Backends are identical replicas

**Decision.** The three demo backends each serve every route (`/api/users`, `/api/orders`,
`/api/quotes`) instead of being three different services.

**Why.** Round-robin load balancing spreads traffic across copies of the same service. With
three different services, a request for `/api/users` would land on the orders service two
times out of three, and taking one backend down would make its routes disappear instead of
moving traffic to another copy.

## The proxy dials 127.0.0.1, not localhost

**Decision.** Default backend addresses use `127.0.0.1`.

**Why.** On macOS `localhost` resolves to IPv6 `::1` first. The backends listen on IPv4
only, so any other program listening on the same port over IPv6 gets the traffic instead.
This happened during development: a Docker container publishing port 9001 answered a third
of the proxy's requests with 404s.
