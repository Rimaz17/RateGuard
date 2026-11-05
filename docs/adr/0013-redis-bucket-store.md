# ADR 0013: The Redis bucket store

- **Status:** Accepted
- **Date:** 2026-11-05

## Context

Phase 5 moves bucket state into Redis so that every instance enforces one shared limit.
ADRs 0002 and 0003 fix the outline: a Lua script does the refill on Redis's clock and
returns relative values only, and every key comes from one `keyFor` function. ADR 0011
defines what the bucket does. This ADR records the details those leave open: the key
layout, the stored fields, what the script returns, and how keys expire.

## Options

For the script's result:

1. **The finished decision:** the script computes `remaining`, `reset_after` and
   `retry_after` itself. The duration arithmetic of ADR 0011 (rounding up, saturating,
   the backwards-clock lag) then exists twice, in Lua and in Go.
2. **The bucket after the decision, in relative terms:** whether the request was
   allowed, the token balance, and how far the bucket's last refill lies ahead of
   Redis's clock (zero unless Redis's clock went backwards). Go builds the decision
   with the same function the in-memory store uses.

For the time resolution, the overview's data model suggests milliseconds; Redis's `TIME`
gives microseconds.

## Decision

- **Keys.** `keyFor(clientID, endpoint)` in `internal/limiter` is the only function that
  builds one: `rg:bucket:<scope>:<caller>`.
  - The scope is `all` for the policy's own limit, or the ID of the endpoint rule that
    matched (ADR 0010).
  - The caller is `client:<id>` for a keyed caller, or `ip:<address>` otherwise.
  - An IPv6 caller is keyed by its `/64` network. A single host is usually given a
    whole `/64`, so a per-address bucket would let it rotate addresses and never be
    limited.
  - The policy ID is not part of the key, so a client keeps its bucket when its plan
    changes and the new capacity applies as ADR 0011 describes.
- **State** is a hash with two fields: `tokens` and `last_refill_us`, both written with
  17 significant digits so no precision is lost. Microseconds are kept rather than the
  overview's milliseconds, so the stored time matches the clock the script reads.
- **The script** (`internal/limiter/scripts/bucket.lua`) ports ADR 0011's refill and
  take step by step, including the 10⁻⁹-token tolerance and the backwards-clock rule. It
  returns option 2: `allowed`, the balance and the lag. Those are relative values, so
  no instance ever sees Redis's absolute time (ADR 0002). Go turns them into a decision
  with the in-memory store's code, so both stores report identical durations from
  identical state.
- **Writes.** An allowed take writes the new state. A refused one writes nothing:
  refill is lazy and additive, so leaving the state as it was decides every later
  request exactly as writing the refilled state would, and a flood of refused requests
  costs Redis no writes. A peek never writes.
- **Expiry.** Each write sets the key to expire when the bucket will be full again
  (`PEXPIRE`, rounded up to the millisecond, capped at 10¹³ ms). A full bucket decides
  exactly like a missing one (ADR 0011), so expiry changes no decision and the key space
  holds only buckets that are in use.
- **Loading.** The script is run with `EVALSHA`. go-redis falls back to `EVAL` once if
  Redis does not have it cached, for example after a restart.
- **No client retries.** go-redis retries a failed command by default. A script whose
  reply is lost but which did run would then take a second token, so retries are turned
  off on RateGuard's Redis client. A failed call is a failure, and the circuit breaker
  (ADR 0014) decides what happens next.

## Consequences

- The script and the in-memory engine can be compared directly. The integration tests
  replay real script calls through the engine and expect the same decision.
- Redis holds one small hash per active caller and scope, and nothing for idle callers.
- With the hash in Redis, an operator can inspect a bucket with `HGETALL`. The stored
  time is Redis's own clock, so it means nothing on any other machine, which is
  intended.
- Sharding a hot key later (ADR 0003) changes only `keyFor`.
