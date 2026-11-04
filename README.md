# RateGuard

A standalone, distributed API rate-limiting microservice in Go.

Applications ask RateGuard over HTTP whether a request should be allowed. RateGuard
identifies the caller, resolves its policy, and atomically consumes a token from a token
bucket shared across every RateGuard instance through Redis. Rate limiting lives in one
service instead of being re-implemented in every application, and any language that can
make an HTTP call can use it.

> **Status: phase 2 (clients and API keys).** The service runs in Docker Compose next to
> Postgres and Redis, applies its schema migrations, serves `GET /healthz` and
> `GET /readyz`, and has an admin API to register applications and issue, rotate and
> revoke their API keys. Rate limiting itself is not implemented yet.

## Architecture

```
 client ──▶ your service ──POST /v1/check──▶ RateGuard (× N instances)
                 ▲                              │  in-process policy cache
                 └──── allowed / remaining ─────┤
                                                ├──▶ Redis       bucket state (atomic Lua script),
                                                │                policy invalidation pub/sub
                                                └──▶ PostgreSQL  clients, API keys, policies,
                                                                 usage rollups (admin path only)
```

Your service returns `429 Too Many Requests` to its own client when RateGuard says no.

- **Hot path** (`/v1/check`): in-process policy cache, then one atomic Lua script in
  Redis. No Postgres I/O and no synchronous logging or analytics
  ([ADR 0004](docs/adr/0004-the-hot-path-is-sacred.md)).
- **Admin path** (`/v1/clients`, `/v1/policies`, `/v1/analytics`): Postgres-backed CRUD.
- **Redis outage**: a circuit breaker falls back to a local bucket, never to unlimited
  ([ADR 0001](docs/adr/0001-redis-failure-degrades-to-local-bucket.md)).

The six binding design decisions are recorded in [docs/adr](docs/adr/README.md).

## Quickstart

Requirements: Docker with Compose v2, `make` and `curl`. Go 1.27.3 is needed only to
build or test outside Docker.

```bash
git clone https://github.com/Rimaz17/RateGuard.git
cd RateGuard
make up      # creates .env, builds, applies migrations, starts, waits for health checks
make smoke   # GET /healthz and GET /readyz, expects 200 from both
make down
```

If your `.env` predates phase 2, add `RG_ADMIN_TOKEN` to it (see
[configs/.env.example](configs/.env.example)); Compose refuses to start without it.

`make up` binds RateGuard to `127.0.0.1:8080`, Postgres to `127.0.0.1:5433` and Redis to
`127.0.0.1:6379`. To change the host ports, edit `.env`. A one-shot `migrate` service
applies the schema before RateGuard starts
([ADR 0007](docs/adr/0007-migrations-run-as-a-separate-step.md)).

Without `make` (on Windows, for example), run the equivalent commands directly:

```bash
cp configs/.env.example .env
docker compose up -d --build --wait
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

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
| `POST/PATCH/DELETE /v1/policies`, `PUT /v1/clients/{id}/policy` | Policy management | Phase 3 |
| `POST /v1/check`, `POST /v1/peek` | Rate-limit decision | Phases 4–6 |
| `GET /v1/analytics/usage`, `GET /metrics` | Usage analytics and Prometheus metrics | Phase 7 |

The `/v1/clients` routes require `Authorization: Bearer <RG_ADMIN_TOKEN>`
([ADR 0008](docs/adr/0008-admin-api-bearer-token.md)). Every error response is
`{"error": "...", "request_id": "..."}`, and never carries internal detail.

Every response carries an `X-Request-ID` header. An inbound `X-Request-ID` is reused if
it is at most 64 characters of `[A-Za-z0-9._-]`; otherwise RateGuard generates a new one.
The contract is [api/openapi.yaml](api/openapi.yaml) (OpenAPI 3.1), updated in the same
commit as any handler.

### Registering an application and rotating its key

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
([ADR 0006](docs/adr/0006-api-keys-sha256-two-active.md)). An application can hold two
active keys at once, so a third is refused with `409` until one is revoked.

Callers of the decision endpoints (from phase 4) will send their key as
`Authorization: Bearer <key>`. A caller without a key is identified by its IP address.
`X-Forwarded-For` is only believed from the proxies listed in `RG_TRUSTED_PROXIES`.

## Data model

PostgreSQL holds the configuration: policies and their endpoint rules, clients and their API keys. Redis holds only ephemeral bucket state. The schema lives in
[`migrations/`](migrations) as reversible, numbered pairs.

| Table | Holds |
|---|---|
| `policies` | Named rate limits: capacity, window, refill rate, enabled flag |
| `endpoint_rules` | A policy's own limits for particular methods and path patterns |
| `clients` | Registered applications, their status and assigned policy |
| `api_keys` | Each client's keys: public prefix, SHA-256 digest, revocation time |

Column-level detail and the conventions behind it are in
[docs/data-model.md](docs/data-model.md).

## Development

```bash
make help             # list every target
make check            # gofmt check, go vet, golangci-lint, go test -race: run before committing
make test             # unit tests with the race detector
make test-integration # testcontainers suite against real Postgres and Redis (needs Docker)
make lint-docker      # the pinned golangci-lint in Docker, if it is not installed locally
make run              # run on the host with the current environment
```

### Migrations

The schema lives in [`migrations/`](migrations) as numbered `.up.sql` / `.down.sql`
pairs, embedded in the binary. Against the compose database:

```bash
make migrate-version     # current schema version
make migrate-down        # revert the last migration (N=3 for three)
make migrate-up          # apply everything pending
```

Outside Compose, run the binary directly: `rateguard migrate up | down [N] | version`,
with `RG_DATABASE_URL` set. An applied migration is never edited; changes go in a new
pair.

The race detector needs cgo. On a machine without a C toolchain, run the tests in the Go
image:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.27.3 go test -race ./...
```

The integration suite starts its own containers with testcontainers-go, so it needs a
reachable Docker daemon. When running it inside a container on Docker Desktop, mount the
Docker socket and set `TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal`.

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs on every push and pull
request: format, vet, lint and race-tested unit tests; the integration suite, which
includes a migrations up/down/up round trip on a fresh database; and an image build
followed by `make up`, `make smoke`, a migrate down and up through the binary, and a
check that `/readyz` turns 503 when Postgres stops and 200 when it returns.

### Layout

```
cmd/rateguard/            composition root: wiring and the migrate subcommand
internal/config/          environment configuration and validation
internal/domain/          entities and sentinel errors; standard library only
internal/client/          client registration, API key issue, rotation and authentication
internal/storage/postgres pgx pool, repositories and the migrator
internal/transport/http   chi router, handlers and middleware (package httptransport)
migrations/               numbered SQL migrations, embedded in the binary
api/openapi.yaml          the HTTP contract
test/integration/         testcontainers suite (build tag: integration)
docs/                     data model and architecture decision records
```

Later phases add `internal/limiter`, `policy` and `analytics`.

## Performance

Measured p50/p99 latency for central and sidecar deployment, and the single-key
throughput ceiling ([ADR 0003](docs/adr/0003-no-hot-key-sharding-in-v1.md)), will be
published here in phase 7. Nothing has been measured yet.
