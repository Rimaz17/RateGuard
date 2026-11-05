package client_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/client"
	"github.com/Rimaz17/RateGuard/internal/domain"
)

// stubKeys stands in for the api_keys repository; the real one is covered
// against Postgres in test/integration.
type stubKeys struct {
	mu    sync.Mutex
	keys  map[string]domain.StoredKey
	reads map[string]int
	err   error
}

func newStubKeys(keys ...domain.StoredKey) *stubKeys {
	s := &stubKeys{keys: map[string]domain.StoredKey{}, reads: map[string]int{}}
	for _, k := range keys {
		s.keys[k.Prefix] = k
	}
	return s
}

func (s *stubKeys) FindByPrefix(_ context.Context, prefix string) (domain.StoredKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads[prefix]++
	if s.err != nil {
		return domain.StoredKey{}, s.err
	}
	k, ok := s.keys[prefix]
	if !ok {
		return domain.StoredKey{}, domain.ErrAPIKeyNotFound
	}
	return k, nil
}

func (s *stubKeys) revoke(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.keys[prefix]
	k.Revoked = true
	s.keys[prefix] = k
}

func (s *stubKeys) readsOf(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads[prefix]
}

const (
	clientA = "0193f2a0-0000-7000-8000-00000000000a"
	clientB = "0193f2a0-0000-7000-8000-00000000000b"
)

func storedKey(prefix, clientID string) domain.StoredKey {
	return domain.StoredKey{KeyID: prefix + "-id", ClientID: clientID, Prefix: prefix, Hash: []byte("digest-" + prefix), ClientStatus: domain.ClientActive}
}

func find(t *testing.T, c *client.KeyCache, prefix string) domain.StoredKey {
	t.Helper()
	k, err := c.FindByPrefix(t.Context(), prefix)
	if err != nil {
		t.Fatalf("FindByPrefix(%s): %v", prefix, err)
	}
	return k
}

func TestKeyCacheServesRepeatLookupsFromMemory(t *testing.T) {
	keys := newStubKeys(storedKey("rg_cus_a1", clientA))
	c := client.NewKeyCache(keys, time.Hour)

	for range 5 {
		if k := find(t, c, "rg_cus_a1"); k.ClientID != clientA {
			t.Fatalf("FindByPrefix = client %s, want %s", k.ClientID, clientA)
		}
	}
	if n := keys.readsOf("rg_cus_a1"); n != 1 {
		t.Errorf("reads = %d, want 1", n)
	}
}

func TestKeyCacheInvalidateClientDropsOnlyThatClientsKeys(t *testing.T) {
	keys := newStubKeys(storedKey("rg_cus_a1", clientA), storedKey("rg_cus_a2", clientA), storedKey("rg_mer_b1", clientB))
	c := client.NewKeyCache(keys, time.Hour)
	for _, p := range []string{"rg_cus_a1", "rg_cus_a2", "rg_mer_b1"} {
		find(t, c, p)
	}

	keys.revoke("rg_cus_a1")
	c.InvalidateClient(clientA)

	if k := find(t, c, "rg_cus_a1"); !k.Revoked {
		t.Error("after InvalidateClient the revoked key is still served as active")
	}
	find(t, c, "rg_cus_a2")
	find(t, c, "rg_mer_b1")
	for prefix, want := range map[string]int{"rg_cus_a1": 2, "rg_cus_a2": 2, "rg_mer_b1": 1} {
		if n := keys.readsOf(prefix); n != want {
			t.Errorf("reads of %s = %d, want %d", prefix, n, want)
		}
	}
}

func TestKeyCacheFlushDropsEverything(t *testing.T) {
	keys := newStubKeys(storedKey("rg_cus_a1", clientA), storedKey("rg_mer_b1", clientB))
	c := client.NewKeyCache(keys, time.Hour)
	find(t, c, "rg_cus_a1")
	find(t, c, "rg_mer_b1")

	c.Flush()
	find(t, c, "rg_cus_a1")
	find(t, c, "rg_mer_b1")
	if a, b := keys.readsOf("rg_cus_a1"), keys.readsOf("rg_mer_b1"); a != 2 || b != 2 {
		t.Errorf("reads after Flush = %d, %d; want 2, 2", a, b)
	}
}

// Unknown prefixes are not cached (ADR 0012): otherwise made-up keys would
// grow the cache without bound.
func TestKeyCacheDoesNotCacheUnknownPrefixes(t *testing.T) {
	keys := newStubKeys()
	c := client.NewKeyCache(keys, time.Hour)

	for range 3 {
		if _, err := c.FindByPrefix(t.Context(), "rg_xyz_unknown"); !errors.Is(err, domain.ErrAPIKeyNotFound) {
			t.Fatalf("FindByPrefix error = %v, want ErrAPIKeyNotFound", err)
		}
	}
	if n := keys.readsOf("rg_xyz_unknown"); n != 3 {
		t.Errorf("reads = %d, want 3: a missing key must be looked up each time", n)
	}
}

func TestKeyCacheDoesNotCacheErrors(t *testing.T) {
	keys := newStubKeys(storedKey("rg_cus_a1", clientA))
	keys.err = errors.New("database down")
	c := client.NewKeyCache(keys, time.Hour)

	if _, err := c.FindByPrefix(t.Context(), "rg_cus_a1"); err == nil {
		t.Fatal("FindByPrefix with the database down: want an error")
	}
	keys.mu.Lock()
	keys.err = nil
	keys.mu.Unlock()
	find(t, c, "rg_cus_a1")
}
