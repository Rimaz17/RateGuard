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

	// ErrInvalid means the input breaks a rule that only the domain knows,
	// such as the syntax of a path pattern. ValidationError wraps it.
	ErrInvalid = errors.New("invalid")
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

	// ErrPolicyDisabled means the policy cannot be assigned because it is
	// switched off.
	ErrPolicyDisabled = fmt.Errorf("policy is disabled: %w", ErrConflict)

	// ErrEndpointRuleNotFound means the policy has no endpoint rule with the
	// given ID.
	ErrEndpointRuleNotFound = fmt.Errorf("endpoint rule %w", ErrNotFound)

	// ErrEndpointRuleExists means the policy already has a rule for the same
	// method and path pattern.
	ErrEndpointRuleExists = fmt.Errorf("endpoint rule for this method and path pattern already exists: %w", ErrConflict)

	// ErrEndpointRuleLimit means the policy already holds MaxEndpointRules
	// rules.
	ErrEndpointRuleLimit = fmt.Errorf("policy already has %d endpoint rules: %w", MaxEndpointRules, ErrConflict)

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

// ErrNoPolicy means no enabled policy applies to the caller: it has none
// assigned, or its policy is disabled, and there is no enabled default
// policy. The request must be refused, never allowed unlimited (ADR 0009).
var ErrNoPolicy = errors.New("no enabled policy applies to this caller")

// ValidationError reports input that breaks a domain rule. Its message is
// written for the caller and names the field, so it is safe to return to a
// client as is.
type ValidationError struct {
	// Field is the name of the offending input, as the API spells it.
	Field string

	// Reason completes the sentence that starts with Field.
	Reason string
}

// Error returns the field and reason as one client-safe sentence.
func (e *ValidationError) Error() string { return e.Field + " " + e.Reason }

// Unwrap makes every ValidationError match ErrInvalid.
func (e *ValidationError) Unwrap() error { return ErrInvalid }
