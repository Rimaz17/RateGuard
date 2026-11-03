# ADR 0002: One clock, and it is Redis's

- **Status:** Accepted (inherited from the project overview, § 8.2)
- **Date:** 2026-11-04

## Context

Lazy refill computes a bucket's tokens from the time elapsed since its last refill. If
several instances compute that from their own clocks, a few hundred milliseconds of skew
gives different balances for the same stored state, and the shared limit quietly stops
being shared.

## Options

1. **Instance clocks plus NTP:** this assumes synchronisation the service cannot verify.
2. **The client passes a timestamp:** this trusts the caller with its own limit.
3. **Redis's clock inside the script:** there is exactly one time source.

## Decision

Option 3. The refill-and-consume Lua script calls `redis.call('TIME')` and does all
refill arithmetic itself. It returns only **relative** values (seconds until reset),
never absolute timestamps, so no instance ever converts between clocks.
`X-RateLimit-Reset` is sent as delta-seconds.

Instance clocks are used only for log timestamps and measuring request duration.

## Consequences

- Clock skew between instances cannot affect bucket state.
- The in-memory engine (phase 4) takes time as an input rather than reading it, so the
  Redis store can supply Redis time and tests can supply any time.
- Delta-seconds headers are unambiguous to clients, whatever their own clock says.
