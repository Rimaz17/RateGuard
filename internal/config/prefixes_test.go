package config_test

import (
	"net/netip"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/config"
)

func TestPrefixesUnmarshalText(t *testing.T) {
	tests := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{"", nil, false},
		{" , ", nil, false},
		{"10.0.0.0/8", []string{"10.0.0.0/8"}, false},
		{"10.1.2.3/8", []string{"10.0.0.0/8"}, false}, // host bits are masked off
		{" 10.0.0.0/8 ,fd00::/8", []string{"10.0.0.0/8", "fd00::/8"}, false},
		{"10.0.0.1", nil, true},
		{"10.0.0.0/33", nil, true},
		{"proxy.internal/32", nil, true},
	}
	for _, tt := range tests {
		var p config.Prefixes
		err := p.UnmarshalText([]byte(tt.in))
		if (err != nil) != tt.wantErr {
			t.Errorf("UnmarshalText(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if len(p) != len(tt.want) {
			t.Errorf("UnmarshalText(%q) = %v, want %v", tt.in, p, tt.want)
			continue
		}
		for i := range p {
			if p[i].String() != tt.want[i] {
				t.Errorf("UnmarshalText(%q)[%d] = %s, want %s", tt.in, i, p[i], tt.want[i])
			}
		}
	}
}

func TestPrefixesContains(t *testing.T) {
	var p config.Prefixes
	if err := p.UnmarshalText([]byte("10.0.0.0/8,fd00::/8")); err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{
		"10.20.30.40": true,
		"11.0.0.1":    false,
		"fd00::1":     true,
		"2001:db8::1": false,
	} {
		if got := p.Contains(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Contains(%s) = %v, want %v", addr, got, want)
		}
	}
	if (config.Prefixes{}).Contains(netip.MustParseAddr("10.0.0.1")) {
		t.Error("an empty list must trust nothing")
	}
}
