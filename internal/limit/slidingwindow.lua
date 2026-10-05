-- Sliding-window counter, run atomically inside Redis.
--
-- A fixed window resets all at once, so a client can spend a full quota just before the
-- boundary and another just after it. This keeps the previous window's count too and
-- estimates the requests in the last full window as:
--
--   previous count * (share of the previous window still inside it) + current count
--
-- assuming the previous window's requests were spread evenly.
--
-- KEYS[1]  the client's state: a hash with fields start (ms), curr and prev
-- ARGV[1]  limit: requests allowed per window
-- ARGV[2]  window length in ms
-- Returns  {allowed (1 or 0), whole requests left, retry after in ms}

local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])

-- Time comes from Redis, so every proxy replica agrees on where windows start. That is
-- also why the window rotation happens in here, on one key, instead of the caller
-- naming a key per window from its own clock.
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local start = now - (now % window)

local h = redis.call('HMGET', KEYS[1], 'start', 'curr', 'prev')
local hstart = tonumber(h[1]) or start
local curr = tonumber(h[2]) or 0
local prev = tonumber(h[3]) or 0

if start ~= hstart then
  if start - hstart == window then
    prev = curr -- moved on by exactly one window
  else
    prev = 0 -- a whole window or more went by with no requests
  end
  curr = 0
end

local elapsed = now - start
local weight = (window - elapsed) / window

local allowed, retry = 0, 0
if prev * weight + curr + 1 <= limit then
  curr = curr + 1
  allowed = 1
elseif curr + 1 <= limit then
  -- The previous window's share is in the way; it shrinks as this window goes on.
  -- Wait until prev * (window - e) / window + curr + 1 <= limit.
  retry = math.ceil(window - (limit - curr - 1) * window / prev - elapsed)
else
  -- This window is full. In the next one this window's count becomes prev:
  -- wait for that window, then until its share has shrunk enough.
  retry = math.ceil(window - elapsed + window - (limit - 1) * window / curr)
end

redis.call('HSET', KEYS[1], 'start', start, 'curr', curr, 'prev', prev)
-- Older than two windows, the state no longer affects any decision.
redis.call('PEXPIRE', KEYS[1], window * 2)

return {allowed, math.max(0, math.floor(limit - (prev * weight + curr))), math.max(retry, 0)}
