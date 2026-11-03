# ADR 0001: Redis unreachable degrades to a local bucket, never to unlimited

- **Status:** Accepted (inherited from the project overview, § 8.1)
- **Date:** 2026-11-04

## Context

Bucket state lives in Redis so that every instance enforces one shared limit. Redis can
still become slow or unreachable, and the limiter has to answer `/v1/check` in some way
while it is.

## Options

1. **Fail open:** allow everything while Redis is down. An attacker gets the whole API
   the moment Redis hiccups.
2. **Fail closed:** reject everything. A limiter outage becomes an outage of every
   service it protects.
3. **Degrade to a local bucket:** enforce an approximate limit from in-process state.

## Decision

Option 3 is the default. A `sony/gobreaker` circuit breaker wraps every Redis call.
After a threshold of consecutive failures it opens, and decisions go to the in-memory
token bucket (the same engine as phase 4) sized at `capacity / RG_INSTANCE_COUNT`, so N
instances still roughly match the real limit. The breaker half-opens periodically, and
buckets resync from Redis on recovery.

`RG_REDIS_FAILURE_MODE` takes `local` (the default), `open` or `closed`, so operators
who want the blunt behaviour can choose it.

## Consequences

- While degraded, enforcement is approximate: uneven load across instances lets a
  client go somewhat over or under the true limit.
- Every degraded decision increments `rateguard_degraded_decisions_total` and logs at
  WARN, so a degradation is visible and never silent.
- The in-memory store has to be production-grade, not a test double.
- `RG_INSTANCE_COUNT` is operator-supplied and can go stale when the deployment scales.
