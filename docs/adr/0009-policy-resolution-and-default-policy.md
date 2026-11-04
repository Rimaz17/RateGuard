# ADR 0009: How a request's policy is resolved, and the default policy

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

Phase 3 resolves the limit that applies to each request: "Free / Premium / Enterprise
policies resolve correctly per request". The overview says a client is assigned a
policy and that endpoint rules beat the client's policy. It leaves three cases open:

- A caller with no API key is identified by IP (phase 2). It has no client, so it has
  no assigned policy.
- A registered client starts with no policy until an admin assigns one.
- A policy can be disabled while clients are still assigned to it.

Whatever these cases resolve to, they must never resolve to "unlimited": ADR 0001
rules that out even for a Redis outage, and a missing configuration row is no better
an excuse.

## Options

1. **Refuse every request in these cases.** This is safe, but a fresh install refuses
   all keyless traffic, and disabling a plan becomes an outage for its clients.
2. **A default policy named by an environment variable.** It is simple, but changing
   it means restarting every instance, so it cannot follow the § 8.5 promise that a
   plan change lands in milliseconds.
3. **A default policy flagged in the database**, managed through the admin API and
   invalidated like any other policy write.

## Decision

Option 3.

- `policies.is_default` marks at most one policy, enforced by a partial unique
  index. Setting it on one policy clears it from the previous one in the same
  transaction, under an advisory lock, so two concurrent changes cannot both win.
- Resolution of a request:
  1. A caller with an API key uses its client's assigned policy, if the client has
     one and it is enabled.
  2. Otherwise, the caller (keyed, or identified by IP) uses the default policy, if
     there is one and it is enabled.
  3. Otherwise, there is no applicable policy. Resolution returns
     `domain.ErrNoPolicy`, and the decision endpoint must refuse the request. It
     never allows it unlimited.
- Within the chosen policy, a matching endpoint rule beats the policy's own limit.
  The matching and precedence rules are in ADR 0010.
- An endpoint rule has a capacity and a window. Its refill rate is
  `capacity / window_seconds`. A policy's refill rate defaults to the same, and
  can be set explicitly to allow bursts larger than the sustained rate. A `PATCH`
  that changes capacity or window without giving a refill rate recomputes it, so
  the two never silently disagree.
- Assigning a disabled policy to a client is refused with `409`. Disabling a policy
  later is allowed: its clients fall back to the default policy.
- `DELETE /v1/policies/{id}` disables a policy; nothing deletes one, so a client
  never points at a missing row. Endpoint rules have no dependants and are deleted
  outright.

## Consequences

- An operator who wants keyless traffic served must mark a policy as the default.
  Until then, keyless requests are refused once `/v1/check` exists.
- Disabling the default policy means every caller without an enabled assigned policy
  is refused. That is the admin's explicit choice, and it is documented.
- Every write that can change a resolution publishes an invalidation: the client ID
  for an assignment, and the policy ID for a policy or rule change. Because any policy
  write may move the default flag, a policy invalidation also drops the cached
  default (ADR 0005).
