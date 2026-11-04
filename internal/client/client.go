// Package client registers client applications, issues, rotates and
// revokes their API keys, and authenticates the keys they present.
//
// The full key exists only on its way back to the caller: storage receives
// its public prefix and SHA-256 digest, and logs only ever see the prefix
// (ADR 0006).
package client
