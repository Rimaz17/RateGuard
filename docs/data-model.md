# Data model

PostgreSQL holds everything that is configured: policies and their endpoint rules,
clients and their API keys. Redis holds only bucket state and the
policy-invalidation channel. The hot path (`/v1/check`) never reads Postgres
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
| `is_default` | `BOOLEAN` | Default `false`. At most one row is `true`, enforced by the partial unique index `policies_one_default_idx` |
| `created_at`, `updated_at` | `TIMESTAMPTZ` | Default `now()` |

The default policy applies to callers with no enabled policy of their own, including
callers identified only by IP ([ADR 0009](adr/0009-policy-resolution-and-default-policy.md)).

## `endpoint_rules`

A policy's own limit for part of an API, such as 5 requests a minute on `/api/login`
under the Free plan. A matching rule beats its policy's limit.
[ADR 0010](adr/0010-endpoint-rule-matching-and-precedence.md) defines the pattern
syntax and which rule wins when several match. A policy holds at most 100 rules.

| Column | Type | Notes |
|---|---|---|
| `id` | `UUID` | Primary key, `uuidv7()` |
| `policy_id` | `UUID` | References `policies(id)`, `ON DELETE CASCADE` |
| `method` | `TEXT` | An upper-case HTTP method, or `*` for any |
| `path_pattern` | `TEXT` | Starts with `/`, at most 512 bytes. Segments may be `*` (one segment) or, last, `**` (any number) |
| `capacity` | `INTEGER` | Greater than 0 |
| `window_seconds` | `INTEGER` | Greater than 0. The rule refills at `capacity / window_seconds` tokens a second |
| `created_at` | `TIMESTAMPTZ` | Default `now()` |

`(policy_id, method, path_pattern)` is unique. Its index leads with `policy_id`, so it
also serves loading a policy's rules.

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

## `api_keys`

A client's API keys. They live in their own table so that a client can hold two valid
keys at once and rotate without downtime: issue the new key, move callers onto it,
then revoke the old one ([ADR 0006](adr/0006-api-keys-sha256-two-active.md)). The
limit of two active keys is enforced when a key is issued, under a row lock on the
client.

| Column | Type | Notes |
|---|---|---|
| `id` | `UUID` | Primary key, `uuidv7()` |
| `client_id` | `UUID` | References `clients(id)`, `ON DELETE CASCADE`. Indexed |
| `key_prefix` | `TEXT` | Public part of the key, `rg_<tag>_<16 base32 chars>`. Unique, so indexed: the authentication lookup |
| `key_hash` | `BYTEA` | SHA-256 of the full key, exactly 32 bytes. The key itself is never stored |
| `created_at` | `TIMESTAMPTZ` | Default `now()` |
| `revoked_at` | `TIMESTAMPTZ` | `NULL` while active. Never earlier than `created_at` |

A full key looks like `rg_cus_7hq2mfx4kd5ta3wb_<52 base32 chars>`:

- `rg` marks a RateGuard key.
- `cus` is a tag taken from the client's name, so a key is easy to attribute.
- The next 16 characters (80 random bits) make the prefix unique.
- The last 52 characters are the 256-bit secret.

Only the part up to and including the 16 random characters is ever logged or shown
again.

## Redis

Redis holds nothing that cannot be lost: losing it refills every bucket and costs a
flush of every instance's caches.

### Bucket state

One hash per bucket in use ([ADR 0013](adr/0013-redis-bucket-store.md)), under a key
that only `keyFor` in `internal/limiter` builds:

```
rg:bucket:<scope>:<caller>
  scope   all (the policy's own limit) or an endpoint rule's ID
  caller  client:<client-id>, ip:<IPv4 address> or ip:<IPv6 network>/64
```

| Field | Value |
|---|---|
| `tokens` | The balance as of the last refill, as a decimal with 17 significant digits |
| `last_refill_us` | Redis's own clock (`TIME`) at the last refill, in microseconds |

Microseconds are used rather than the overview's milliseconds so the stored time has
the resolution of the clock the script reads. Only an allowed take writes the hash; a
refusal or a peek leaves it alone. Each write sets `PEXPIRE` to the time until the
bucket is full again, so a key disappears once it would decide like a new bucket. The
policy is not part of the key, so a client keeps its balance when its plan changes.

### Invalidation channel

`rg:policy:invalidate` carries `client:<id>` (an assignment changed or a key was
revoked) and `policy:<id>` (a policy, its rules or the default flag changed). Every
instance subscribes and drops the matching cache entries
([ADR 0005](adr/0005-policy-cache-ttl-pubsub-singleflight.md),
[ADR 0012](adr/0012-api-key-cache-on-the-hot-path.md)).
