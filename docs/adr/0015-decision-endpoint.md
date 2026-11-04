# ADR 0015: The decision endpoint

- **Status:** Accepted
- **Date:** 2026-11-05

## Context

`POST /v1/check` is the hot path: an application asks whether a request it has received
may proceed. The overview fixes the response fields (`allowed`, `limit`, `remaining`,
`reset_after`, `retry_after`) and says the caller is identified by API key, or by IP as
a fallback. It does not say what the request carries, which status codes the endpoint
uses, or how durations are rounded.

## Options

For the status of a refusal:

1. **Always 200**, with `allowed: false` in the body. The application must read the body
   to learn anything.
2. **429 for a refusal, 200 for an allowance**, with the same body either way. An
   application, or a proxy in front of it, can pass the status straight on to its own
   client.

## Decision

Option 2, with the rest of the contract as follows.

- **The request body** is `{"method": "GET", "path": "/api/products"}`, both required:
  the method and path of the request the application received.
  - `method` is an HTTP method token of at most 32 characters; case does not matter.
  - `path` starts with `/` and is at most 2,048 bytes. It may not contain `?`, `#` or
    control characters. A path with a query string would not match the rule for the
    path without it, so `/api/login?x` would escape a strict rule on `/api/login`.
    Refusing it makes an integration that forwards the raw request URI fail loudly
    instead of quietly bypassing its limits.
  - The body is at most 4 KiB, and unknown fields are refused.
- **The caller** is the one the application is asking about, identified as the
  `Identify` middleware already does (ADR 0006, ADR 0008). The application forwards
  the caller's `Authorization: Bearer <API key>` header if there is one. Without one,
  the caller is identified by IP: the connection's address, or the `X-Forwarded-For`
  entries added by networks in `RG_TRUSTED_PROXIES`. An application that wants per-IP
  limits must append its caller's address to `X-Forwarded-For`, and its own network
  must be trusted.
- **The response** has the same body for both outcomes:
  `{"allowed": true, "limit": 100, "remaining": 72, "reset_after": 18}`, plus
  `"retry_after": 12` on a refusal only. Durations are whole seconds, rounded up, so
  that waiting `retry_after` seconds is always enough (ADR 0011) and a refused request
  never says 0.
- **Statuses:**
  - 200: allowed.
  - 429: refused.
  - 400, 413 or 415: the request body is wrong.
  - 401: the API key is invalid, malformed or revoked.
  - 403: the client is disabled, or no enabled policy applies to the caller. A caller
    without a policy is refused, never allowed unlimited (ADR 0009).
  - 500: anything else, with only a request ID in the body.

  Every response other than 200 and 429 has the standard error body.
- **Nothing is logged** for an allowed or refused decision (ADR 0004). A caller that
  disconnects mid-decision gets no response and no log line.
- Decisions are sent with `Cache-Control: no-store`.

## Consequences

- An integration takes the status as the answer: 200 serves, 429 is passed on, and
  anything else is an error the integrator chooses how to handle.
- The response headers (`X-RateLimit-*`, `Retry-After`) and `POST /v1/peek` follow in
  phase 6, on top of this body.
- Rounding up means a client told to wait 1 second may sometimes have been allowed a
  little sooner. It is never told a time that is too short.
