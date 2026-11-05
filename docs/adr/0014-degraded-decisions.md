# ADR 0014: Degraded decisions when Redis fails

- **Status:** Accepted
- **Date:** 2026-11-05

## Context

ADR 0001 sets the outline: a `sony/gobreaker` circuit breaker wraps Redis, and while it
is open, decisions fall back to an in-memory bucket of `capacity / RG_INSTANCE_COUNT`,
or to `open` or `closed` if the operator chooses. Building it raised questions the
outline leaves open: what counts as a failure, how long a call may take, how a local
bucket is sized when the division is not exact, what the response says while degraded,
and how loudly to log.

The logging question is a conflict between two binding decisions. ADR 0001 (overview
§ 8.1) says every degraded decision logs at WARN. ADR 0004 (§ 8.4) says `/v1/check`
does no synchronous logging. During an outage, a line per decision would mean thousands
of lines a second on the hot path, at exactly the moment the service is under strain.
The project owner chose a rate-limited WARN on 2026-11-05.

The same day, the owner also chose to add the Prometheus counter that ADR 0001 requires,
and the `/metrics` endpoint that serves it, in phase 5 rather than phase 7.

## Decision

- **One breaker per instance** wraps every bucket call. Each call is bounded by
  `RG_REDIS_TIMEOUT` (default 100ms), so a hung Redis costs at most that much per
  request until the breaker opens.
- **A failure** is any error from the Redis store, timeouts included, except the
  caller's own cancellation. A caller that has gone away says nothing about Redis: its
  request is neither a success nor a failure for the breaker, and it gets an error
  rather than a decision, since nobody is waiting for one.
- **The breaker opens** after `RG_REDIS_BREAKER_FAILURES` consecutive failures
  (default 5). It stays open for `RG_REDIS_BREAKER_COOLDOWN` (default 5s), then lets one
  probe request through. If the probe succeeds the breaker closes; if not, it opens
  again for another cooldown.
- **A degraded decision** is any decision Redis did not make: while the breaker is open,
  while a probe is in flight, and for the request whose call just failed. That request
  gets a degraded decision, not an error.
- **`local` (the default)** decides from the in-memory store, under the same bucket key,
  with capacity `max(1, ⌊capacity / N⌋)` and refill rate `rate / N`, where N is
  `RG_INSTANCE_COUNT`. Rounding down keeps N instances at or under the real limit,
  except when the capacity is below N, where each instance still allows one request.
  The response reports the policy's capacity as `limit`, and the local bucket's
  balance as `remaining`.
- **`open`** allows the request and reports the full limit as remaining, since nothing
  is being counted.
- **`closed`** refuses the request with `retry_after` and `reset_after` equal to the
  cooldown, the earliest moment Redis will be tried again.
- **Recovery.** Once the breaker closes, Redis's buckets are authoritative again and the
  local ones are ignored until the next outage; the sweep removes them once full. Nothing
  is merged back. A caller can therefore get up to its local allowance on top of its
  Redis balance across an outage, which is within the approximation ADR 0001 accepts.
- **Metrics** (served on `GET /metrics`):
  - `rateguard_degraded_decisions_total{mode}` counts every degraded decision.
  - `rateguard_redis_errors_total` counts failed Redis calls. Calls the open breaker
    never made are not errors.
  - `rateguard_redis_breaker_state` is 0 when closed, 1 when half-open and 2 when open.
- **Logging.**
  - The breaker opening logs a WARN, and closing again logs an INFO.
  - While decisions are degraded, at most one WARN is written every 10 seconds, with
    the number of degraded decisions since the previous one.
  - The other transitions (a probe going out or failing) are DEBUG.

## Consequences

- An outage is visible within one log line and one scrape, without a log line per
  request.
- The counter still counts every degraded decision, so the metric, not the log, is the
  exact record.
- `RG_INSTANCE_COUNT` must be kept in step with the deployment by the operator. Too low,
  and an outage lets callers through at up to N times their limit; too high, and it
  refuses them early.
- Each instance learns of an outage on its own, after its own five failures. Until then,
  each failed call costs up to `RG_REDIS_TIMEOUT`.
- `/metrics` is unauthenticated, like `/healthz`. Its labels never carry a client ID or
  key (CLAUDE.md § 11), so it reveals only aggregate behaviour.
