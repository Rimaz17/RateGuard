# Using RateGuard

How to register applications, set plans, decide requests and integrate, and how the
shared buckets, the Redis fallback, usage analytics and observability work. The
[README](../README.md) has the overview, quickstart, configuration and API summary.

## Running the stack

`make up` binds RateGuard to `127.0.0.1:8080`, Postgres to `127.0.0.1:5433` and Redis to
`127.0.0.1:6379`. To change the host ports, edit `.env`. A one-shot `migrate` service
applies the schema before RateGuard starts
([ADR 0007](adr/0007-migrations-run-as-a-separate-step.md)).

To see the shared limit across instances, `make demo` starts three RateGuard instances
behind an nginx load balancer on `127.0.0.1:8090` and runs
[scripts/demo-distributed.sh](../scripts/demo-distributed.sh): 300 concurrent requests for a
client limited to 100 (exactly 100 are allowed), then Redis is stopped (every request is
still decided, from local buckets) and started again (the circuit breakers close).
`make demo-down` stops it.

## The API contract

The `/v1/clients`, `/v1/policies` and `/v1/analytics` routes require `Authorization: Bearer <RG_ADMIN_TOKEN>`
([ADR 0008](adr/0008-admin-api-bearer-token.md)). Every error response, routing
misses included, is `{"error": "...", "request_id": "..."}`, and never carries internal
detail; a 405 also lists the accepted methods in `Allow`. `make docs` serves Swagger UI
for the contract on `127.0.0.1:8081` (read-only: try-it-out is off).

Every response carries an `X-Request-ID` header. An inbound `X-Request-ID` is reused if
it is at most 64 characters of `[A-Za-z0-9._-]`; otherwise RateGuard generates a new one.
The contract is [api/openapi.yaml](../api/openapi.yaml) (OpenAPI 3.1), updated in the same
commit as any handler.

## Registering an application and rotating its key

With the stack from the quickstart running (the token is the placeholder from
`configs/.env.example`):

```bash
TOKEN=change-me-local-only-admin-token-0000

# Register: the response holds the full key. It is shown once and never again.
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"name":"Customer App"}' http://127.0.0.1:8080/v1/clients
# {"client":{"id":"<client-id>",...},"api_key":{"id":"<key-id>","prefix":"rg_cus_...","key":"rg_cus_..._...",...}}

# Rotate without downtime: issue a second key, move your callers onto it...
curl -s -H "Authorization: Bearer $TOKEN" -X POST http://127.0.0.1:8080/v1/clients/<client-id>/keys

# ...then revoke the old one.
curl -s -H "Authorization: Bearer $TOKEN" -X DELETE http://127.0.0.1:8080/v1/clients/<client-id>/keys/<key-id>
```

A key reads `rg_<tag>_<16 characters>_<52 characters>`. The tag comes from the
application's name (`rg_cus_` for "Customer App"), so keys are easy to attribute. The
part up to the second underscore is the public prefix: it is what logs and the admin
API show. RateGuard stores only the SHA-256 digest of the full key
([ADR 0006](adr/0006-api-keys-sha256-two-active.md)). An application can hold two
active keys at once, so a third is refused with `409` until one is revoked.

Keys are presented to the decision endpoint as `Authorization: Bearer <key>` (see
below). A caller without a key is identified by its IP address. `X-Forwarded-For` is only
believed from the proxies listed in `RG_TRUSTED_PROXIES`.

## Policies, endpoint rules and plans

A policy is a named limit such as "100 requests per 60 seconds". An endpoint rule gives
part of an API its own limit inside a policy, so `/api/login` can be far stricter than
the rest:

```bash
# The Free plan, made the default for callers without a plan of their own.
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"name":"Free","capacity":100,"window_seconds":60,"default":true}' http://127.0.0.1:8080/v1/policies
# {"id":"<policy-id>","name":"Free","capacity":100,"window_seconds":60,"refill_rate":1.6666666666666667,"enabled":true,"default":true,...}

# 5 login attempts a minute on the Free plan.
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"method":"POST","path_pattern":"/api/login","capacity":5,"window_seconds":60}' http://127.0.0.1:8080/v1/policies/<policy-id>/rules

# Put an application on the plan ({"policy_id":null} takes it off again).
curl -s -H "Authorization: Bearer $TOKEN" -X PUT -H 'Content-Type: application/json' -d '{"policy_id":"<policy-id>"}' http://127.0.0.1:8080/v1/clients/<client-id>/policy

# Change the plan's limit; DELETE disables it.
curl -s -H "Authorization: Bearer $TOKEN" -X PATCH -H 'Content-Type: application/json' -d '{"capacity":200}' http://127.0.0.1:8080/v1/policies/<policy-id>
```

