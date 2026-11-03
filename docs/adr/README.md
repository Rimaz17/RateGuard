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

New ADRs take the next number. To change an accepted ADR, mark it superseded and write
a new one rather than editing it in place.
