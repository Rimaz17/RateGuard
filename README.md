# RateGuard

A standalone, distributed API rate-limiting microservice in Go.

Applications ask RateGuard over HTTP whether a request should be allowed. RateGuard
identifies the caller, resolves its policy, and atomically takes a token from a token
bucket that every RateGuard instance shares through Redis. Rate limiting lives in one
service instead of in every application, and any language that can make an HTTP call
can use it.

> **Status: v1 complete (phase 7 of 7).** Admin API for applications, API keys, policies
> and endpoint rules; `POST /v1/check` with the standard `X-RateLimit-*` and
> `Retry-After` headers; a circuit breaker that degrades to a local bucket, never to
> unlimited, when Redis fails; usage analytics, Prometheus metrics, and
> [measured performance](#performance).

## Architecture

![RateGuard architecture: callers, API and security, the hot path, resilience, data and storage, analytics, observability, and CI and infrastructure](docs/images/rateguard-architecture.png)

Your service returns `429 Too Many Requests` to its own client when RateGuard says no.

- **Hot path** (`/v1/check`): the caller's key and policy come from in-process caches,
  then one atomic Lua script runs in Redis. No Postgres I/O, no synchronous logging, no
  analytics write ([ADR 0004](docs/adr/0004-the-hot-path-is-sacred.md)).
- **Off the hot path**: usage counts move into Redis every second and into Postgres every
  10 seconds ([ADR 0018](docs/adr/0018-usage-analytics-pipeline.md)).
- **Redis outage**: decisions fall back to a local bucket per instance
  ([ADR 0001](docs/adr/0001-redis-failure-degrades-to-local-bucket.md)).
- **Postgres outage**: decisions carry on from the last cached keys and plans for up to
  15 minutes ([ADR 0021](docs/adr/0021-serve-stale-cache-entries-while-postgres-is-down.md)).
- **Deployment**: one central service, or a sidecar next to each application on a Unix
  socket ([ADR 0017](docs/adr/0017-unix-socket-for-sidecars.md)).

Every design decision is recorded in [docs/adr](docs/adr/README.md).

## Quickstart

Requirements: Docker with Compose v2, `make` and `curl`. Go 1.27.3 is needed only to
build or test outside Docker.

```bash
git clone https://github.com/Rimaz17/RateGuard.git
cd RateGuard
make up      # creates .env, builds, applies migrations, starts, waits for health checks
make smoke   # GET /healthz and GET /readyz, expects 200 from both
```

Then make a plan, register an application and decide a few requests (`TOKEN` is the
placeholder admin token from `configs/.env.example`):

```bash
TOKEN=change-me-local-only-admin-token-0000
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Tour","capacity":3,"window_seconds":60,"default":true}' http://127.0.0.1:8080/v1/policies
KEY=$(curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Tour App"}' http://127.0.0.1:8080/v1/clients | sed -n 's/.*"key":"\([^"]*\)".*/\1/p')
for i in 1 2 3 4; do
  curl -s -w ' %{http_code}\n' -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d '{"method":"GET","path":"/api/products"}' http://127.0.0.1:8080/v1/check
done
# three 200s, then {"allowed":false,...,"retry_after":20} 429
sleep 12   # usage reaches Postgres within about 11 seconds
curl -s -H "Authorization: Bearer $TOKEN" 'http://127.0.0.1:8080/v1/analytics/usage?top=3'
# {"from":...,"totals":{"requests":4,"allowed":3,"rejected":1},...,"clients":[{"name":"Tour App",...}],...}
make down
```

The new application has no plan of its own, so it uses the default policy: 3 requests,
refilling one every 20 seconds.

RateGuard listens on `127.0.0.1:8080`, Postgres on `127.0.0.1:5433` and Redis on
`127.0.0.1:6379`; change the host ports in `.env`. `make demo` runs three instances
behind nginx to show one shared limit and a Redis outage
([scripts/demo-distributed.sh](scripts/demo-distributed.sh)); `make demo-down` stops it.

Without `make` (on Windows, for example):

```bash
cp configs/.env.example .env
docker compose up -d --build --wait
curl -i http://127.0.0.1:8080/readyz
```

**Next:** the [guide](docs/guide.md) covers key rotation, plans and endpoint rules, the
decision contract, the Go middleware, sidecars, analytics and observability.

**Testing guide:** [docs/RateGuard-Testing-Guide.pdf](docs/RateGuard-Testing-Guide.pdf)
is a step-by-step guide to testing every feature on Windows with Postman: setup for a
first-time Go user, then each request to send and the result to expect, the Redis and
Postgres failure tests, and the automated test commands. Its source is
[docs/testing-guide.html](docs/testing-guide.html).

## Configuration

RateGuard reads `RG_`-prefixed environment variables. A variable that is set but empty
takes its default. [configs/.env.example](configs/.env.example) documents every variable, including the
ones that only Docker Compose reads.

| Variable | Default | Meaning |
|---|---|---|
| `RG_HTTP_ADDR` | `:8080` | Address the HTTP server listens on |
| `RG_HTTP_READ_HEADER_TIMEOUT` | `5s` | Maximum time to read request headers |
| `RG_HTTP_READ_TIMEOUT` | `10s` | Maximum time to read the whole request |
| `RG_HTTP_WRITE_TIMEOUT` | `10s` | Maximum time to write the response |
| `RG_HTTP_IDLE_TIMEOUT` | `120s` | Keep-alive idle timeout |
| `RG_SHUTDOWN_TIMEOUT` | `15s` | Grace period for in-flight requests on SIGINT/SIGTERM |
| `RG_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `RG_DATABASE_URL` | *(required)* | PostgreSQL connection string; treated as a secret and never logged |
| `RG_DATABASE_MAX_CONNS` | `10` | Connection pool size (at least 1) |
| `RG_DATABASE_MAX_CONN_LIFETIME` | `30m` | Recycle pooled connections after this long |
| `RG_DATABASE_MAX_CONN_IDLE_TIME` | `5m` | Close pooled connections idle for this long |
| `RG_REDIS_URL` | `redis://localhost:6379/0` | Redis URL; treated as a secret and never logged |
| `RG_REDIS_TIMEOUT` | `100ms` | Longest a bucket operation in Redis may take before it counts as a failure |
| `RG_REDIS_FAILURE_MODE` | `local` | While Redis is unreachable: `local` (in-memory bucket per instance), `open` (allow all) or `closed` (refuse all) ([ADR 0014](docs/adr/0014-degraded-decisions.md)) |
| `RG_REDIS_BREAKER_FAILURES` | `5` | Consecutive failed bucket operations that open the circuit breaker (at least 1) |
| `RG_REDIS_BREAKER_COOLDOWN` | `5s` | How long the breaker stays open before one request probes Redis again |
| `RG_UNIX_SOCKET` | *(empty)* | Absolute path of a Unix socket to also serve on, for sidecars; empty means TCP only ([ADR 0017](docs/adr/0017-unix-socket-for-sidecars.md)) |
| `RG_UNIX_SOCKET_MODE` | `0660` | Octal permission of the socket file; its peers are trusted to set `X-Forwarded-For` |
| `RG_INSTANCE_COUNT` | `1` | Instances sharing the traffic; local buckets hold `capacity / RG_INSTANCE_COUNT` while Redis is down |
| `RG_USAGE_FLUSH_INTERVAL` | `1s` | How often each instance adds its usage counts into Redis ([ADR 0018](docs/adr/0018-usage-analytics-pipeline.md)) |
| `RG_USAGE_ROLLUP_INTERVAL` | `10s` | How often usage counts move from Redis into Postgres; analytics lag by about both intervals |
| `RG_READINESS_TIMEOUT` | `2s` | Per-dependency timeout for the `/readyz` checks |
| `RG_TRUSTED_PROXIES` | *(empty)* | Comma-separated CIDRs whose `X-Forwarded-For` is believed for IP identification; empty trusts none |
| `RG_ADMIN_TOKEN` | *(required)* | Bearer token for the admin API, at least 32 characters ([ADR 0008](docs/adr/0008-admin-api-bearer-token.md)) |

Every timeout must be positive. RateGuard exits at startup if any configuration value is
invalid.

## API

| Method and path | Purpose | Status |
|---|---|---|
| `GET /healthz` | Liveness: the process is up and serving | Available |
| `GET /readyz` | Readiness: Postgres and Redis are reachable (200, or 503 naming the one that is not) | Available |
| `POST /v1/clients` | Register an application; returns its first API key, once | Available |
| `GET /v1/clients`, `GET /v1/clients/{id}` | List (paged) and read applications | Available |
| `GET /v1/clients/{id}/keys` | List an application's keys, by prefix only | Available |
| `POST /v1/clients/{id}/keys`, `DELETE /v1/clients/{id}/keys/{keyID}` | Issue a second key for rotation; revoke a key | Available |
| `POST /v1/policies`, `GET /v1/policies`, `GET/PATCH/DELETE /v1/policies/{id}` | Create, list, read, update and disable policies | Available |
| `GET/POST /v1/policies/{id}/rules`, `DELETE /v1/policies/{id}/rules/{ruleID}` | A policy's endpoint rules | Available |
| `PUT /v1/clients/{id}/policy` | Assign a policy to an application, or remove it with `null` | Available |
| `POST /v1/check` | Rate-limit decision: takes a token, answers 200 or 429 ([ADR 0015](docs/adr/0015-decision-endpoint.md)) | Available |
| `GET /metrics` | Prometheus scrape: decisions, latency, cache hit ratio, Redis and breaker health, usage losses | Available |
| `POST /v1/peek` | Report the allowance without taking a token ([ADR 0016](docs/adr/0016-rate-limit-headers-and-peek.md)) | Available |
| `GET /openapi.yaml` | The OpenAPI document this binary was built with | Available |
| `GET /v1/analytics/usage` | Usage: totals, the most throttled clients, and volume by the minute or hour ([ADR 0018](docs/adr/0018-usage-analytics-pipeline.md)) | Available |

The `/v1/clients`, `/v1/policies` and `/v1/analytics` routes require
`Authorization: Bearer <RG_ADMIN_TOKEN>` ([ADR 0008](docs/adr/0008-admin-api-bearer-token.md)).
Every error is `{"error": "...", "request_id": "..."}` and every response carries
`X-Request-ID`. The contract is [api/openapi.yaml](api/openapi.yaml) (OpenAPI 3.1);
`make docs` serves it in Swagger UI on `127.0.0.1:8081`.

## Data model

PostgreSQL holds the configuration (policies and their endpoint rules, clients and their
API keys) and per-minute usage. Redis holds only ephemeral state: buckets, usage counts
not yet rolled up, and the invalidation channel. The schema lives in
[`migrations/`](migrations) as reversible, numbered pairs.

| Store | Holds |
|---|---|
| `policies` table | Named rate limits: capacity, window, refill rate, enabled flag, and which one is the default |
| `endpoint_rules` table | A policy's own limits for particular methods and path patterns |
| `clients` table | Registered applications, their status and assigned policy |
| `api_keys` table | Each client's keys: public prefix, SHA-256 digest, revocation time |
| `usage_rollups` table | Checks allowed and rejected per client (or for all callers known by IP) per minute |
| Redis `rg:bucket:*` | One hash per active bucket: `tokens` and `last_refill_us`, expiring when full |
| Redis `rg:usage:*` | Usage counts on their way from the instances to `usage_rollups` |
| Redis `rg:policy:invalidate` | Pub/sub channel carrying `client:<id>` and `policy:<id>` changes |

Column-level detail and the conventions behind it are in
[docs/data-model.md](docs/data-model.md).

## Development

```bash
make help             # list every target
make check            # gofmt check, go vet, golangci-lint, go test -race: run before committing
make test             # unit tests with the race detector
make test-integration # testcontainers suite against real Postgres and Redis (needs Docker)
make cover            # race-tested unit tests with coverage.out; make cover-engine then checks the engine's 90% floor
make lint-docker      # the pinned golangci-lint in Docker, if it is not installed locally
make run              # run on the host with the current environment
make load             # k6 and socket benchmarks against the compose stack (about 25 minutes; see Performance)
```

Migrations, running the race detector without a C toolchain, what CI checks, and the
repository layout are in [docs/development.md](docs/development.md).

## Performance

Measured with `make load` on one laptop (Intel Core i5-10210U, 4 cores, Docker Desktop
on WSL2) at commit `312c006`, with k6, RateGuard, Redis and Postgres all on the same
machine. Every request was an allowed take that wrote its bucket in Redis. The setup,
caveats and full analysis are in [docs/performance.md](docs/performance.md) and
[ADR 0020](docs/adr/0020-how-latency-and-the-key-ceiling-are-measured.md).

Latency, one instance, 100 API keys, requests at a fixed rate for 60 seconds:

| Topology | Client | p50 at 250/s | p99 at 250/s | p50 at 1,000/s | p99 at 1,000/s |
|---|---|---|---|---|---|
| Central, over the Docker network | k6 | 1.44 ms | 26.4 ms | 0.89 ms | 41.8 ms |
| Sidecar, loopback TCP | k6 | 1.40 ms | 23.9 ms | 1.59 ms | 89.9 ms |
| Sidecar, Unix socket | Go ([socketbench](test/load/socketbench/main.go)) | 1.08 ms | 15.4 ms | 1.11 ms | 92.3 ms |
| Sidecar, loopback TCP | Go ([socketbench](test/load/socketbench/main.go)) | 0.86 ms | 9.1 ms | 1.20 ms | 80.1 ms |

Throughput, 100 virtual users sending back to back for 30 seconds:

| Run | Requests a second | p50 | p99 |
|---|---|---|---|
| One instance, one hot key | 4,921 | 14.5 ms | 93.7 ms |
| One instance, 100 keys | 4,757 | 15.2 ms | 99.0 ms |
| Three instances behind nginx, one hot key | 2,503 | 28.5 ms | 170.0 ms |
| Three instances behind nginx, 100 keys | 2,386 | 30.1 ms | 179.2 ms |
| Redis alone: the bucket script on one key (`redis-benchmark`, 50 connections) | 19,680 | | |
| Redis alone: the bucket script on random keys | 20,740 | | |

- **A decision costs about 1 ms at the median** in every topology. The p99 ranged from
  9 to 92 ms on this laptop, mostly because the load generator and the service share
  its CPUs, so the overview's single-digit-millisecond budget holds at the median, not
  at p99.
- **On one machine the topologies cannot be told apart.** What a sidecar saves is the
  network round trip between hosts, which one machine cannot show.
- **The per-key ceiling is about 19,700 decisions a second here**, the rate at which
  Redis runs the bucket script on one key. With one Redis it equals the ceiling for
  every key together; end to end, a hot key was never the limit
  ([ADR 0003](docs/adr/0003-no-hot-key-sharding-in-v1.md)).
