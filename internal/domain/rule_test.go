package domain_test

import (
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

func TestRefillRates(t *testing.T) {
	tests := []struct {
		capacity, window int
		want             float64
	}{
		{100, 60, 100.0 / 60},
		{5, 60, 5.0 / 60},
		{10, 1, 10},
		{1, 86400, 1.0 / 86400},
	}
	for _, tt := range tests {
		if got := domain.DefaultRefillRate(tt.capacity, tt.window); got != tt.want {
			t.Errorf("DefaultRefillRate(%d, %d) = %v, want %v", tt.capacity, tt.window, got, tt.want)
		}
		r := domain.EndpointRule{Capacity: tt.capacity, WindowSeconds: tt.window}
		if got := r.RefillRate(); got != tt.want {
			t.Errorf("EndpointRule{%d, %d}.RefillRate() = %v, want %v", tt.capacity, tt.window, got, tt.want)
		}
	}
}
