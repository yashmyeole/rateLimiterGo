-- Token bucket, run atomically inside Redis: no other command can run between the read
-- and the write below, so two requests can never spend the same token.
--
-- KEYS[1]  the client's bucket: a hash with fields tokens and ts (ms)
-- ARGV[1]  rate: tokens added per second
-- ARGV[2]  burst: bucket capacity
-- Returns  {allowed (1 or 0), whole tokens left, retry after in ms}

local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])

-- Time comes from Redis, so every proxy replica agrees on it.
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

local b = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(b[1]) or burst -- a new client starts with a full bucket
local ts = tonumber(b[2]) or now

-- Lazy refill, as in the in-memory version.
tokens = math.min(burst, tokens + (now - ts) / 1000 * rate)

local allowed, retry = 0, 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  retry = math.ceil((1 - tokens) / rate * 1000)
end

redis.call('HSET', KEYS[1], 'tokens', tokens, 'ts', now)
-- After burst/rate seconds idle the bucket would be full again, which is the same as
-- having no key at all, so let Redis delete it then.
redis.call('PEXPIRE', KEYS[1], math.ceil(burst / rate * 1000))

return {allowed, math.floor(tokens), retry}
