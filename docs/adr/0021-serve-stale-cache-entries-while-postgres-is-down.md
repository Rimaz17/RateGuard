# ADR 0021: Serve stale cache entries while Postgres is down

- **Status:** Accepted
- **Date:** 2026-11-05

## Context

Decisions read API keys and policies from in-process caches with a 10-second TTL
(ADR 0005, ADR 0012), so the hot path does no Postgres I/O (ADR 0004). When an entry
expired, the next request reloaded it from Postgres. If that reload failed, the error
reached the caller, and errors were never cached.

So during a Postgres outage, `/v1/check` kept answering for at most 10 seconds. After
that, every decision for a caller whose entries had expired failed with
`500 internal server error`. This was found while writing the testing guide and
reproduced on the compose stack. Redis, the only dependency a decision actually needs,
was healthy all along. An outage of the database that holds configuration was taking
down rate limiting itself, which goes against the reason ADR 0004 keeps Postgres off the
hot path.

## Options

1. **Keep failing:** the status quo. It is simple, but a database outage becomes an
   outage of every application that calls RateGuard.
2. **Fall back to a default when a load fails:** "unlimited" is ruled out by ADR 0009,
   and refusing everything is the outage under another name.
3. **Longer TTLs:** this only delays the failure, and it slows down the safety net for a
   missed invalidation, which is the TTL's whole purpose (ADR 0005).
4. **Serve the last loaded value when a reload cannot reach Postgres,** for a bounded
   time.

## Decision

Option 4, implemented once in `internal/cache` (`cache.WithStale`) and turned on for the
API key cache and all three resolver caches.

- **Expired entries stay in memory** for up to 15 minutes past their TTL
  (`client.DefaultKeyMaxStale`, `policy.DefaultMaxStale`). They are not served while
  fresh data can be loaded: an expired entry is reloaded as before.
- **Only an outage falls back.** A failed reload serves the last value if the error is a
  failure to reach Postgres: a connection error, a timeout, anything else. It does not
  fall back if the error is Postgres's answer, `domain.ErrNotFound` or
  `domain.ErrInvalid`. An answer drops the entry. So a key or policy the database has
  disowned is never served again, even if Postgres then goes down.
- **Changes still win.** Invalidations and flushes delete entries as before. A stale
  value is therefore never one the cache was told is out of date. Pub/sub invalidation
  keeps working during a Postgres outage, because it runs over Redis. A lost
  subscription flushes the caches (ADR 0005), which also removes every stale fallback.
  No admin write can happen while Postgres is down, so no change can be missed during
  the outage either.
- **No waiting.** A caller waits at most 100 ms for a reload when an expired value can
  be served. A healthy Postgres reloads in about a millisecond. A hanging one would
  otherwise hold the caller for the whole 5-second load timeout, once per cache a
  check walks through (up to four). Together that outlasts the server's 10-second write
  timeout, which is what the compose stack showed when Postgres was stopped. Past
  100 ms the caller gets the expired value, and the reload carries on: it stores its
  result if it succeeds, or marks the entry failing if it does not. Lookups of a
  failing entry get the stale value at once, and the source is retried in the
  background about once a second per entry (sharing the singleflight load), not once
  per request. Once a retry succeeds, the entry is fresh again.
- **Visible.** Lookups answered from a stale entry count as
  `rateguard_cache_lookups_total{result="stale"}`, next to `hit` and `miss`. This
  extends ADR 0019's label set; every series still exists from startup at zero.
  `/readyz` reports Postgres as unavailable throughout.

## Consequences

- A Postgres outage of up to 15 minutes no longer stops decisions for callers whose
  key and plan were cached. Admin writes, analytics rollups and readiness still fail,
  as they should.
- A caller first seen during the outage has nothing cached, so its check still fails
  with 500. A key revoked or a plan changed during the outage cannot happen, because
  the admin API needs Postgres.
- After the max stale age, entries are dropped and checks fail again. This bounds how
  long an entry missed by both pub/sub and the TTL could be served.
- Memory: an expired entry stays in memory until a lookup finds it past its max stale
  age, rather than until its next lookup after the TTL. The caches hold only keys,
  client assignments and policies that exist in Postgres (unknown prefixes are not
  cached, ADR 0012), so the number of entries stays bounded by the database.
- `cache.Observer.Lookup` now takes a `cache.Result` (hit, miss or stale) instead of a
  boolean.
