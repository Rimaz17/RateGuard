// Package domain holds RateGuard's entities, value objects and sentinel
// errors. It imports nothing outside the standard library, and nothing in
// it knows about HTTP, SQL or Redis: every other package may depend on
// domain, and domain depends on none of them.
package domain
