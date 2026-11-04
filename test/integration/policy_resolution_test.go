//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rimaz17/RateGuard/internal/client"
	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/policy"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// instance is one RateGuard process as main wires it, minus the decision
// endpoint that later phases add: the admin API, a resolver kept in sync
// over Redis pub/sub, and a stand-in protected app that resolves the limit
// for each request it receives.
type instance struct {
	resolver *policy.Resolver
	admin    *httptest.Server
	app      *httptest.Server
}

// newInstance starts an instance on the shared database and Redis. Its
// cache TTL is an hour, so anything it notices within the test came over
// pub/sub, not from expiry.
func newInstance(t *testing.T, pool *pgxpool.Pool, redisURL string) *instance {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	clientStore := postgres.NewClients(pool)
	policyStore := postgres.NewPolicies(pool)
	rdb := newRedisClient(t, redisURL)

	resolver := policy.NewResolver(clientStore, policyStore, policy.WithTTL(time.Hour))
	broadcaster := policy.NewBroadcaster(rdb, resolver, logger)
	policies := policy.New(policyStore, postgres.NewEndpointRules(pool), clientStore, broadcaster)
	clients := client.New(clientStore, postgres.NewAPIKeys(pool), broadcaster)
	runSubscriber(t, rdb, &observed{inner: resolver})

	admin := httptest.NewServer(httptransport.NewRouter(httptransport.Deps{
		Logger:           logger,
		ReadinessTimeout: time.Second,
		AdminToken:       testAdminToken,
		Clients:          clients,
		Keys:             clients,
		Policies:         policies,
		Rules:            policies,
		Assigner:         policies,
	}))
	t.Cleanup(admin.Close)

	auth := client.NewAuthenticator(postgres.NewAPIKeys(pool))
	app := httptest.NewServer(httptransport.Identify(logger, auth, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := httptransport.IdentityFromContext(r.Context())
		limit, err := resolver.Resolve(r.Context(), id, r.Method, r.URL.Path)
		if errors.Is(err, domain.ErrNoPolicy) {
			http.Error(w, "no policy", http.StatusForbidden)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resolvedWire{Policy: limit.PolicyName, Rule: limit.RuleID != "", Capacity: limit.Capacity, Window: limit.WindowSeconds})
	})))
	t.Cleanup(app.Close)

	return &instance{resolver: resolver, admin: admin, app: app}
}

type resolvedWire struct {
	Policy   string `json:"policy"`
	Rule     bool   `json:"rule"`
	Capacity int    `json:"capacity"`
	Window   int    `json:"window"`
}

func (r resolvedWire) String() string {
	return fmt.Sprintf("%s %d/%ds (rule %v)", r.Policy, r.Capacity, r.Window, r.Rule)
}

// resolveVia sends one protected request to the instance's app, with key
// as its API key ("" for none), and returns the limit that applied.
func resolveVia(t *testing.T, in *instance, key, method, path string) resolvedWire {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, in.app.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := in.app.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s = %d %s", method, path, resp.StatusCode, body)
	}
	var out resolvedWire
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return out
}

func adminJSON(t *testing.T, srv *httptest.Server, method, path, body string, want int) map[string]any {
	t.Helper()
	code, resp := adminCall(t, srv, method, path, body)
	if code != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, code, resp, want)
	}
	if len(resp) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("decoding %s: %v", resp, err)
	}
	return out
}

// plan is a policy from the project overview's table, with its override.
type plan struct {
	name                     string
	capacity                 int
	ruleMethod, rulePattern  string
	ruleCapacity, ruleWindow int
	isDefault                bool
}

var overviewPlans = []plan{
	{"Free", 100, "POST", "/api/login", 5, 60, true},
	{"Premium", 1000, "*", "/api/orders/**", 20, 60, false},
	{"Enterprise", 5000, "GET", "/api/products/*", 100, 60, false},
}

// setUpPlans creates the overview's plans through the admin API and
// returns their IDs by name.
func setUpPlans(t *testing.T, admin *httptest.Server) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, p := range overviewPlans {
		created := adminJSON(t, admin, http.MethodPost, "/v1/policies",
			fmt.Sprintf(`{"name":%q,"capacity":%d,"window_seconds":60,"default":%v}`, p.name, p.capacity, p.isDefault), http.StatusCreated)
		id, _ := created["id"].(string)
		ids[p.name] = id
		adminJSON(t, admin, http.MethodPost, "/v1/policies/"+id+"/rules",
			fmt.Sprintf(`{"method":%q,"path_pattern":%q,"capacity":%d,"window_seconds":%d}`, p.ruleMethod, p.rulePattern, p.ruleCapacity, p.ruleWindow), http.StatusCreated)
	}
	return ids
}

