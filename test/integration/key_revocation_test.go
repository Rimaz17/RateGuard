//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"
)

// statusVia sends one protected request to the instance's app with key as
// its API key and returns the status code.
func statusVia(t *testing.T, in *instance, key string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, in.app.URL+"/api/products", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := in.app.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/products: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestRevokedKeyStopsWorkingOnEveryInstance checks ADR 0012: both instances
// cache API keys for an hour, so only the revocation notice can explain
// either of them refusing the key within the test.
func TestRevokedKeyStopsWorkingOnEveryInstance(t *testing.T) {
	pool := migratedPool(t)
	_, redisURL := startRedis(t.Context(), t)
	a := newInstance(t, pool, redisURL)
	b := newInstance(t, pool, redisURL)
	setUpPlans(t, a.admin)

	reg := registerViaAPI(t, a.admin, "Customer App")
	// A second key keeps the client usable: only the revoked one must fail.
	second := adminJSON(t, a.admin, http.MethodPost, "/v1/clients/"+reg.Client.ID+"/keys", "", http.StatusCreated)
	secondKey, _ := second["key"].(string)

	// Warm both caches with both keys.
	for _, in := range []*instance{a, b} {
		for _, key := range []string{reg.APIKey.Key, secondKey} {
			if code := statusVia(t, in, key); code != http.StatusOK {
				t.Fatalf("before revocation a valid key got %d, want 200", code)
			}
		}
	}

	start := time.Now()
	adminJSON(t, a.admin, http.MethodDelete, "/v1/clients/"+reg.Client.ID+"/keys/"+reg.APIKey.ID, "", http.StatusNoContent)
	// The instance that took the write refuses the key before it answers.
	if code := statusVia(t, a, reg.APIKey.Key); code != http.StatusUnauthorized {
		t.Errorf("instance A accepted the revoked key with %d right after revoking it", code)
	}

	const budget = time.Second // generous for CI; the logged figure is the result
	code := statusVia(t, b, reg.APIKey.Key)
	for code != http.StatusUnauthorized && time.Since(start) < budget {
		time.Sleep(time.Millisecond)
		code = statusVia(t, b, reg.APIKey.Key)
	}
	if code != http.StatusUnauthorized {
		t.Fatalf("instance B still accepts the revoked key after %v (status %d)", time.Since(start), code)
	}
	t.Logf("revoked key refused by instance B %v after the admin call started", time.Since(start).Round(time.Microsecond))

	for _, in := range []*instance{a, b} {
		if code := statusVia(t, in, secondKey); code != http.StatusOK {
			t.Errorf("the client's other key got %d after the revocation, want 200", code)
		}
	}
}
