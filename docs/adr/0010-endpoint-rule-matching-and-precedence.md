# ADR 0010: Endpoint rule matching and precedence

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

An endpoint rule gives one part of an API its own limit, for example `/api/login` at
5 requests a minute on the Free plan while the rest of the API gets 100. The overview
requires "endpoint-specific rules with path matching and precedence" and, for v1,
"documented precedence". It does not define the pattern syntax or how two matching
rules are ranked.

Two properties matter more than expressiveness:

- **No easy bypass.** The calling application forwards the path it received. If
  `/api/login/` or `/api//login` escaped a rule on `/api/login`, the strict limit
  would be one character away from not applying.
- **Cheap on the hot path.** Rules are matched for every decision, in memory.

## Options

1. **Exact paths only.** Simple, but `/api/orders/{id}` needs a rule per order.
2. **Regular expressions.** Expressive, but precedence between two regexes cannot be
   defined in a way people can predict, and a bad pattern can be slow.
3. **Segment patterns with whole-segment wildcards**, ranked left to right like
   common HTTP routers.

## Decision

Option 3.

**Patterns.** A pattern starts with `/` and is split on `/` into segments:

- A literal segment matches itself exactly, case-sensitively.
- `*` matches exactly one non-empty segment.
- `**` matches zero or more segments, and may only be the last segment.
- `/` alone matches only the root path.

Wildcards must be whole segments (`/api/v*` is rejected). Empty segments, `.` and
`..` are rejected, as is a trailing slash. A pattern is at most 512 bytes.

**Methods.** A rule names one HTTP method or `*` for any. Methods are compared in
upper case.

**Request paths** are normalised before matching: an empty path becomes `/`, a
missing leading slash is added, and `path.Clean` removes duplicate and trailing
slashes and resolves `.` and `..`. So `/api/login/`, `/api//login` and
`/api/x/../login` all match a rule on `/api/login`. The caller should send the
decoded path without the query string, as its own router sees it.

**Precedence.** When several of a policy's rules match a request, the most specific
wins:

1. Compare the patterns segment by segment, left to right. At the first position
   where they differ in kind, a literal beats `*`, and `*` beats `**`. A pattern that
   has ended beats a `**` that matches nothing more, so `/api` beats `/api/**` for
   the path `/api`.
2. If the patterns are equally specific, a rule for the request's method beats a rule
   for `*`.

Two different rules can never tie on both, since the unique constraint on
`(policy_id, method, path_pattern)` prevents duplicates, and two different patterns of
the same shape cannot both match one path.

If no rule matches, the policy's own limit applies.

**Buckets.** A matching rule has its own bucket per caller, shared by every path the
rule matches. `/api/orders/*` at 20 a minute allows 20 order requests a minute in
total, not 20 per order.

A policy holds at most 100 rules, so the in-memory scan stays short.

## Consequences

- Precedence can be explained in two sentences, and it does not depend on the order
  in which rules were created.
- Rules are sorted by precedence once, when a policy is loaded into the cache, so a
  decision takes the first match.
- Literal matching is case-sensitive. An application whose router ignores case should
  lower-case paths before calling RateGuard.
- Path parameters inside a segment (`/files/{name}.json`) cannot be expressed. A
  later ADR can add them if they are needed.
