package limiter

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// Bucket keys are an internal detail with no exported constructor, so these
// tests sit inside the package. The rest of the limiter is tested through
// its exported API.

const (
	testClient = "0193f2a0-0000-7000-8000-00000000000a"
	testRule   = "0193f2a0-0000-7000-8000-0000000000f1"
)

func TestKeyFor(t *testing.T) {
	tests := []struct {
		clientID, endpoint, want string
	}{
		{"client:" + testClient, "", "rg:bucket:all:client:" + testClient},
		{"client:" + testClient, testRule, "rg:bucket:" + testRule + ":client:" + testClient},
		{"ip:203.0.113.7", "", "rg:bucket:all:ip:203.0.113.7"},
		{"ip:2001:db8:1:2::/64", testRule, "rg:bucket:" + testRule + ":ip:2001:db8:1:2::/64"},
	}
	for _, tt := range tests {
		if got := keyFor(tt.clientID, tt.endpoint); got != tt.want {
			t.Errorf("keyFor(%q, %q) = %q, want %q", tt.clientID, tt.endpoint, got, tt.want)
		}
	}
}

func TestKeyForSeparatesScopes(t *testing.T) {
	// A rule's bucket and the policy's bucket are distinct for one caller,
	// and one rule's bucket is distinct between callers.
	keys := map[string]bool{}
	for _, k := range []string{
		keyFor("client:"+testClient, ""),
		keyFor("client:"+testClient, testRule),
		keyFor("ip:203.0.113.7", ""),
		keyFor("ip:203.0.113.7", testRule),
	} {
		if keys[k] {
			t.Fatalf("duplicate key %q", k)
		}
		keys[k] = true
	}
}

func TestCallerID(t *testing.T) {
	tests := []struct {
		name string
		id   domain.Identity
		want string
	}{
		{"keyed", domain.Identity{Kind: domain.IdentityAPIKey, ClientID: testClient, KeyPrefix: "rg_cus_x"}, "client:" + testClient},
		{"ipv4", ip("203.0.113.7"), "ip:203.0.113.7"},
		{"ipv4-mapped ipv6 is the ipv4 caller", ip("::ffff:203.0.113.7"), "ip:203.0.113.7"},
		{"ipv6 keyed by its /64", ip("2001:db8:1:2:aaaa:bbbb:cccc:dddd"), "ip:2001:db8:1:2::/64"},
		{"ipv6 zone dropped", ip("fe80::1%eth0"), "ip:fe80::/64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := callerID(tt.id)
			if err != nil {
				t.Fatalf("callerID: %v", err)
			}
			if got != tt.want {
				t.Errorf("callerID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCallerIDSharesOneBucketAcrossAnIPv6Network(t *testing.T) {
	a, _ := callerID(ip("2001:db8:1:2::1"))
	b, _ := callerID(ip("2001:db8:1:2:ffff:ffff:ffff:ffff"))
	c, _ := callerID(ip("2001:db8:1:3::1"))
	if a != b {
		t.Errorf("two addresses in one /64 got %q and %q, want one caller", a, b)
	}
	if a == c {
		t.Errorf("addresses in different /64s both got %q, want two callers", a)
	}
}

func TestCallerIDRefusesIdentitiesWithoutACaller(t *testing.T) {
	for name, id := range map[string]domain.Identity{
		"keyed without client": {Kind: domain.IdentityAPIKey},
		"ip without address":   {Kind: domain.IdentityIP},
		"unknown kind":         {Kind: "carrier-pigeon"},
		"zero identity":        {},
	} {
		if _, err := callerID(id); !errors.Is(err, errNoCaller) {
			t.Errorf("%s: error = %v, want errNoCaller", name, err)
		}
	}
}

func ip(s string) domain.Identity {
	return domain.Identity{Kind: domain.IdentityIP, IP: netip.MustParseAddr(s)}
}
