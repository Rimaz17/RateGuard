# ADR 0018: The usage analytics pipeline

- **Status:** Accepted
- **Date:** 2026-11-05

## Context

The overview asks for basic usage analytics (§ 3F): totals of requests processed,
allowed and rejected; a breakdown per client with a ranking of the most throttled; and
volume over time by the minute or hour. Its data model (§ 6) says usage counters are
"incremented in Redis and flushed to `usage_rollups` periodically". ADR 0004 (§ 8.4)
says `/v1/check` makes no synchronous analytics writes. A Redis increment on every
check would be exactly such a write: a second round trip per decision.

## Options

1. **Increment Redis on every check.** Literal to § 6, but it breaks ADR 0004.
2. **Count in memory and write to Postgres from each instance.** Simplest, but it
   departs from § 6, Postgres load grows with the instance count, and a crash loses
   up to one Postgres write interval of counts.
3. **Count in memory, flush to Redis, roll up to Postgres.** Both texts hold, in
   three steps, with no I/O on the decision path.

## Decision

Option 3, chosen by the project owner on 2026-11-05.

- **Record.** `limiter.Service.Check` hands each decided check to an in-process
  `analytics.Recorder`: one map update under a mutex, keyed by (minute, client). A
  caller identified only by IP counts as client `""`; its address is never recorded.
  Peeks, refusals for lack of a policy, errors and checks whose caller left are not
  usage.
- **Bound.** The recorder holds at most 100,000 (minute, caller) slots. Beyond that,
  checks that would need a new slot are dropped and counted in
  `rateguard_usage_dropped_total`. Only a long Redis outage with many active clients
  gets there.
- **Flush** (`RG_USAGE_FLUSH_INTERVAL`, default 1s). Each instance moves its counts
  into Redis: one hash per minute, `rg:usage:<minute>`, with fields `<client>:a` and
  `<client>:r` (`-` for callers known by IP), plus a sorted set `rg:usage:minutes`
  indexing the minutes. A Lua script adds up to 250 callers' counts at a time,
  atomically. Counts that fail go back into the recorder for the next flush.
- **Roll up** (`RG_USAGE_ROLLUP_INTERVAL`, default 10s). Every instance drains up to
  120 minutes, oldest first. A Lua script reads and deletes one minute's hash in one
  step, so two instances never take the same counts and no leader is needed. The
  counts are upserted into `usage_rollups`, adding to any existing row. If Postgres
  fails, they go back into Redis; if that fails too, they are lost, logged at ERROR and
  counted as dropped.
- **Table.** `usage_rollups` has one row per (client, minute). `client_id` is NULL for
  callers known by IP, with `UNIQUE NULLS NOT DISTINCT` so they also get one row per
  minute. A CHECK keeps `bucket_start` on a UTC minute, and an index on `bucket_start`
  serves range queries. Counts for a client that no longer exists are discarded rather
  than failing the batch forever.
- **Time.** The minute is read from the instance clock when the check is recorded.
  This is a record of when something happened, like a log timestamp, and touches no
  bucket (ADR 0002 still holds for every decision). Skew between instances can only move
  a count into a neighbouring minute.
- **Report.** `GET /v1/analytics/usage` (admin token) takes `from`, `to`,
  `granularity` (`minute` or `hour`, default `hour`) and `top` (1–100, default 10). The
  range defaults to the last 24 hours, is widened to whole points in UTC, and may span
  at most 1,440 points. The answer, read in one repeatable-read snapshot, holds totals,
  the unidentified share, the clients ranked by rejections then requests, and a series
  with a point for every interval, zeros included.

## Consequences

- A decision does no analytics I/O. Its only cost is a map update.
- Reports lag real time by about the flush plus the rollup interval (about 11 seconds
  by default).
- An instance that crashes loses up to one flush interval of its counts. A rollup that
  crashes between draining Redis and committing to Postgres loses that batch. A flush
  whose reply is lost to a timeout may have been applied, so its retry can count the
  batch twice. Analytics are approximate in these rare cases, and
  `rateguard_usage_dropped_total` counts every loss the service can see.
- `usage_rollups` grows by one row per active caller per minute. Nothing prunes it in
  v1.
- Counting time from the instance clock is a narrow, documented exception to "instance
  clocks only for logs and request duration": it is not bucket arithmetic.
