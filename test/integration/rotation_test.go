//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/client"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// generation is a key a caller may be using: 1 is the original, 2 the
// rotated one.
type generation struct {
	n   int64
	key string
}

// newProtectedApp serves a stand-in for a decision endpoint behind the
// real Identify middleware and authenticator.
func newProtectedApp(t *testing.T, auth httptransport.Authenticator) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(httptransport.Identify(slog.New(slog.DiscardHandler), auth, nil)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := httptransport.IdentityFromContext(r.Context())
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"client_id": id.ClientID, "key_prefix": id.KeyPrefix})
		})))
	t.Cleanup(srv.Close)
	return srv
}

func callWithKey(t *testing.T, srv *httptest.Server, key string) (int, error) {
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := srv.Client().Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// TestRotationWithZeroFailedRequests is the second half of phase 2's
// done-when. Callers send requests continuously while the client rotates
// the way ADR 0006 prescribes: issue a new key, move every caller onto it,
// then revoke the old key. Not one request may fail.
func TestRotationWithZeroFailedRequests(t *testing.T) {
	pool := migratedPool(t)
	admin := newAdminAPI(t, pool)
	app := newProtectedApp(t, client.NewAuthenticator(postgres.NewAPIKeys(pool)))

	reg := registerViaAPI(t, admin, "Customer App")
	var current atomic.Pointer[generation]
	current.Store(&generation{n: 1, key: reg.APIKey.Key})

	const callers = 6
	var (
		stop     = make(chan struct{})
		wg       sync.WaitGroup
		total    atomic.Int64
		perGen   [3]atomic.Int64
		failures = make(chan string, 64)
		// latest[i] is the generation caller i used for its last
		// successful request.
		latest [callers]atomic.Int64
	)
	for i := range callers {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				g := current.Load()
				code, err := callWithKey(t, app, g.key)
				total.Add(1)
				if err != nil || code != http.StatusOK {
					select {
					case failures <- fmt.Sprintf("caller %d, key generation %d: status %d, error %v", i, g.n, code, err):
					default:
					}
					continue
				}
				perGen[g.n].Add(1)
				latest[i].Store(g.n)
			}
		})
	}

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				close(stop)
				wg.Wait()
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	allOn := func(n int64) func() bool {
		return func() bool {
			for i := range callers {
				if latest[i].Load() != n {
					return false
				}
			}
			return true
		}
	}

	waitFor("every caller to succeed on the original key", allOn(1))

	// 1. Issue the new key. Both keys now work.
	code, body := adminCall(t, admin, http.MethodPost, "/v1/clients/"+reg.Client.ID+"/keys", "")
	if code != http.StatusCreated {
		close(stop)
		wg.Wait()
		t.Fatalf("issuing the new key = %d %s", code, body)
	}
	var issued struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &issued); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	// 2. Move every caller onto it, and wait until each has used it.
	current.Store(&generation{n: 2, key: issued.Key})
	waitFor("every caller to move to the new key", allOn(2))

	// 3. Revoke the old key, then keep the traffic going a while longer.
	if code, body := adminCall(t, admin, http.MethodDelete, "/v1/clients/"+reg.Client.ID+"/keys/"+reg.APIKey.ID, ""); code != http.StatusNoContent {
		close(stop)
		wg.Wait()
		t.Fatalf("revoking the old key = %d %s", code, body)
	}
	before := perGen[2].Load()
	waitFor("traffic on the new key after the revoke", func() bool { return perGen[2].Load() > before+200 })

	close(stop)
	wg.Wait()
	close(failures)

	for f := range failures {
		t.Errorf("failed request: %s", f)
	}
	t.Logf("%d requests: %d on the original key, %d on the rotated key", total.Load(), perGen[1].Load(), perGen[2].Load())
	if perGen[1].Load() == 0 || perGen[2].Load() == 0 {
		t.Errorf("want traffic on both keys, got %d and %d", perGen[1].Load(), perGen[2].Load())
	}
	if got := perGen[1].Load() + perGen[2].Load(); got != total.Load() {
		t.Errorf("%d of %d requests succeeded", got, total.Load())
	}

	// The rotation really happened: the old key is now refused.
	if code, err := callWithKey(t, app, reg.APIKey.Key); err != nil || code != http.StatusUnauthorized {
		t.Errorf("the revoked key = %d, %v; want 401", code, err)
	}
	if code, err := callWithKey(t, app, issued.Key); err != nil || code != http.StatusOK {
		t.Errorf("the new key = %d, %v; want 200", code, err)
	}
}
