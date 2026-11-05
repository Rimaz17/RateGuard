package httptransport_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/config"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

func proxies(t *testing.T, cidrs string) config.Prefixes {
	t.Helper()
	var p config.Prefixes
	if err := p.UnmarshalText([]byte(cidrs)); err != nil {
		t.Fatalf("parsing %q: %v", cidrs, err)
	}
	return p
}

func TestClientIP(t *testing.T) {
	lb := proxies(t, "10.0.0.0/8,fd00::/8")
	tests := []struct {
		name    string
		remote  string
		xff     []string
		trusted httptransport.TrustedProxies
		want    string
	}{
		{"direct caller", "203.0.113.7:51000", nil, lb, "203.0.113.7"},
		{"direct caller's header is ignored", "203.0.113.7:51000", []string{"1.2.3.4"}, lb, "203.0.113.7"},
		{"no trusted proxies configured", "10.0.0.5:51000", []string{"1.2.3.4"}, nil, "10.0.0.5"},
		{"empty trust list", "10.0.0.5:51000", []string{"1.2.3.4"}, config.Prefixes{}, "10.0.0.5"},
		{"behind one proxy", "10.0.0.5:51000", []string{"198.51.100.9"}, lb, "198.51.100.9"},
		{"spoofed entries left of the real one", "10.0.0.5:51000", []string{"6.6.6.6, 7.7.7.7, 198.51.100.9"}, lb, "198.51.100.9"},
		{"chain of trusted proxies", "10.0.0.5:51000", []string{"198.51.100.9, 10.1.1.1, 10.2.2.2"}, lb, "198.51.100.9"},
		{"multiple header lines", "10.0.0.5:51000", []string{"198.51.100.9", "10.1.1.1"}, lb, "198.51.100.9"},
		{"spaces and empty entries", "10.0.0.5:51000", []string{" , 198.51.100.9 ,, "}, lb, "198.51.100.9"},
		{"every hop trusted", "10.0.0.5:51000", []string{"10.3.3.3, 10.1.1.1"}, lb, "10.3.3.3"},
		{"trusted proxy without header", "10.0.0.5:51000", nil, lb, "10.0.0.5"},
		{"garbage stops the walk", "10.0.0.5:51000", []string{"198.51.100.9, not-an-ip, 10.1.1.1"}, lb, "10.1.1.1"},
		{"ipv6 caller", "[2001:db8::1]:443", nil, lb, "2001:db8::1"},
		{"ipv6 through ipv6 proxy", "[fd00::5]:443", []string{"2001:db8::2"}, lb, "2001:db8::2"},
		{"ipv4-mapped peer is unwrapped", "[::ffff:203.0.113.7]:51000", nil, lb, "203.0.113.7"},
		{"ipv4-mapped proxy is still trusted", "[::ffff:10.0.0.5]:51000", []string{"::ffff:198.51.100.9"}, lb, "198.51.100.9"},
		{"zone is dropped", "[fe80::1%eth0]:51000", nil, lb, "fe80::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			got, ok := httptransport.ClientIP(r, tt.trusted)
			if !ok || got != netip.MustParseAddr(tt.want) {
				t.Errorf("ClientIP = %v, %v; want %s, true", got, ok, tt.want)
			}
		})
	}
}

func TestClientIPWithoutAnAddress(t *testing.T) {
	for _, remote := range []string{"", "@", "/run/rateguard.sock"} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
		r.RemoteAddr = remote
		if got, ok := httptransport.ClientIP(r, nil); ok {
			t.Errorf("ClientIP with RemoteAddr %q = %v, true; want false", remote, got)
		}
	}
}

// A Unix-socket peer has no address, but a trusted one may say whom it is
// forwarding for (ADR 0017).
func TestClientIPFromATrustedSocketPeer(t *testing.T) {
	lb := proxies(t, "10.0.0.0/8")
	tests := []struct {
		name    string
		xff     []string
		trusted httptransport.TrustedProxies
		want    string // "" means no address
	}{
		{"the application's entry is the caller", []string{"198.51.100.9"}, nil, "198.51.100.9"},
		{"entries left of it are the caller's own", []string{"6.6.6.6, 198.51.100.9"}, nil, "198.51.100.9"},
		{"the application's own proxy is walked past", []string{"198.51.100.9, 10.1.1.1"}, lb, "198.51.100.9"},
		{"ipv4-mapped entry is unwrapped", []string{"::ffff:198.51.100.9"}, nil, "198.51.100.9"},
		{"no header, no address", nil, nil, ""},
		{"only garbage, no address", []string{"not-an-ip"}, nil, ""},
		{"only trusted hops ends on the last of them", []string{"10.3.3.3, 10.1.1.1"}, lb, "10.3.3.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := httptransport.TrustPeer(t.Context(), nil)
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
			r.RemoteAddr = "@"
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			got, ok := httptransport.ClientIP(r, tt.trusted)
			if tt.want == "" {
				if ok {
					t.Errorf("ClientIP = %v, true; want no address", got)
				}
				return
			}
			if !ok || got != netip.MustParseAddr(tt.want) {
				t.Errorf("ClientIP = %v, %v; want %s, true", got, ok, tt.want)
			}
		})
	}
}

func TestClientIPIgnoresTheHeaderFromAnUntrustedSocketPeer(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	r.RemoteAddr = "@"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got, ok := httptransport.ClientIP(r, nil); ok {
		t.Errorf("ClientIP without TrustPeer = %v, true; want no address", got)
	}
}

func TestClientIPBoundsTheWalk(t *testing.T) {
	lb := proxies(t, "10.0.0.0/8")
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	r.RemoteAddr = "10.0.0.5:51000"
	// A real caller far to the left, behind more trusted hops than the
	// walk will take.
	r.Header.Set("X-Forwarded-For", "198.51.100.9,"+strings.Repeat("10.9.9.9,", 40)+"10.1.1.1")

	got, ok := httptransport.ClientIP(r, lb)
	if !ok || !lb.Contains(got) {
		t.Errorf("ClientIP = %v, %v; want the walk to stop on a trusted hop", got, ok)
	}
}
