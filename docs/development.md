# Development

The everyday `make` targets are in the [README](../README.md#development). This page
covers migrations, running the tests without a local toolchain, what CI checks, and the
repository layout.

## Migrations

The schema lives in [`migrations/`](../migrations) as numbered `.up.sql` / `.down.sql`
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

CI ([.github/workflows/ci.yml](../.github/workflows/ci.yml)) runs on every push and pull
request: format, vet, lint and race-tested unit tests, with the token bucket engine held
to at least 90% statement coverage; the integration suite, which
includes a migrations up/down/up round trip on a fresh database; and an image build
followed by `make up`, `make smoke`, checks of the admin API (clients, then a policy
with a rule assigned to a client, and the instance's subscription to policy changes), a
decision through `/v1/check` with its bucket in Redis and `/metrics`, the rate-limit
headers, a peek, `/openapi.yaml` and a JSON 404, the Go middleware's end-to-end test, a
usage report that reaches Postgres through Redis, the phase 7 metrics, a smoke run of
the k6 scripts, a migrate down and up through the binary, and a check that `/readyz`
turns 503 when Postgres stops and 200 when it returns; the middleware module's own
format, vet, lint and race tests; and `make demo`, the three-instance demo described in
the [README](../README.md#quickstart).

## Layout

```
cmd/rateguard/            composition root: wiring and the migrate subcommand
internal/config/          environment configuration and validation
internal/domain/          entities and sentinel errors; standard library only
internal/client/          client registration, API key issue, rotation and authentication
internal/policy/          policy and rule management, resolution, its cache and pub/sub sync
internal/cache/           the TTL + singleflight cache behind policies and API keys
internal/analytics/       usage recorder, Redis flusher, rollup to Postgres, and reports
internal/analytics/scripts the add and drain Lua scripts for usage counts
internal/limiter/         token bucket engine, Redis and in-memory stores, circuit breaker, keys
internal/limiter/scripts  the refill-and-consume Lua script
internal/storage/postgres pgx pool, repositories and the migrator
internal/transport/http   chi router, handlers and middleware (package httptransport)
migrations/               numbered SQL migrations, embedded in the binary
api/                      the HTTP contract (openapi.yaml), embedded and served by the binary
examples/go-middleware/   reference Go middleware and sample app (a module of its own)
scripts/                  the distributed demo, its nginx config, and the load test runner
test/integration/         testcontainers suite (build tag: integration)
test/load/                k6 scripts and the Unix-socket benchmark (socketbench)
docs/                     data model and architecture decision records
```

