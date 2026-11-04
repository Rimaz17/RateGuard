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
	for _, err := range []error{domain.ErrInvalidAPIKey, domain.ErrClientDisabled} {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrConflict) {
			t.Errorf("%v must not match a CRUD category", err)
		}
	}
}
