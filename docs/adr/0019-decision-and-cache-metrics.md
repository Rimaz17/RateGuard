# ADR 0019: Decision and cache metrics

- **Status:** Accepted
- **Date:** 2026-11-05

## Context

CLAUDE.md § 11 sets the minimum metrics: decisions by outcome, a decision latency
histogram, Redis errors, breaker state, degraded decisions and the cache hit ratio.
Phase 5 delivered the last four that concern Redis (ADR 0014). This records the rest.
Every label must be low-cardinality: never a client, a key or an address.

## Options

For where latency is measured:

1. **Inside `limiter.Service`:** resolution plus the bucket call. It leaves out
   identifying the caller, which includes the API key cache, and the JSON.
2. **Around the HTTP route:** everything the server does for a decision request,
   whatever its status.

For the cache hit ratio:

1. **A ratio gauge:** this loses the counts behind it and cannot be aggregated across
   instances.
2. **Hit and miss counters:** the ratio is computed in PromQL.

## Decision

Option 2 in both cases.

- **`rateguard_decisions_total{outcome}`**: one count per check on `/v1/check`.
  `outcome` is `allowed`, `rejected`, `no_policy` or `error`. A check whose caller left
  before it was decided is not counted, nor is a peek. The counts come from
  `limiter.Service`, from the same place as usage (ADR 0018), so metrics and analytics
  count the same checks.
- **`rateguard_decision_duration_seconds{operation}`**: a histogram of how long the
  server took to answer, measured by middleware in front of identification. `operation`
  is `check` or `peek`, and every status counts. The buckets run from 100µs to 1s, fine
  below a millisecond, where a decision normally lands. The instance clock is used,
  which ADR 0002 allows for request duration.
- **`rateguard_cache_lookups_total{cache,result}`**: one count per lookup in the
  in-process caches on the decision path. `cache` is `api_key`, `client_policy`,
  `policy` or `default_policy`, and `result` is `hit` or `miss`. A lookup that joins
  another caller's load is a miss. The hit ratio is
  `sum by (cache) (rate(...{result="hit"}[5m])) / sum by (cache) (rate(...[5m]))`.
- **`rateguard_usage_dropped_total`**: checks lost to analytics (ADR 0018).
- Every series exists from startup at zero, so dashboards and alerts see "none yet"
  rather than no data.

## Consequences

- Fifteen label combinations per instance (4 outcomes, 2 operations, 8 cache results,
  1 drop counter), whatever the traffic.
- The histogram measures RateGuard's own time, not the network. The load tests measure
  the caller's view.
