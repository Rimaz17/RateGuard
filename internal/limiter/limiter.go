// Package limiter is RateGuard's rate-limiting engine: one token bucket
// algorithm, implemented once and exactly (ADR 0011), behind a storage
// interface.
//
// The engine in engine.go is a pure function of a bucket's state, its limit
// and the current time. It reads no clock (ADR 0002) and does no I/O, so its
// correctness is proven in isolation. Stores keep bucket state: Memory holds
// it in process, and is also the fallback when Redis is unreachable
// (ADR 0001).
package limiter
