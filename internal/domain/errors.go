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
)