// registerOn registers a client through the admin API, assigns it a policy
// (none when policyID is ""), and returns its ID and API key.
func registerOn(t *testing.T, admin *httptest.Server, name, policyID string) (string, string) {
	t.Helper()
	reg := registerViaAPI(t, admin, name)
	if policyID != "" {
		adminJSON(t, admin, http.MethodPut, "/v1/clients/"+reg.Client.ID+"/policy", `{"policy_id":"`+policyID+`"}`, http.StatusOK)
	}
	return reg.Client.ID, reg.APIKey.Key
}

// TestPlansResolvePerRequest is the first half of phase 3's done-when:
// Free, Premium and Enterprise resolve correctly per request. Everything
// is real: the admin API writes Postgres, callers present real API keys to
// the real Identify middleware, and the resolver decides from its cache.
func TestPlansResolvePerRequest(t *testing.T) {
	pool := migratedPool(t)
	_, redisURL := startRedis(t.Context(), t)
	in := newInstance(t, pool, redisURL)

	ids := setUpPlans(t, in.admin)
	_, customerKey := registerOn(t, in.admin, "Customer App", ids["Free"])
	_, merchantKey := registerOn(t, in.admin, "Merchant App", ids["Premium"])
	_, adminKey := registerOn(t, in.admin, "Admin App", ids["Enterprise"])
	_, newcomerKey := registerOn(t, in.admin, "Newcomer App", "")

	tests := []struct {
		caller, key  string
		method, path string
		want         resolvedWire
	}{
		{"free", customerKey, http.MethodPost, "/api/login", resolvedWire{"Free", true, 5, 60}},
		{"free", customerKey, http.MethodPost, "/api/login/", resolvedWire{"Free", true, 5, 60}},
		{"free", customerKey, http.MethodGet, "/api/login", resolvedWire{"Free", false, 100, 60}},
		{"free", customerKey, http.MethodGet, "/api/products/1", resolvedWire{"Free", false, 100, 60}},
		{"premium", merchantKey, http.MethodPut, "/api/orders/7/items", resolvedWire{"Premium", true, 20, 60}},
		{"premium", merchantKey, http.MethodPost, "/api/login", resolvedWire{"Premium", false, 1000, 60}},
		{"enterprise", adminKey, http.MethodGet, "/api/products/42", resolvedWire{"Enterprise", true, 100, 60}},
		{"enterprise", adminKey, http.MethodDelete, "/api/products/42", resolvedWire{"Enterprise", false, 5000, 60}},
		{"enterprise", adminKey, http.MethodGet, "/api/orders", resolvedWire{"Enterprise", false, 5000, 60}},
		{"unassigned client, so default", newcomerKey, http.MethodPost, "/api/login", resolvedWire{"Free", true, 5, 60}},
		{"no key, so default by IP", "", http.MethodGet, "/", resolvedWire{"Free", false, 100, 60}},
	}
	for _, tt := range tests {
		if got := resolveVia(t, in, tt.key, tt.method, tt.path); got != tt.want {
			t.Errorf("%s: %s %s resolved %v, want %v", tt.caller, tt.method, tt.path, got, tt.want)
		}
	}

	// Disabling Premium sends its clients to the default, and disabling
	// the default leaves keyless callers with no policy at all.
	adminJSON(t, in.admin, http.MethodDelete, "/v1/policies/"+ids["Premium"], "", http.StatusNoContent)
	if got := resolveVia(t, in, merchantKey, http.MethodPost, "/api/login"); got != (resolvedWire{"Free", true, 5, 60}) {
		t.Errorf("after disabling Premium its client resolved %v, want the Free default", got)
	}
	adminJSON(t, in.admin, http.MethodDelete, "/v1/policies/"+ids["Free"], "", http.StatusNoContent)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, in.app.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := in.app.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("with the default disabled, a keyless caller got %d, want no policy (403 here)", resp.StatusCode)
	}
}
