# RateGuard — Progress

**Current phase:** Phase 0 — Foundations (verified locally; CI not yet run)
**Last updated:** 2026-11-04

## Phase status

| Phase | Name | Status | Notes |
|---|---|---|---|
| 0 | Foundations | Verified locally | Done-when met on a fresh clone. CI has never run because the branch is not pushed yet |
| 1 | Persistence | Not started | |
| 2 | Clients & API keys | Not started | |
| 3 | Policies | Not started | |
| 4 | Token bucket engine (in-memory) | Not started | |
| 5 | Distribution via Redis | Not started | |
| 6 | Response contract & integration | Not started | |
| 7 | Analytics, observability & polish | Not started | |

## Now

Phase 0 is in review on branch `chore/phase-0-foundations`, opened as a PR into `main`.
Next steps:

1. Get CI green on the PR, then squash-merge it.
2. Start phase 1 (persistence) on `feat/phase-1-persistence`.

### How phase 0 was verified (2026-11-04)

- **Done when "`make up` brings up the stack and `/healthz` returns 200": met.** The repo
  was cloned fresh into a temp directory, then `make up` and `make smoke` were run. make
  ran in a `docker:29-cli` container against the host Docker daemon because make is not
  installed on this Windows machine. All three services reported healthy, and
  `GET /healthz` returned `200 {"status":"ok"}` both from inside that container and from
  the host.
- `gofmt -l .` was empty, and `go vet ./...` and `go test -race ./...` were clean in the
  `golang:1.27.3` container. Coverage is 100% on `internal/config` and
  `internal/transport/http`. `cmd/rateguard` has no tests.
- `golangci-lint run` (v2.15.0, in its official image) reported 0 issues.
- `actionlint` on `ci.yml` was clean.
- `docker stop` (SIGTERM) drained and exited 0 with the "shutting down" and "stopped" log
  lines, and an invalid config exited 1 with a structured error.
- **Not verified:** the GitHub Actions workflow itself, which has never run.

## Done this session

These are the branch commits. Squash-merging collapses them into one commit on `main`.

- `1c43023` gitignore and LF line-ending normalisation
- `439f362` Go module pinned to go 1.27.3
- `15bac24` env config with validation (caarlos0/env)
- `c791b56` chi router with `/healthz`
- `2779cf5` request ID and panic recovery middleware
- `98645bd` `main.go` composition root with server timeouts and graceful shutdown
- `39b6180` multi-stage Dockerfile on distroless static, nonroot, about 17MB
- `b2790c1` readable shutdown-timeout log line
- `40d293f` docker-compose stack with Postgres 18.6 and Redis 8.10.3, plus `.env.example`
- `aa6cddb` Makefile
- `47b8699` lint-driven cleanup of the middleware and tests
- `db75a0d` golangci-lint v2 config
- `f864519` GitHub Actions CI: lint, race tests, image build, compose smoke test
- `2612e18` ADRs 0001–0006
- `71b680e` README

## Open questions / blockers

- **CLAUDE.md § 16 CI items 6 and 8** (integration tests, migrations up and down) are not
  in the workflow, because nothing exists for them to exercise yet. They must be added
  in phase 1.
- **Local toolchain gaps:** `make`, `golangci-lint` and a C compiler (needed for `-race`)
  are not installed on the dev machine. Containers work around all three (see the
  README's Development section). Installing them natively is the user's call.
- **Stray files in the workspace root, outside the repo:** `PROGRESS.md` there is a
  leftover from an unrelated project ("Clientele"), and `.claude/launch.json` points at
  that project's React app. They were left untouched. Delete them or keep them?
- GitHub Actions are pinned to major tags (`@v7`), not commit SHAs. Should they move to
  SHA pinning for supply-chain hardening?

## Decisions log

- 2026-11-04: `main` was bootstrapped with one empty root commit (`0af1be3`), and phase 0
  was rebased onto it, so that phase 0 arrives through a PR like every later phase. This
  was the owner's choice over pushing the branch directly as `main`.
- 2026-11-04: The package in `internal/transport/http` is named `httptransport` so it
  never shadows `net/http`. Callers import it with that alias.
- 2026-11-04: `go.mod` is the single source of the Go version (1.27.3). CI reads it with
  `go-version-file`, and `make check-go-version` fails CI if the Dockerfile's
  `GO_VERSION` differs.
- 2026-11-04: The runtime image is `gcr.io/distroless/static-debian13:nonroot`: a static
  CGO-free binary with no shell. As a result, the RateGuard container has no Docker
  HEALTHCHECK. `make smoke` and the CI job check `/healthz` from outside instead.
- 2026-11-04: A config variable that is set but empty takes its default (the library's
  behaviour, pinned by a test), so compose can pass `${VAR:-}` placeholders.
- 2026-11-04: Compose publishes Postgres on host port 5433 by default, because 5432 is
  taken by a local Postgres install on the dev machine. All dependency ports bind to
  127.0.0.1.
- 2026-11-04: The request ID and panic recovery middleware landed in phase 0 rather than
  phase 7, because CLAUDE.md §§ 10–11 make them standing requirements for every handler.
  There is still no per-request access logging (ADR 0004).
