package domain_test

import (
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

func TestIsID(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"01a1a2fb-68ca-772e-a1e6-072972830b19", true},
		{"01A1A2FB-68CA-772E-A1E6-072972830B19", true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"", false},
		{"not-a-uuid", false},
		{"01a1a2fb68ca772ea1e6072972830b19", false},       // no hyphens
		{"{01a1a2fb-68ca-772e-a1e6-072972830b19}", false}, // braces
		{"01a1a2fb-68ca-772e-a1e6-072972830b1", false},    // too short
		{"01a1a2fb-68ca-772e-a1e6-072972830b199", false},  // too long
		{"01a1a2fb-68ca-772e-a1e6_072972830b19", false},   // wrong separator
		{"01a1a2fg-68ca-772e-a1e6-072972830b19", false},   // non-hex digit
		{"01a1a2fb-68ca-772e-a1e6-07297283\x000b1", false},
	}
	for _, tt := range tests {
		if got := domain.IsID(tt.in); got != tt.want {
			t.Errorf("IsID(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