Which limit applies to a request ([ADR 0009](adr/0009-policy-resolution-and-default-policy.md)):

1. A caller with an API key uses its application's policy, if it has one and it is
   enabled.
2. Anyone else, including a caller identified by IP, uses the default policy, if there is
   one and it is enabled.
3. Otherwise the caller has no policy and will be refused: RateGuard never falls back to
   "unlimited".

Within that policy, the most specific matching endpoint rule beats the policy's own
limit ([ADR 0010](adr/0010-endpoint-rule-matching-and-precedence.md)). A pattern's
segments are literals, `*` for exactly one segment, or a final `**` for any number.
Patterns are compared from the left, and at the first difference a literal beats `*`,
which beats `**`. Between equally specific patterns, a rule naming the request's method
beats one for `*`. Request paths are cleaned first, so `/api/login/` cannot dodge a
rule on `/api/login`. A refill rate defaults to `capacity / window_seconds`; set
`refill_rate` on a policy to allow bursts above the sustained rate.

Every instance caches resolution data for 10 seconds, and every admin write publishes
the changed ID on the Redis channel `rg:policy:invalidate`, so each instance drops it at
once ([ADR 0005](adr/0005-policy-cache-ttl-pubsub-singleflight.md)). The
integration suite measures a plan change made on one instance becoming visible on
another in tens of milliseconds, and the 10-second TTL only matters if a message is lost.

## Checking a request

Your application calls `POST /v1/check` with the method and path of the request it
received, and forwards the caller's `Authorization` header if it sent one
([ADR 0015](adr/0015-decision-endpoint.md)):

```bash
curl -s -w ' %{http_code}\n' -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"method":"GET","path":"/api/products"}' http://127.0.0.1:8080/v1/check
# {"allowed":true,"limit":100,"remaining":99,"reset_after":1} 200
# ...and once the bucket is empty:
# {"allowed":false,"limit":100,"remaining":0,"reset_after":60,"retry_after":1} 429
```

The status is the answer: 200 to serve the request, 429 to refuse it. Durations are whole
seconds, rounded up. Both carry headers your service can copy onto its own response
unchanged ([ADR 0016](adr/0016-rate-limit-headers-and-peek.md)):

| Header | Value |
|---|---|
| `X-RateLimit-Limit` | `limit` |
| `X-RateLimit-Remaining` | `remaining` |
| `X-RateLimit-Reset` | `reset_after`: seconds until the bucket is full again, not a timestamp |
| `Retry-After` | On 429 only: `retry_after`, at least 1 |

`POST /v1/peek` takes the same body and reports the same fields without taking a token.
It always answers 200, with `allowed` saying what a check would do. A bad key is a 401, never a quiet fallback to IP, and a caller with
no applicable policy gets a 403. Send the path only: a query string or fragment is
refused, because `/api/login?x` would otherwise escape a rule on `/api/login`. Without a
key, the caller is identified by IP, so an application that wants per-IP limits appends
its caller's address to `X-Forwarded-For` and has its network listed in
`RG_TRUSTED_PROXIES`.

A decision reads no database. Policies and API keys come from in-process caches kept in
sync over Redis pub/sub ([ADR 0005](adr/0005-policy-cache-ttl-pubsub-singleflight.md),
[ADR 0012](adr/0012-api-key-cache-on-the-hot-path.md)), and a revoked key stops
working on every instance within milliseconds.

## Integrating from Go

[examples/go-middleware](../examples/go-middleware) is a reference `net/http` middleware,
in a module of its own with no dependencies beyond the standard library:

```go
rg, err := rateguard.New(os.Getenv("RATEGUARD_URL"))
if err != nil {
	log.Fatal(err)
}
handler := rg.Middleware(mux)
```

It forwards each request's method, decoded path, `Authorization` header and address,
keeps a pool of keep-alive connections to RateGuard, copies the rate-limit headers onto
your responses, and answers 429 itself when RateGuard refuses. If RateGuard cannot be
reached it answers 503, unless you choose `WithFailOpen()`. The
[sample app](../examples/go-middleware/sampleapp/main.go) is a complete service built this
way, and CI runs the middleware against the real stack.

## Sidecar deployment

