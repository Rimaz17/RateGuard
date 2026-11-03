# ADR 0006: API keys hashed with SHA-256, two valid at once

- **Status:** Accepted (inherited from the project overview, § 8.6)
- **Date:** 2026-11-04

## Context

API keys authenticate callers on a path that runs on every request. They must be stored
so that a database leak does not hand out working keys, and they must be rotatable
without a flag-day cutover, because a rushed cutover is exactly when people paste a key
into a chat window.

## Options

1. **A slow password hash (bcrypt or argon2):** this costs milliseconds per request and
   buys nothing against keys that already carry 256 bits of entropy.
2. **SHA-256 of a high-entropy key:** fast, and brute force is infeasible.
3. **One key per client:** simple, but rotation forces downtime.
4. **A separate `api_keys` table that allows more than one active key per client.**

## Decision

Options 2 and 4:

- Keys are generated from `crypto/rand` with 256 bits of entropy and carry a readable
  prefix (`rg_cus_…`, `rg_mer_…`).
- Keys live in their own `api_keys` table, and a client can hold two valid keys at once.
  Rotation is issue the new key, migrate callers, then revoke the old one.
- Lookup is by the indexed `key_prefix`, followed by a constant-time comparison
  (`crypto/subtle`) of the SHA-256 digest.
- The full key is returned exactly once, at creation. It is never logged and is redacted
  from error paths, and only the prefix appears in logs and analytics.

## Consequences

- Authentication costs one indexed lookup and one hash, with no slow KDF.
- A lost key cannot be recovered, only replaced.
- A redaction helper is mandatory, and logging a key or a key hash counts as a bug.
