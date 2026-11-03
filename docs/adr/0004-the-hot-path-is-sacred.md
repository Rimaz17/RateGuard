# ADR 0004: The hot path is sacred

- **Status:** Accepted (inherited from the project overview, § 8.4)
- **Date:** 2026-11-04

## Context

Every protected request now makes an extra network call to RateGuard. That cost is real,
and claiming it is negligible without measuring it is how a service like this loses the
trust of its first integrator.

## Options

1. **Central deployment only:** simple, but every caller pays a network hop.
2. **Sidecar only:** cheap per call, but policy and state are harder to share.
3. **Support both, and publish the measured overhead of each.**

## Decision

Option 3. `/v1/check` does no Postgres I/O, no synchronous logging and no synchronous
analytics writes. Policies come from the in-process cache (ADR 0005), and usage counters
are buffered and flushed off the hot path. A Unix-socket listener supports sidecar
deployment, and the reference middleware keeps a keep-alive connection pool. Central
deployment stays the default.

Phase 7 publishes measured p50 and p99 latency for both topologies.

## Consequences

- No per-allowed-request logging, ever. It would dominate both latency and log volume.
- Analytics can lag real time by up to one flush interval, and counters buffered at the
  moment an instance crashes are lost.
- Any proposed change that adds I/O to `/v1/check` needs a new ADR.