Set `RG_UNIX_SOCKET` to an absolute path, and RateGuard also serves its whole API on that
Unix socket, alongside TCP ([ADR 0017](adr/0017-unix-socket-for-sidecars.md)). The
file is created with `RG_UNIX_SOCKET_MODE` (`0660` by default). A stale socket left by a
crash is replaced, but a live socket or any other file at that path stops startup
untouched, and the file is removed on shutdown. Point the middleware at it with
`rateguard.New("unix:///run/rateguard/rateguard.sock")`.

Over the socket, a caller without an API key is identified by the address the
application adds to `X-Forwarded-For`, because only processes the file's permissions let
in can connect. Keep the mode tight for that reason. The latency of both topologies is
under [Performance](performance.md).

## Shared buckets in Redis

Every instance draws from the same buckets. Refill and take run as one Lua script in
Redis, loaded with `EVALSHA`, so concurrent requests on any instance cannot overspend a
bucket. The script reads Redis's clock, so clock skew between instances cannot matter,
and it returns only relative values ([ADR 0002](adr/0002-redis-is-the-only-clock.md),
[ADR 0013](adr/0013-redis-bucket-store.md)). A bucket is a hash under
`rg:bucket:<scope>:<caller>`, where the scope is `all` or an endpoint rule's ID and the
caller is `client:<id>` or `ip:<address>`; IPv6 callers are keyed by their `/64`. Keys
expire when the bucket would be full again, so idle callers cost nothing. Every key is
built by one function, so sharding a hot key later is a one-line change
([ADR 0003](adr/0003-no-hot-key-sharding-in-v1.md)).

The integration suite runs three instances behind a round-robin load balancer: a client
limited to 100 gets exactly 100 of 300 concurrent requests through, although each
instance serves 100 of them. With per-instance memory instead, all 300 get through.

## When Redis fails

A circuit breaker wraps every bucket call, each bounded by `RG_REDIS_TIMEOUT`. After
`RG_REDIS_BREAKER_FAILURES` consecutive failures it opens, and decisions follow
`RG_REDIS_FAILURE_MODE` ([ADR 0001](adr/0001-redis-failure-degrades-to-local-bucket.md),
[ADR 0014](adr/0014-degraded-decisions.md)):

| Mode | While Redis is unreachable |
|---|---|
| `local` (default) | Each instance decides from its own bucket of `capacity / RG_INSTANCE_COUNT` (rounded down, at least 1), refilling at `rate / RG_INSTANCE_COUNT` |
| `open` | Every request is allowed |
| `closed` | Every request is refused, with `retry_after` set to the breaker's cooldown |

After `RG_REDIS_BREAKER_COOLDOWN` one request probes Redis; if it succeeds, the breaker
closes and Redis's buckets decide again from where they were left. Every degraded
decision increments `rateguard_degraded_decisions_total`, and
`rateguard_redis_breaker_state` reads 0, 1 or 2 for closed, half-open and open. The log
gets a WARN when the breaker opens, at most one WARN every 10 seconds while degraded
(with a count, rather than a line per decision), and an INFO when it closes. Set
`RG_INSTANCE_COUNT` to the number of instances you run, or the local buckets will be the
wrong size.

The integration suite kills Redis mid-test under three instances: every request is still
answered, each instance allows exactly its local share, all of it shows in `/metrics`,
and once Redis returns the breakers close and Redis's balance applies again.

## Usage analytics

