# ADR 0005: Policy cache with a 10s TTL, pub/sub invalidation and singleflight

- **Status:** Accepted (inherited from the project overview, § 8.5)
- **Date:** 2026-11-04

## Context

Resolving a policy from Postgres on every request would put the database on the hot path
(see ADR 0004). A plain TTL cache fixes that, but then a plan change takes up to the full
TTL to reach every instance, which confuses an admin who has just upgraded a customer.

## Options

1. **No cache:** Postgres sits on the hot path.
2. **TTL only:** changes land slowly and unpredictably.
3. **TTL plus active invalidation plus stampede protection.**

## Decision

Option 3:

- Each instance keeps an in-process policy cache with a **10-second TTL**.
- Every policy write publishes the changed client or policy ID on the Redis channel
  `rg:policy:invalidate`. Every instance subscribes and evicts that entry immediately.
- Concurrent misses for the same key collapse into one Postgres query through
  `golang.org/x/sync/singleflight`.

The TTL is only a safety net for a dropped pub/sub message. It is not the primary
mechanism.

## Consequences

- Plan changes reach every instance within milliseconds in the normal case, and within
  10 seconds in the worst case.
- A cold cache under load cannot stampede the database.
- Each instance holds one long-lived Redis subscription, which must reconnect, and must
  flush its cache when it does, because messages published while it was disconnected are
  lost.
