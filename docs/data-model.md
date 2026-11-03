# Data model

PostgreSQL holds everything that is configured: clients, their API keys and
policies. Redis holds only bucket state and the policy-invalidation channel, and
it arrives in later phases. The hot path (`/v1/check`) never reads Postgres
([ADR 0004](adr/0004-the-hot-path-is-sacred.md)).

The schema is defined by the numbered migrations in [`migrations/`](../migrations).
Each one is a reversible `.up.sql` / `.down.sql` pair, and an applied migration is
never edited: changes go in a new one.

Conventions:

- Primary keys are `UUID DEFAULT uuidv7()` (PostgreSQL 18). They are time-ordered,
  which keeps the index compact and gives list endpoints a stable order.
- Timestamps are `TIMESTAMPTZ`, stored in UTC.
- Rules that must always hold are `CHECK` constraints, so the database rejects bad
  rows even if application validation is bypassed.

## `policies`

A named rate limit, such as Free, Premium or Enterprise. Policies are disabled
(`enabled = false`), never deleted.

| Column | Type | Notes |
|---|---|---|
| `id` | `UUID` | Primary key, `uuidv7()` |
| `name` | `TEXT` | Unique, not blank, at most 100 characters |
| `capacity` | `INTEGER` | Bucket size: the largest burst. Greater than 0 |
| `window_seconds` | `INTEGER` | Period the capacity is quoted over. Greater than 0 |
| `refill_rate` | `DOUBLE PRECISION` | Tokens returned per second. Finite and greater than 0 |
| `enabled` | `BOOLEAN` | Default `true` |
| `created_at`, `updated_at` | `TIMESTAMPTZ` | Default `now()` |

## `clients`

A registered application that calls RateGuard, such as a customer app or a
merchant app.

| Column | Type | Notes |
|---|---|---|
| `id` | `UUID` | Primary key, `uuidv7()` |
| `name` | `TEXT` | Not blank, at most 100 characters. Not unique: the ID is the identity |
| `policy_id` | `UUID` | References `policies(id)`. `NULL` until a policy is assigned. Indexed |
| `status` | `TEXT` | `active` (default) or `disabled` |
| `created_at`, `updated_at` | `TIMESTAMPTZ` | Default `now()` |
