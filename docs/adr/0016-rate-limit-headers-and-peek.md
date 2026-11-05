# ADR 0016: Rate-limit headers, the peek endpoint and error bodies

- **Status:** Accepted
- **Date:** 2026-11-05

## Context

Phase 6 finalises the decision contract that ADR 0015 started. The overview lists four
headers, `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset` and
`Retry-After`, and asks for `X-RateLimit-Reset` in delta-seconds (ADR 0002). It also
lists an optional `POST /v1/peek`, and asks for consistent error shapes. Three things
are left open:

- what exactly each header holds;
- what a peek answers;
- how routing misses look, since chi's defaults are a plain-text 404 and a 405 with an
  empty body.

## Options

For the headers:

1. **The IETF draft's `RateLimit` and `RateLimit-Policy` fields:** these are still a
   draft, and they are not what the overview names.
2. **The `X-RateLimit-*` family with epoch seconds for the reset,** as some public APIs
   use it. That needs the instance's clock, which ADR 0002 rules out.
3. **The `X-RateLimit-*` family with delta-seconds, mirroring the body.**

For a peek that would be refused:

1. **429, like a check:** a client could not tell "you would be refused" from "you were
   refused".
2. **200, with `allowed: false` in the body:** the peek itself succeeded.

## Decision

Option 3 for the headers, and option 2 for a refused peek.

- **Every decision response** (200 or 429 from `/v1/check`, and 200 from `/v1/peek`)
  carries the same values as its body:
  - `X-RateLimit-Limit` is `limit`, the bucket's capacity.
  - `X-RateLimit-Remaining` is `remaining`.
  - `X-RateLimit-Reset` is `reset_after`: seconds until the bucket is full again,
    rounded up. It is not a timestamp.
- **A 429** also carries `Retry-After`, the same number as `retry_after`, at least 1.
- **`POST /v1/peek`** takes the same body and identifies the caller the same way as
  `/v1/check`, but takes no token and never creates a bucket. It answers 200 whatever
  the outcome:
  - `allowed` says whether a check now would be allowed;
  - `remaining` is the whole tokens a check would start from;
  - `retry_after` is present when `allowed` is false.

  It sends the `X-RateLimit-*` headers but not `Retry-After`, because nothing was
  refused. Its errors are exactly those of `/v1/check`.
- **Every error response** has the standard body `{"error": "...", "request_id": "..."}`,
  including routing misses. An unknown path is a JSON 404. A known path with the wrong
  method is a JSON 405 with an `Allow` header listing the methods it does accept.

## Consequences

- An application can copy the four headers from RateGuard's response onto its own,
  unchanged, and its clients get a correct contract. The reference middleware does
  exactly that.
- Delta-seconds mean a client never needs a synchronised clock to use them.
- A dashboard or client SDK can show the allowance with `/v1/peek` without spending it.
- No response from RateGuard's router has a body a JSON client cannot parse.
