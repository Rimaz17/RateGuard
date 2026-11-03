# ADR 0003: No hot-key sharding in v1

- **Status:** Accepted (inherited from the project overview, § 8.3)
- **Date:** 2026-11-04

## Context

Every request from one client goes through one Redis key, and Redis runs scripts on a
key one at a time. A single very busy client therefore has a throughput ceiling.

## Options

1. **Shard now:** K sub-buckets per client, each with `capacity / K`. This is real
   complexity for traffic that does not exist yet.
2. **Never shard:** this leaves the ceiling unknown and undocumented.
3. **Defer, but make sharding cheap:** measure the ceiling and publish it.

## Decision

Option 3. Every bucket key comes from a single `keyFor(clientID, endpoint string) string`
function, so introducing sub-buckets later means changing that one function. The phase 7
k6 load test includes a single-hot-key run to find the per-key ceiling, and the README
publishes that number next to this note.

Above the ceiling, the documented path is K sub-buckets of `capacity / K`, picked at
random.

## Consequences

- Nothing else may build bucket keys by hand, and code review should reject any that do.
- The published ceiling has to be re-measured when the Lua script or the Redis topology
  changes.
