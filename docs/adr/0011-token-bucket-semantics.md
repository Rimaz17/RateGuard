# ADR 0011: Token bucket semantics

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

Phase 4 builds the token bucket engine that every decision runs through: in memory now,
as the Redis-outage fallback later (ADR 0001), and as the reference the phase 5 Lua
script must agree with. The overview fixes the algorithm (a bucket of `capacity`
tokens refilled lazily at `refill_rate` a second, one token per request) and the
response fields (`limit`, `remaining`, `reset_after`, `retry_after`), but not what
`reset_after` means for a bucket that refills continuously, nor how the engine treats
time that runs backwards or arithmetic that is not exact.

Any of these, decided differently by the in-memory and Redis implementations, would
make a decision depend on which store answered.

## Options

For `reset_after`:

1. **Time until the next token.** It then equals `retry_after` whenever a request is
   rejected, and tells an allowed caller nothing about its burst allowance.
2. **Time until the bucket is full again.** After that long without requests, the
   caller has its whole `limit` available, which is what "the allowance resets" means.
3. **The end of a fixed window.** A token bucket has no windows; inventing one would
   report a reset that does not happen.

## Decision

Option 2 for `reset_after`, together with the rest of the engine's contract:

- **State** is a token count (a float) and the time of the last refill. A bucket that
  has never been used is full.
- **Refill** is lazy: on each use, `tokens = min(capacity, tokens + elapsed × rate)`.
  No timers run.
- **Time** is an input, never read by the engine (ADR 0002). The Redis store will pass
  Redis's clock; the in-memory store passes its own process's clock, which is fine
  because its state never leaves the process.
- **Zero or negative elapsed time** refills nothing. When time runs backwards, the last
  refill time is kept rather than moved back, so the same interval is never counted
  twice when the clock catches up. Until it does, nothing refills, so `retry_after` and
  `reset_after` include the time left until the clock reaches the last refill again.
- **Capacity changes** apply at once: tokens above a lowered capacity are clamped; a
  raised capacity refills gradually rather than jumping.
- **A request is allowed** when at least one token is available, and takes one.
  Floating-point refill can land a hair below a whole token at the exact moment it
  should be whole (100/60 tokens a second for 0.6 s), so a shortfall of up to 10⁻⁹ of
  a token counts as whole. Leniency on that scale is invisible; refusing a request at
  the instant its token arrived would not be. In time, it lets a token through
  10⁻⁹ / `rate` seconds early: 0.6 ns on the Free plan, 86 µs at one request a day.
- **`remaining`** is the whole tokens left after the decision.
- **`retry_after`**, sent only on rejection, is the time until one token is available.
  Retrying after it is always allowed if nothing else has used the bucket.
- **`reset_after`** is the time until the bucket is full, or zero if it already is.
- Durations are rounded up to the nanosecond, and saturate at the largest
  `time.Duration` instead of overflowing when a rate is tiny. The HTTP layer (phase 6)
  rounds them up to whole seconds.
- **An idle bucket that has refilled to capacity** is indistinguishable from one that
  never existed, so a store may delete it. This bounds memory without changing any
  decision. The only exception is a capacity raised while the bucket sat idle: the
  re-created bucket starts at the new capacity rather than refilling to it, which is
  lenient by at most the difference, once.

## Consequences

- The engine is a pure function of state, limit and time, so it can be tested
  exhaustively without any network, and the Lua script has an exact reference.
- `remaining` can stay 0 for a while after an allowed request, until a whole token has
  refilled; `reset_after` keeps counting down meanwhile.
- The 10⁻⁹ allowance means a burst can never exceed `capacity`, and over any interval of
  length T, allowed requests never exceed `capacity + rate × T` by more than a
  billionth of a request.
