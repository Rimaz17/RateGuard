// Package policy manages rate-limit policies and their endpoint rules, and
// resolves which limit applies to a request.
//
// Resolution sits on the decision hot path, so it reads from an in-process
// cache with a 10-second TTL, collapses concurrent misses with singleflight,
// and drops entries the moment any instance publishes a change on Redis
// (ADR 0005). Which policy applies, and which endpoint rule wins, is
// defined in ADR 0009 and ADR 0010.
package policy