`GET /v1/analytics/usage` reports how many checks were allowed and rejected
([ADR 0018](adr/0018-usage-analytics-pipeline.md)):

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://127.0.0.1:8080/v1/analytics/usage?from=2026-11-05T09:00:00Z&to=2026-11-05T10:00:00Z&granularity=minute&top=5'
```

```json
{
  "from": "2026-11-05T09:00:00Z", "to": "2026-11-05T10:00:00Z", "granularity": "minute",
  "totals":       {"requests": 1300, "allowed": 1200, "rejected": 100},
  "unidentified": {"requests": 40, "allowed": 40, "rejected": 0},
  "clients": [
    {"client_id": "...", "name": "Customer App", "requests": 900, "allowed": 820, "rejected": 80}
  ],
  "series": [
    {"start": "2026-11-05T09:00:00Z", "requests": 21, "allowed": 21, "rejected": 0}
  ]
}
```

| Parameter | Default | Meaning |
|---|---|---|
| `from`, `to` | the last 24 hours | RFC 3339 times; the range is widened to whole minutes or hours, in UTC, and the response says which range it answered |
| `granularity` | `hour` | `minute` or `hour`; a series holds at most 1,440 points (a day by the minute, 60 days by the hour) |
| `top` | `10` | How many clients to rank, from 1 to 100 |

`clients` ranks applications by rejections, then by requests, so the most throttled
come first. `unidentified` counts callers without an API key, who are known by IP;
their addresses are never stored. `series` has a point for every minute or hour,
zeros included. Peeks are not counted.

A decision only counts itself in memory. Every second each instance adds its counts to
one hash per minute in Redis, and every 10 seconds any instance drains those hashes
into the `usage_rollups` table. A drain reads and deletes in one Lua script, so no two
instances take the same counts and no leader is needed. Reports therefore lag by about
11 seconds. An instance that crashes loses up to a second of its counts, and every loss
the service can see increments `rateguard_usage_dropped_total`.

## Observability

**Metrics.** `GET /metrics` serves Prometheus metrics from a registry of the service's
own ([ADR 0014](adr/0014-degraded-decisions.md),
[ADR 0019](adr/0019-decision-and-cache-metrics.md)). No label carries a client, a
key or an address.

| Metric | Labels | What it shows |
|---|---|---|
| `rateguard_decisions_total` | `outcome`: `allowed`, `rejected`, `no_policy`, `error` | Checks by outcome |
| `rateguard_decision_duration_seconds` | `operation`: `check`, `peek` | Histogram of the server's time per decision request, from 100µs to 1s |
| `rateguard_cache_lookups_total` | `cache`: `api_key`, `client_policy`, `policy`, `default_policy`; `result`: `hit`, `miss` | Hit ratio of the caches that keep Postgres off the hot path |
| `rateguard_degraded_decisions_total` | `mode`: `local`, `open`, `closed` | Decisions made without Redis |
| `rateguard_redis_errors_total` | | Failed or timed-out bucket calls |
| `rateguard_redis_breaker_state` | | 0 closed, 1 half-open, 2 open |
| `rateguard_usage_dropped_total` | | Checks lost to analytics |

Plus the Go runtime and process collectors. For example:

```promql
# p99 decision latency per instance
histogram_quantile(0.99, sum by (instance, le) (rate(rateguard_decision_duration_seconds_bucket{operation="check"}[5m])))
# share of checks refused
sum(rate(rateguard_decisions_total{outcome="rejected"}[5m])) / sum(rate(rateguard_decisions_total[5m]))
# cache hit ratio, per cache
sum by (cache) (rate(rateguard_cache_lookups_total{result="hit"}[5m])) / sum by (cache) (rate(rateguard_cache_lookups_total[5m]))
```

**Logs.** JSON lines on stdout through `log/slog`. Every request gets an ID (reused from
a valid inbound `X-Request-ID`, otherwise generated), returned in `X-Request-ID` and
included in every log line about that request. Nothing is logged per decision
([ADR 0004](adr/0004-the-hot-path-is-sacred.md)): an outage shows as one WARN when
the breaker opens and at most one every 10 seconds while degraded, and a failed request
as one ERROR with its request ID. API keys appear only as their public prefix.

**Probes.** `/healthz` answers while the process is up; `/readyz` checks Postgres and
Redis and answers 503 naming whichever is unreachable.

## The token bucket

Each caller's bucket holds up to `capacity` tokens and refills continuously at
`refill_rate` tokens a second. A request takes one token, and an empty bucket refuses
it. Refill is computed lazily from the elapsed time whenever the bucket is used, so no
timers run. A burst can use the whole capacity at once, while the long-run rate stays at
the refill rate. [ADR 0011](adr/0011-token-bucket-semantics.md) pins down the
details so every store decides identically:

| Field | Meaning |
|---|---|
| `limit` | The bucket's capacity |
| `remaining` | Whole tokens left after this decision |
| `reset_after` | Time until the bucket is full again if nothing else uses it |
| `retry_after` | On a refusal only: time until one token is back. Retrying then is allowed |

The engine (`internal/limiter/engine.go`) is a pure function of a bucket's state, its
limit and the time it is given. It never reads a clock itself. The Lua script ports it
step by step on Redis's clock, and the integration suite replays real script calls
through the engine and expects identical results. The in-memory store applies the engine
under a mutex; it is the fallback when Redis is down, and it deletes buckets that have
refilled to capacity, since they decide exactly as new ones do.

The tests check, without any network:

- bursts and steady drain: exactly `capacity + rate × elapsed` requests get through,
  never one more;
- refill boundaries at and around the nanosecond a token is due;
- a clock that stands still or jumps back;
- capacity raised and lowered under a client;
- degenerate limits;
- a fuzz test of the invariants;
- concurrent consumption under `-race`: 3,200 racing takes on a bucket of 1,000 admit
  exactly 1,000.

