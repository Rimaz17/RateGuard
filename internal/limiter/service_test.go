package limiter_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/limiter"
)

// The resolver is stubbed: which limit applies is the policy package's job,
// tested there. These tests cover what the service adds: which bucket a
// request draws from.

const (
	loginRule = "0193f2a0-0000-7000-8000-0000000000f1"
	clientA   = "0193f2a0-0000-7000-8000-00000000000a"
	clientB   = "0193f2a0-0000-7000-8000-00000000000b"
)

// plan resolves /api/login to a strict rule and everything else to the
// policy's own limit, and records what it was asked.
type plan struct {
	mu       sync.Mutex
	capacity int
	err      error
	asked    []string
}

func (p *plan) Resolve(_ context.Context, id domain.Identity, method, path string) (domain.ResolvedLimit, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, string(id.Kind)+" "+method+" "+path)
	if p.err != nil {
		return domain.ResolvedLimit{}, p.err
	}
	if strings.HasPrefix(path, "/api/login") {
		return domain.ResolvedLimit{PolicyID: "free", RuleID: loginRule, Capacity: 2, WindowSeconds: 60, RefillRate: 1.0 / 3600}, nil
	}
	return domain.ResolvedLimit{PolicyID: "free", Capacity: p.capacity, WindowSeconds: 60, RefillRate: 1.0 / 3600}, nil
}

func keyed(clientID string) domain.Identity {
	return domain.Identity{Kind: domain.IdentityAPIKey, ClientID: clientID, KeyPrefix: "rg_cus_x"}
}

func byIP(addr string) domain.Identity {
	return domain.Identity{Kind: domain.IdentityIP, IP: netip.MustParseAddr(addr)}
}

func check(t *testing.T, s *limiter.Service, id domain.Identity, path string) limiter.Decision {
	t.Helper()
	d, err := s.Check(t.Context(), id, "POST", path)
	if err != nil {
		t.Fatalf("Check(%s): %v", path, err)
	}
	return d
}

func TestServiceTakesFromTheResolvedLimit(t *testing.T) {
	s := limiter.New(&plan{capacity: 3}, limiter.NewMemory(nil))
	for i := range 3 {
		if d := check(t, s, keyed(clientA), "/api/products"); !d.Allowed || d.Limit != 3 || d.Remaining != 2-i {
			t.Fatalf("take %d = %+v, want allowed from the policy's 3", i+1, d)
		}
	}
	if d := check(t, s, keyed(clientA), "/api/products"); d.Allowed {
		t.Errorf("take 4 = %+v, want refused", d)
	}
}

func TestServiceGivesEachScopeAndCallerItsOwnBucket(t *testing.T) {
	s := limiter.New(&plan{capacity: 3}, limiter.NewMemory(nil))

	// Draining client A's login rule leaves its policy bucket, and client
	// B's login rule, untouched.
	check(t, s, keyed(clientA), "/api/login")
	check(t, s, keyed(clientA), "/api/login")
	if d := check(t, s, keyed(clientA), "/api/login"); d.Allowed {
		t.Fatalf("third login = %+v, want refused by the 2-request rule", d)
	}
	if d := check(t, s, keyed(clientA), "/api/products"); !d.Allowed || d.Remaining != 2 {
		t.Errorf("A's policy bucket = %+v, want a fresh one", d)
	}
	if d := check(t, s, keyed(clientB), "/api/login"); !d.Allowed || d.Remaining != 1 {
		t.Errorf("B's login bucket = %+v, want a fresh one", d)
	}
	if d := check(t, s, byIP("203.0.113.7"), "/api/login"); !d.Allowed || d.Remaining != 1 {
		t.Errorf("an IP caller's login bucket = %+v, want a fresh one", d)
	}
}

func TestServiceSharesARulesBucketAcrossItsPaths(t *testing.T) {
	s := limiter.New(&plan{capacity: 3}, limiter.NewMemory(nil))
	check(t, s, keyed(clientA), "/api/login")
	if d := check(t, s, keyed(clientA), "/api/login/sso"); !d.Allowed || d.Remaining != 0 {
		t.Errorf("second path under the same rule = %+v, want the rule's last token", d)
	}
}

func TestServiceKeysIPv6CallersByNetwork(t *testing.T) {
	s := limiter.New(&plan{capacity: 3}, limiter.NewMemory(nil))
	for i, addr := range []string{"2001:db8:1:2::1", "2001:db8:1:2::2", "2001:db8:1:2:ffff::9"} {
		if d := check(t, s, byIP(addr), "/"); d.Remaining != 2-i {
			t.Fatalf("address %s = %+v, want one shared /64 bucket at %d remaining", addr, d, 2-i)
		}
	}
	if d := check(t, s, byIP("2001:db8:1:3::1"), "/"); d.Remaining != 2 {
		t.Errorf("another /64 = %+v, want its own bucket", d)
	}
}

func TestServiceKeepsTheBucketWhenTheLimitChanges(t *testing.T) {
	p := &plan{capacity: 10}
	s := limiter.New(p, limiter.NewMemory(nil))
	for range 8 {
		check(t, s, keyed(clientA), "/")
	}
	// A downgrade to 5 clamps the 2 remaining tokens rather than handing
	// out a fresh bucket of 5 (ADR 0011).
	p.mu.Lock()
	p.capacity = 5
	p.mu.Unlock()
	if d := check(t, s, keyed(clientA), "/"); !d.Allowed || d.Limit != 5 || d.Remaining != 1 {
		t.Errorf("after the downgrade = %+v, want the old bucket's last-but-one token", d)
	}
}

func TestServicePeekTakesNothing(t *testing.T) {
	s := limiter.New(&plan{capacity: 3}, limiter.NewMemory(nil))
	check(t, s, keyed(clientA), "/")
	for range 3 {
		d, err := s.Peek(t.Context(), keyed(clientA), "GET", "/")
		if err != nil {
			t.Fatal(err)
		}
		if !d.Allowed || d.Remaining != 2 {
			t.Errorf("Peek = %+v, want 2 remaining every time", d)
		}
	}
}

func TestServicePassesNoPolicyThrough(t *testing.T) {
	s := limiter.New(&plan{err: domain.ErrNoPolicy}, limiter.NewMemory(nil))
	if _, err := s.Check(t.Context(), byIP("203.0.113.7"), "GET", "/"); !errors.Is(err, domain.ErrNoPolicy) {
		t.Errorf("error = %v, want ErrNoPolicy so the caller is refused", err)
	}
}

func TestServiceRefusesAnIdentityWithoutACaller(t *testing.T) {
	p := &plan{capacity: 3}
	s := limiter.New(p, limiter.NewMemory(nil))
	if _, err := s.Check(t.Context(), domain.Identity{Kind: domain.IdentityAPIKey}, "GET", "/"); err == nil {
		t.Error("Check for a keyed identity without a client succeeded, want an error")
	}
	if len(p.asked) != 0 {
		t.Errorf("the resolver was asked %v for an identity with no caller", p.asked)
	}
}

func TestServicePassesTheRequestToTheResolver(t *testing.T) {
	p := &plan{capacity: 3}
	s := limiter.New(p, limiter.NewMemory(nil))
	if _, err := s.Check(t.Context(), byIP("203.0.113.7"), "DELETE", "/api/orders/7"); err != nil {
		t.Fatal(err)
	}
	if len(p.asked) != 1 || p.asked[0] != "ip DELETE /api/orders/7" {
		t.Errorf("resolver was asked %v, want the identity, method and path", p.asked)
	}
}
