package domain

import (
	"errors"
	"fmt"
)

// Error categories. Callers that only care about the category, such as the
// HTTP layer choosing a status code, match these with errors.Is; the
// specific errors below wrap them.
var (
	// ErrNotFound means the referenced entity does not exist.
	ErrNotFound = errors.New("not found")

	// ErrConflict means the request is valid but clashes with current state,
	// such as a duplicate name.
	ErrConflict = errors.New("conflict")
)

// Specific errors. Each wraps a category, so errors.Is(ErrClientNotFound,
// ErrNotFound) holds.
var (
	// ErrClientNotFound means no client has the given ID.
	ErrClientNotFound = fmt.Errorf("client %w", ErrNotFound)

	// ErrPolicyNotFound means no policy has the given ID.
	ErrPolicyNotFound = fmt.Errorf("policy %w", ErrNotFound)

	// ErrPolicyNameTaken means another policy already uses the name.
	ErrPolicyNameTaken = fmt.Errorf("policy name already in use: %w", ErrConflict)

	// ErrAPIKeyNotFound means the client has no API key with the given ID.
	ErrAPIKeyNotFound = fmt.Errorf("API key %w", ErrNotFound)

	// ErrActiveKeyLimit means the client already holds MaxActiveKeys
	// unrevoked keys, so another cannot be issued until one is revoked.
	ErrActiveKeyLimit = fmt.Errorf("client already has %d active API keys; revoke one first: %w", MaxActiveKeys, ErrConflict)

	// ErrAPIKeyPrefixTaken means a newly generated key's prefix collided
	// with an existing one. With 80 random bits it should never happen;
	// the issuer retries with a fresh key if it does.
	ErrAPIKeyPrefixTaken = fmt.Errorf("API key prefix already in use: %w", ErrConflict)
)

// Authentication errors. They deliberately say nothing about which check
// failed: malformed, unknown, wrong and revoked keys all read the same.
var (
	// ErrInvalidAPIKey means the presented key does not authenticate.
	ErrInvalidAPIKey = errors.New("invalid API key")

	// ErrClientDisabled means the key is valid but its client is disabled.
	ErrClientDisabled = errors.New("client is disabled")
)
