# RateGuard — Progress

**Current phase:** Phase 1 — Persistence (not started)
**Last updated:** 2026-11-04

## Phase status

| Phase | Name | Status | Notes |
|---|---|---|---|
| 0 | Foundations | Done | PR #1 merged as `fe2f915`. CI green on the PR and on `main` |
| 1 | Persistence | Not started | Next up |
| 2 | Clients & API keys | Not started | |
| 3 | Policies | Not started | |
| 4 | Token bucket engine (in-memory) | Not started | |
| 5 | Distribution via Redis | Not started | |
| 6 | Response contract & integration | Not started | |
| 7 | Analytics, observability & polish | Not started | |

## Now

Nothing is in flight. `main` is at `fe2f915` (or at this docs PR's merge, once it
merges) and the working tree is clean.

**Next step:** start phase 1 on `feat/phase-1-persistence`, branched from an up-to-date
`main`:

1. Re-read the phase 1 section of the project overview, plus § 6 (data model).
2. Outline the plan and the files involved for the user before writing code.
   - Phase 1 work: golang-migrate wiring, the initial schema for clients and policies, a
     pgx connection pool, the repository layer, and `/readyz` checking Postgres and
     Redis.
   - Done when: migrations run cleanly up and down, and readiness flips when Postgres is
     stopped.
3. Add the two CI checks deferred from phase 0: testcontainers integration tests, and
   migrations up/down against a fresh database (CLAUDE.md § 16, items 6 and 8).

## What exists today (end of phase 0)

- `cmd/rateguard/main.go`: the composition root. It loads config, sets up the JSON slog
  logger and an `http.Server` with every timeout set, and shuts down gracefully on
  SIGINT/SIGTERM.
- `internal/config`: `RG_` environment variables via `caarlos0/env`, with validation. A
  variable that is set but empty takes its default.
- `internal/transport/http` (package `httptransport`): the chi router, `GET /healthz`,
  and the `RequestID` and `Recoverer` middleware.
- `Dockerfile` (distroless, nonroot), and `docker-compose.yml` with Postgres 18.6 and
  Redis 8.10.3, configured from `.env`.
- `Makefile`, `.golangci.yml` (v2), and `.github/workflows/ci.yml` with three jobs: lint,
  test, and container (image build plus `make up` and `make smoke`).
- `docs/adr/0001`–`0006` (PDF § 8), `README.md`, and this file.
- No `internal/domain`, no migrations, no `api/openapi.yaml` yet. They arrive with the
  phases that need them.

## Local environment notes (dev machine: Windows 11)

- Go 1.27.3 and Docker 29 are installed, along with `gh`, which is logged in as
  `Rimaz17`.
- **`make` is not installed.** Run make targets in a container:
  `docker run --rm -v //var/run/docker.sock:/var/run/docker.sock -v "<repo>:/src" -w /src docker:29-cli sh -c 'apk add -q make curl && make up'`.
  Use `MSYS_NO_PATHCONV=1` in Git Bash.
- **`golangci-lint` is not installed.** Use `make lint-docker`, or the
  `golangci/golangci-lint:v2.15.0` image.
- **No C compiler, so `-race` will not run natively.** Run tests in `golang:1.27.3`:
  `docker run --rm -v "<repo>:/src" -w /src golang:1.27.3 go test -race ./...`.
- Port 5432 is taken by a local Postgres install. Compose publishes Postgres on 5433.

## Done this session (2026-11-04)

- Built phase 0 in 16 commits on `chore/phase-0-foundations`, from `1c43023` (gitignore)
  to `cac4aa1` (progress log).
- Bootstrapped `main` with an empty root commit (`0af1be3`) and opened
  [PR #1](https://github.com/Rimaz17/RateGuard/pull/1).
- CI passed on PR #1, all three jobs, run for both the push and the pull_request event.
  golangci-lint reported 0 issues, `go 1.27.3 everywhere` held, `make up` and
  `make smoke` passed, and `go test -race` passed.
- PR #1 merged into `main` as `fe2f915`, and CI on `main` passed afterwards.
- CLAUDE.md § 13 now requires reading this file at the start of every session and
  updating it at the end of every session.

## Open questions / blockers

- **Repo merge setting:** PR #1 was merged with a merge commit, not squashed, although
  CLAUDE.md § 12 asks for squash merges. Nothing needs undoing, but enabling only
  "Allow squash merging" in GitHub settings would enforce it. That is the user's call.
- **Local toolchain gaps:** `make`, `golangci-lint` and a C compiler are missing natively
  (see the environment notes above). Installing them is the user's call.
- **Stray files in the workspace root, outside the repo:** a `PROGRESS.md` from an
  unrelated project ("Clientele"), and a `.claude/launch.json` pointing at that project's
  React app. They were left untouched. Delete them or keep them? Until then, ignore the
  root `PROGRESS.md`; this file is the real one.
- GitHub Actions are pinned to major tags (`@v7`), not commit SHAs. Should they move to
  SHA pinning?

## Decisions log

- 2026-11-04: `main` was bootstrapped with an empty root commit (`0af1be3`), and phase 0
  arrived through PR #1 like every later phase. This was the owner's choice over pushing
  the branch directly as `main`.
- 2026-11-04: The package in `internal/transport/http` is named `httptransport` so it
  never shadows `net/http`.
- 2026-11-04: `go.mod` is the single source of the Go version (1.27.3). CI reads it with
  `go-version-file`, and `make check-go-version` fails CI if the Dockerfile's
  `GO_VERSION` differs.
- 2026-11-04: The runtime image is `distroless/static-debian13:nonroot`. As a result,
  the RateGuard container has no Docker HEALTHCHECK. `make smoke` and CI check
  `/healthz` from outside instead.
- 2026-11-04: A config variable that is set but empty takes its default (the library's
  behaviour, pinned by a test), so compose can pass `${VAR:-}` placeholders.
- 2026-11-04: Compose publishes Postgres on host port 5433, and every dependency port
  binds to 127.0.0.1.
- 2026-11-04: The request ID and panic recovery middleware landed in phase 0, because
  CLAUDE.md §§ 10–11 make them standing requirements. There is no per-request access
  logging (ADR 0004).
