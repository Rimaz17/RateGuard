# ADR 0008: The admin API requires a shared bearer token

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

Phase 2 adds the admin API: `POST /v1/clients` registers an application and returns a
working API key, and `/v1/clients/{id}/keys` issues and revokes keys. Phase 3 adds
policy management behind the same surface. The project overview treats multi-tenant
admin authentication as a step after v1, but it specifies no protection for the admin
API in v1 at all.

Left open, anyone who can reach RateGuard could mint keys, read the client list or
revoke another application's keys. Binding to a private network reduces that risk
without removing it, and it fails silently when someone publishes the port.

## Options

1. **No admin authentication in v1**, relying on network placement. This is the least
   work, but the failure mode is an open key-minting endpoint.
2. **A single shared bearer token, `RG_ADMIN_TOKEN`**, required at startup. This
   takes a few lines, carries no new dependency and needs no schema change.
3. **Admin users with their own credentials and roles.** This is the multi-tenant
   admin authentication the overview defers until after v1.

## Decision

Option 2. Every `/v1/clients` route, and every later admin route, requires
`Authorization: Bearer <RG_ADMIN_TOKEN>`.

- The token is required and must be at least 32 characters. The service refuses to
  start without it.
- The presented and configured tokens are compared as SHA-256 digests with
  `crypto/subtle`, so neither their contents nor their lengths leak through timing.
- A missing or wrong token gets `401` with `WWW-Authenticate: Bearer`. The token is
  never logged.
- The probes (`/healthz`, `/readyz`) stay open. Application API keys authenticate the
  decision endpoints, which are a separate surface.

## Consequences

- Operators must set `RG_ADMIN_TOKEN`. `configs/.env.example` carries a local-only
  placeholder, and a `.env` created before phase 2 needs the line added.
- Anyone holding the token can do everything. Rotating it means restarting with a new
  value, briefly rejecting admin calls that carry the old one. That is acceptable for
  an occasional admin path.
- Option 3 can replace this later without changing the application-facing API.
