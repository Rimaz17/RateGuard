package httptransport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

const adminToken = "test-admin-token-0123456789abcdef"

func TestAdminAuth(t *testing.T) {
	tests := []struct {
		name   string
		header []string // Authorization values; nil sends none
		want   int
	}{
		{"correct token", []string{"Bearer " + adminToken}, http.StatusOK},
		{"scheme is case-insensitive", []string{"bearer " + adminToken}, http.StatusOK},
		{"surrounding space is ignored", []string{"Bearer   " + adminToken + " "}, http.StatusOK},
		{"no header", nil, http.StatusUnauthorized},
		{"empty header", []string{""}, http.StatusUnauthorized},
		{"wrong token", []string{"Bearer " + strings.Repeat("x", len(adminToken))}, http.StatusUnauthorized},
		{"token prefix only", []string{"Bearer " + adminToken[:10]}, http.StatusUnauthorized},
		{"token with extra suffix", []string{"Bearer " + adminToken + "x"}, http.StatusUnauthorized},
		{"wrong scheme", []string{"Basic " + adminToken}, http.StatusUnauthorized},
		{"scheme without token", []string{"Bearer"}, http.StatusUnauthorized},
		{"bearer with blank token", []string{"Bearer    "}, http.StatusUnauthorized},
		{"two headers", []string{"Bearer " + adminToken, "Bearer " + adminToken}, http.StatusUnauthorized},
		{"oversized header", []string{"Bearer " + adminToken + strings.Repeat(" ", 600)}, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := httptransport.RequestID(httptransport.AdminAuth(adminToken)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/clients", nil)
			for _, v := range tt.header {
				req.Header.Add("Authorization", v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want != http.StatusUnauthorized {
				return
			}
			if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
				t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
			}
			var body struct {
				Error     string `json:"error"`
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding body: %v", err)
			}
			if body.Error == "" || body.RequestID != rec.Header().Get(httptransport.RequestIDHeader) {
				t.Errorf("body = %+v, want a message and the request ID", body)
			}
			if strings.Contains(rec.Body.String(), adminToken) {
				t.Error("the 401 body echoes the admin token")
			}
		})
	}
}
