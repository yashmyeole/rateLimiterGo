# Design decisions

Short records of the choices that shaped the code: what was decided, why, and what it
costs. Measured numbers come from the machine described in [RESULTS.md](RESULTS.md).

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

**Next.** A token bucket and a sliding-window counter in Redis, both computed inside a Lua
script so the read-and-update is atomic and time comes from Redis, not from each proxy.

## Rate limits live in Redis, shared by all replicas

**Decision.** With more than one proxy replica, counts must live in one shared place.

**Why.** Each replica limiting on its own gives a client the full limit at every replica.
Measured with two replicas, an in-memory limit of 10, and 40 requests alternating between
them: 20 allowed (10 per replica). With the fixed window in Redis, the same 40 requests got
exactly 10, and 200 requests sent at once across both replicas also got exactly 10, because
`INCR` is atomic in Redis.

**Cost.** Every request now makes a network round trip to Redis, and a Redis outage
becomes a rate-limiter outage. For now the middleware fails open (requests are allowed when
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
