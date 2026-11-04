# ADR 0007: Migrations run from the binary, as a separate step

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

Phase 1 introduces a schema, managed by golang-migrate as numbered, reversible SQL
pairs. Something has to apply them, in development, in CI and wherever RateGuard is
deployed. The runtime image is distroless, so it has no shell and no package manager.

## Options

1. **The service migrates itself on startup.** Nothing extra to run, but every
   replica races to migrate, a bad migration takes down every replica that restarts,
   and the service needs DDL rights it otherwise never uses.
2. **A separate golang-migrate CLI image runs the SQL files.** This is a second tool
   whose version must match, and the migration files have to be shipped next to it.
3. **The rateguard binary embeds the migrations and exposes a `migrate`
   subcommand, which runs as its own step before the service starts.**

## Decision

Option 3. `rateguard migrate up | down [N] | version` applies the SQL files embedded
with `go:embed`, so the image that serves is the image that migrates and the two can
never disagree. Docker Compose runs it as a one-shot `migrate` service that RateGuard
waits on (`service_completed_successfully`). `make migrate-up`, `make migrate-down`
and `make migrate-version` drive it by hand. The subcommand reads only
`RG_DATABASE_URL`.

## Consequences

- Starting the service never changes the schema. A deployment that forgets the
  migrate step fails visibly instead of drifting.
- golang-migrate takes a Postgres advisory lock, so two concurrent migrate runs are
  safe, and a migration that fails part-way leaves the version marked dirty for an
  operator to inspect.
- The service's own database role could be limited to DML later, with DDL rights
  only for the migrate step.
