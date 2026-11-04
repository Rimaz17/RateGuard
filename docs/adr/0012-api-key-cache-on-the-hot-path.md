# ADR 0012: API key lookups are cached in process

- **Status:** Accepted
- **Date:** 2026-11-05

## Context

`/v1/check` identifies a keyed caller by looking its key up by prefix and comparing
digests (ADR 0006). Until now that lookup read Postgres on every request, and ADR 0004
forbids Postgres I/O on the hot path. Policies already avoid it with a cached resolver
(ADR 0005); keys need the same treatment before `/v1/check` exists.

A cache must not keep a revoked key working for long, and it must not let an attacker
grow memory without bound.

## Options

1. **No cache:** one indexed Postgres read per keyed decision. This breaks ADR 0004.
2. **Replicate every active key into every instance:** no misses at all, but every
   instance holds and refreshes the whole key table, and a new key needs a broadcast
   before it works anywhere.
3. **The policy cache's design, applied to keys:** a per-prefix TTL cache with
   singleflight on a miss, and pub/sub invalidation when a key is revoked.

## Decision

Option 3, reusing the same cache code, which moves from `internal/policy` to
`internal/cache`:

- A cache entry is the stored key for one prefix: digest, client, revoked flag and
  client status. The digest comparison still runs on every request, in constant time,
  so caching changes nothing about how a key is checked.
- Entries live for **10 seconds**, the same safety net as ADR 0005.
- Revoking a key publishes `client:<id>` on `rg:policy:invalidate`, the message a
  policy assignment already sends. Every instance drops all cached keys of that client.
  The instance that took the write drops them before it answers.
- A miss for a prefix that does not exist is **not** cached. Real keys bound the cache's
  size; caching the prefixes of made-up keys would let anyone fill memory with them.
  Each such request costs one indexed lookup, shared between concurrent requests for
  the same prefix.

## Consequences

- After warm-up, a keyed decision reads no database, as ADR 0004 requires.
- A revoked key stops working on every instance within milliseconds. If the pub/sub
  message is lost, it stops within 10 seconds, the same bound plan changes have.
- A newly issued key works at once: nothing could have cached it before it existed.
- Requests with made-up keys still reach Postgres. Limiting failed authentications is
  future work, noted in the README.
