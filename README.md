# RateGuard

A standalone, distributed API rate-limiting microservice in Go.

Applications ask RateGuard over HTTP whether a request should be allowed. RateGuard
identifies the caller, resolves its policy, and atomically consumes a token from a token
bucket shared across every RateGuard instance through Redis. Rate limiting lives in one
service instead of being re-implemented in every application, and any language that can
make an HTTP call can use it.

> **Status: phase 0 (foundations).** The service builds, runs in Docker Compose next to
> Postgres and Redis, and serves `GET /healthz`. Rate limiting itself is not implemented
> yet. [PROGRESS.md](PROGRESS.md) tracks the build phase by phase.

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
make up      # creates .env from configs/.env.example, builds, starts, waits for health checks
make smoke   # GET /healthz, expects 200 {"status":"ok"}
make down
```

`make up` binds RateGuard to `127.0.0.1:8080`, Postgres to `127.0.0.1:5433` and Redis to
`127.0.0.1:6379`. To change the host ports, edit `.env`.

Without `make` (on Windows, for example), run the equivalent commands directly:

```bash
cp configs/.env.example .env
docker compose up -d --build --wait
curl -i http://127.0.0.1:8080/healthz
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

Every timeout must be positive. RateGuard exits at startup if any configuration value is
invalid.

## API

| Method and path | Purpose | Status |
|---|---|---|
| `GET /healthz` | Liveness: the process is up and serving | Available |
| `GET /readyz` | Readiness: Postgres and Redis are reachable | Phase 1 |
| `POST /v1/clients`, `GET /v1/clients`, `POST/DELETE /v1/clients/{id}/keys` | Client and API key management | Phase 2 |
| `POST/PATCH/DELETE /v1/policies`, `PUT /v1/clients/{id}/policy` | Policy management | Phase 3 |
| `POST /v1/check`, `POST /v1/peek` | Rate-limit decision | Phases 4–6 |
| `GET /v1/analytics/usage`, `GET /metrics` | Usage analytics and Prometheus metrics | Phase 7 |

Every response carries an `X-Request-ID` header. An inbound `X-Request-ID` is reused if
it is at most 64 characters of `[A-Za-z0-9._-]`; otherwise RateGuard generates a new one.
An OpenAPI contract arrives in `api/openapi.yaml` with the first handler that needs one.

## Development

```bash
make help        # list every target
make check       # gofmt check, go vet, golangci-lint, go test -race: run before committing
make test        # unit tests with the race detector
make lint-docker # the pinned golangci-lint in Docker, if it is not installed locally
make run         # run on the host with the current environment
```

The race detector needs cgo. On a machine without a C toolchain, run the tests in the Go
image:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.27.3 go test -race ./...
```

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs the format, vet, lint and
race-test checks, builds the image, and runs `make up` with `make smoke` on every push
and pull request.

### Layout

```
cmd/rateguard/          composition root: wiring only
internal/config/        environment configuration and validation
internal/transport/http chi router, handlers and middleware (package httptransport)
docs/adr/               architecture decision records
```

Later phases add `internal/domain`, `limiter`, `policy`, `client`, `analytics` and
`storage/postgres`, plus `migrations/` and `api/openapi.yaml`.

## Performance

Measured p50/p99 latency for central and sidecar deployment, and the single-key
throughput ceiling ([ADR 0003](docs/adr/0003-no-hot-key-sharding-in-v1.md)), will be
published here in phase 7. Nothing has been measured yet.
