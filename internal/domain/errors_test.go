package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

func TestSpecificErrorsWrapTheirCategory(t *testing.T) {
	tests := []struct {
		err      error
		category error
		message  string
	}{
		{domain.ErrClientNotFound, domain.ErrNotFound, "client not found"},
		{domain.ErrPolicyNotFound, domain.ErrNotFound, "policy not found"},
		{domain.ErrPolicyNameTaken, domain.ErrConflict, "policy name already in use: conflict"},
		{domain.ErrPolicyDisabled, domain.ErrConflict, "policy is disabled: conflict"},
		{domain.ErrEndpointRuleNotFound, domain.ErrNotFound, "endpoint rule not found"},
		{domain.ErrEndpointRuleExists, domain.ErrConflict, "endpoint rule for this method and path pattern already exists: conflict"},
		{domain.ErrEndpointRuleLimit, domain.ErrConflict, "policy already has 100 endpoint rules: conflict"},
		{domain.ErrAPIKeyNotFound, domain.ErrNotFound, "API key not found"},
		{domain.ErrActiveKeyLimit, domain.ErrConflict, "client already has 2 active API keys; revoke one first: conflict"},
		{domain.ErrAPIKeyPrefixTaken, domain.ErrConflict, "API key prefix already in use: conflict"},
	}
	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			wrapped := fmt.Errorf("loading thing: %w", tt.err)
			if !errors.Is(wrapped, tt.category) {
				t.Errorf("errors.Is(%v, %v) = false, want true", wrapped, tt.category)
			}
			if !errors.Is(wrapped, tt.err) {
				t.Errorf("errors.Is(%v, %v) = false, want true", wrapped, tt.err)
			}
			if got := tt.err.Error(); got != tt.message {
				t.Errorf("Error() = %q, want %q", got, tt.message)
			}
		})
	}
}

func TestCategoriesAreDistinct(t *testing.T) {
	if errors.Is(domain.ErrClientNotFound, domain.ErrConflict) {
		t.Error("ErrClientNotFound must not match ErrConflict")
	}
	if errors.Is(domain.ErrPolicyNameTaken, domain.ErrNotFound) {
		t.Error("ErrPolicyNameTaken must not match ErrNotFound")
	}
	for _, err := range []error{domain.ErrInvalidAPIKey, domain.ErrClientDisabled, domain.ErrNoPolicy} {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%v must not match a CRUD category", err)
		}
	}
}

func TestValidationError(t *testing.T) {
	var err error = &domain.ValidationError{Field: "path_pattern", Reason: "must start with /"}
	wrapped := fmt.Errorf("creating rule: %w", err)

	if got := err.Error(); got != "path_pattern must start with /" {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(wrapped, domain.ErrInvalid) {
		t.Error("a ValidationError must match ErrInvalid")
	}
	if errors.Is(wrapped, domain.ErrNotFound) || errors.Is(wrapped, domain.ErrConflict) {
		t.Error("a ValidationError must match no other category")
	}
	var ve *domain.ValidationError
	if !errors.As(wrapped, &ve) || ve.Field != "path_pattern" {
		t.Errorf("errors.As found %+v, want the original error", ve)
	}
}
