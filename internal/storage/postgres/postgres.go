// Package postgres implements RateGuard's repositories on PostgreSQL with
// pgx, and applies the schema migrations. It is used by the admin API only:
// the /v1/check hot path never reaches Postgres (ADR 0004).
package postgres
