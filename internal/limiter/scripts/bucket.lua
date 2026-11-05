-- Refill-and-consume for one token bucket, run atomically by Redis.
--
-- This is the engine of ADR 0011 (internal/limiter/engine.go) ported step by
-- step, with Redis's clock as the only clock (ADR 0002). It returns relative
-- values only, and Go builds the decision from them with the same code the
-- in-memory store uses (ADR 0013).
--
-- KEYS[1]  the bucket's key, built by keyFor
-- ARGV[1]  capacity: a whole number, at least 1
-- ARGV[2]  refill rate in tokens per second: positive and finite
-- ARGV[3]  "take" to consume a token if one is available, "peek" to look only
--
-- Returns {allowed, tokens, lag_us}:
--   allowed  1 if the request is allowed, else 0
--   tokens   the balance after the decision
--   lag_us   microseconds by which the last refill lies ahead of Redis's
--            clock: 0 unless that clock went backwards
-- tokens and lag_us are decimal strings, because Redis truncates Lua numbers
-- in a reply to integers.

local capacity = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local mode = ARGV[3]
if not capacity or not (capacity >= 1) or not rate or not (rate > 0) or rate == math.huge
    or (mode ~= 'take' and mode ~= 'peek') then
  return redis.error_reply('ERR rateguard bucket: invalid arguments')
end

-- A shortfall of up to this much still counts as a whole token (ADR 0011).
local tolerance = 1e-9

-- 17 significant digits write any double exactly; Lua's default of 14 would
-- lose precision in both the balance and the time.
local function exact(n)
  return string.format('%.17g', n)
end

local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000000 + tonumber(clock[2])

local state = redis.call('HMGET', KEYS[1], 'tokens', 'last_refill_us')
local tokens = capacity
local last = tonumber(state[2])
if last == nil or last ~= last then
  -- Never used, or expired because it had refilled (ADR 0011): full.
  last = now
else
  tokens = tonumber(state[1])
  if tokens == nil or not (tokens >= 0) then
    -- Only corrupt state can hold this; the comparison also catches NaN.
    tokens = 0
  end
  if tokens > capacity then
    -- The capacity was lowered: clamp at once.
    tokens = capacity
  end
  local elapsed = now - last
  if elapsed > 0 then
    tokens = math.min(capacity, tokens + elapsed / 1000000 * rate)
    last = now
  end
  -- Otherwise the clock stood still or went backwards. Nothing refills and
  -- the last refill stays where it was, so no interval is counted twice.
end

local lag = math.max(last - now, 0)
local allowed = tokens >= 1 - tolerance

-- Only an allowed take changes the bucket. Refill is additive, so leaving a
-- refused or peeked bucket as it was decides every later request exactly as
-- storing the refilled balance would, without a write.
if mode == 'take' and allowed then
  tokens = math.max(tokens - 1, 0)
  -- Expire the key when the bucket will be full again: from then on it
  -- decides exactly like a missing one. Milliseconds, rounded up, at least
  -- one, and capped where PEXPIRE is still safe.
  local ttl = math.ceil(lag / 1000 + (capacity - tokens) / rate * 1000)
  ttl = math.max(1, math.min(ttl, 1e13))
  redis.call('HSET', KEYS[1], 'tokens', exact(tokens), 'last_refill_us', exact(last))
  redis.call('PEXPIRE', KEYS[1], exact(ttl))
end

if allowed then
  return {1, exact(tokens), exact(lag)}
end
return {0, exact(tokens), exact(lag)}
