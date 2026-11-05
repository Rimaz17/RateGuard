# Architecture decision records

An ADR records each decision that had a real alternative: its context, the options, the
decision itself and its consequences. ADRs 0001–0006 are the six binding decisions from
§ 8 of the project overview, recorded here so the repository explains itself.

| ADR | Decision |
|---|---|
| [0001](0001-redis-failure-degrades-to-local-bucket.md) | Redis unreachable degrades to a local bucket, never to unlimited |
| [0002](0002-redis-is-the-only-clock.md) | One clock, and it is Redis's |
| [0003](0003-no-hot-key-sharding-in-v1.md) | No hot-key sharding in v1 |
| [0004](0004-the-hot-path-is-sacred.md) | The hot path is sacred |
| [0005](0005-policy-cache-ttl-pubsub-singleflight.md) | Policy cache with a 10s TTL, pub/sub invalidation and singleflight |
| [0006](0006-api-keys-sha256-two-active.md) | API keys hashed with SHA-256, two valid at once |
| [0007](0007-migrations-run-as-a-separate-step.md) | Migrations run from the binary, as a separate step |
| [0008](0008-admin-api-bearer-token.md) | The admin API requires a shared bearer token |
| [0009](0009-policy-resolution-and-default-policy.md) | How a request's policy is resolved, and the default policy |
| [0010](0010-endpoint-rule-matching-and-precedence.md) | Endpoint rule matching and precedence |
| [0011](0011-token-bucket-semantics.md) | Token bucket semantics |
| [0012](0012-api-key-cache-on-the-hot-path.md) | API key lookups are cached in process |
| [0013](0013-redis-bucket-store.md) | The Redis bucket store: key layout, script and expiry |
| [0014](0014-degraded-decisions.md) | Degraded decisions when Redis fails: breaker, local buckets, metrics and logging |
| [0015](0015-decision-endpoint.md) | The decision endpoint: request, statuses and response body |
| [0016](0016-rate-limit-headers-and-peek.md) | Rate-limit headers, the peek endpoint and error bodies |
| [0017](0017-unix-socket-for-sidecars.md) | A Unix socket for sidecar deployment |
| [0018](0018-usage-analytics-pipeline.md) | Usage analytics: counted in memory, flushed to Redis, rolled up to Postgres |
| [0019](0019-decision-and-cache-metrics.md) | Decision outcome, latency and cache metrics |
| [0020](0020-how-latency-and-the-key-ceiling-are-measured.md) | How latency and the per-key ceiling are measured |

New ADRs take the next number. To change an accepted ADR, mark it superseded and write
a new one rather than editing it in place.
