package httptransport

// Internal tests: decodeJSON, validateRequest and writeServiceError are
// shared plumbing whose edge cases are clearer tested directly than
// through every handler that uses them.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

type sample struct {
	Name string `json:"name" validate:"required,max=5"`
}

func TestDecodeJSON(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int // 0 means success
		wantMsg     string
	}{
		{"valid", "application/json", `{"name":"ok"}`, 0, ""},
		{"no content type", "", `{"name":"ok"}`, 0, ""},
		{"content type with charset", "application/json; charset=utf-8", `{"name":"ok"}`, 0, ""},
		{"whitespace after object", "", "{\"name\":\"ok\"}\n  ", 0, ""},
		{"wrong content type", "text/plain", `{"name":"ok"}`, http.StatusUnsupportedMediaType, "Content-Type"},
		{"empty body", "", ``, http.StatusBadRequest, "required"},
		{"syntax error", "", `{"name":`, http.StatusBadRequest, "not valid JSON"},
		{"bad token", "", `{name}`, http.StatusBadRequest, "not valid JSON"},
		{"wrong type", "", `{"name":42}`, http.StatusBadRequest, `field "name" has the wrong type`},
		{"array instead of object", "", `["x"]`, http.StatusBadRequest, "must be a JSON object"},
		{"unknown field", "", `{"name":"ok","admin":true}`, http.StatusBadRequest, `unknown field "admin"`},
		{"two objects", "", `{"name":"a"}{"name":"b"}`, http.StatusBadRequest, "single JSON object"},
		{"trailing garbage", "", `{"name":"a"} x`, http.StatusBadRequest, "single JSON object"},
		{"too large", "", `{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, http.StatusRequestEntityTooLarge, "at most"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			var dst sample
			err := decodeJSON(httptest.NewRecorder(), req, &dst)
			if tt.wantStatus == 0 {
				if err != nil {
					t.Fatalf("decodeJSON: %v", err)
				}
				if dst.Name != "ok" {
					t.Errorf("Name = %q, want ok", dst.Name)
				}
				return
			}
			if err == nil {
				t.Fatalf("decodeJSON: want %d, got nil", tt.wantStatus)
			}
			if err.status != tt.wantStatus || !strings.Contains(err.message, tt.wantMsg) {
				t.Errorf("decodeJSON = %d %q, want %d containing %q", err.status, err.message, tt.wantStatus, tt.wantMsg)
			}
		})
	}
}

func TestValidateRequest(t *testing.T) {
	v := newValidator()
	tests := []struct {
		in   sample
		want string
	}{
		{sample{Name: "fine"}, ""},
		{sample{Name: ""}, "name is required"},
		{sample{Name: "toolong"}, "name must be at most 5 characters"},
		{sample{Name: "ééééé"}, ""}, // max counts characters, not bytes
	}
	for _, tt := range tests {
		err := validateRequest(v, &tt.in)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("validateRequest(%+v) = %v, want nil", tt.in, err)
		case tt.want != "" && (err == nil || err.message != tt.want || err.status != http.StatusBadRequest):
			t.Errorf("validateRequest(%+v) = %v, want 400 %q", tt.in, err, tt.want)
		}
	}
}

type numericSample struct {
	Count    int      `json:"count" validate:"min=1,max=10"`
	Rate     *float64 `json:"rate" validate:"omitempty,gt=0,lte=5"`
	Optional *int     `json:"optional" validate:"omitempty,gte=2"`
	Short    string   `json:"short" validate:"omitempty,min=2"`
}

func TestValidateRequestWordsNumbersAsValues(t *testing.T) {
	v := newValidator()
	f := func(x float64) *float64 { return &x }
	i := func(x int) *int { return &x }
	tests := []struct {
		in   numericSample
		want string
	}{
		{numericSample{Count: 1}, ""},
		{numericSample{Count: 10, Rate: f(5), Optional: i(2), Short: "ab"}, ""},
		{numericSample{Count: 0}, "count must be at least 1"},
		{numericSample{Count: 11}, "count must be at most 10"},
		{numericSample{Count: 1, Rate: f(0)}, "rate must be greater than 0"},
		{numericSample{Count: 1, Rate: f(-1)}, "rate must be greater than 0"},
		{numericSample{Count: 1, Rate: f(5.5)}, "rate must be at most 5"},
		{numericSample{Count: 1, Optional: i(1)}, "optional must be at least 2"},
		{numericSample{Count: 1, Short: "a"}, "short must be at least 2 characters"},
	}
	for _, tt := range tests {
		err := validateRequest(v, &tt.in)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("validateRequest(%+v) = %v, want nil", tt.in, err)
		case tt.want != "" && (err == nil || err.message != tt.want || err.status != http.StatusBadRequest):
			t.Errorf("validateRequest(%+v) = %v, want 400 %q", tt.in, err, tt.want)
		}
	}
}

func TestWriteServiceError(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
		wantMsg    string
	}{
		{fmt.Errorf("getting client: %w", domain.ErrClientNotFound), http.StatusNotFound, "client not found"},
		{fmt.Errorf("revoking key: %w", domain.ErrAPIKeyNotFound), http.StatusNotFound, "API key not found"},
		{fmt.Errorf("issuing key: %w", domain.ErrActiveKeyLimit), http.StatusConflict, "revoke one before issuing another"},
		{fmt.Errorf("getting policy: %w", domain.ErrPolicyNotFound), http.StatusNotFound, "policy not found"},
		{fmt.Errorf("creating policy: %w", domain.ErrPolicyNameTaken), http.StatusConflict, "another policy already has this name"},
		{fmt.Errorf("assigning policy: %w", domain.ErrPolicyDisabled), http.StatusConflict, "enable it before assigning it"},
		{fmt.Errorf("deleting endpoint rule: %w", domain.ErrEndpointRuleNotFound), http.StatusNotFound, "endpoint rule not found"},
		{fmt.Errorf("creating endpoint rule: %w", domain.ErrEndpointRuleExists), http.StatusConflict, "already has a rule for this method and path pattern"},
		{fmt.Errorf("creating endpoint rule: %w", domain.ErrEndpointRuleLimit), http.StatusConflict, "already has 100 endpoint rules"},
		{fmt.Errorf("creating endpoint rule: %w", &domain.ValidationError{Field: "path_pattern", Reason: "must start with /"}), http.StatusBadRequest, "path_pattern must start with /"},
		{errors.New("selecting client: connection refused to db.internal:5432"), http.StatusInternalServerError, "internal server error"},
	}
	for _, tt := range tests {
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		rec := httptest.NewRecorder()
		writeServiceError(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/clients/x", nil), logger, tt.err)

		var body errorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		if rec.Code != tt.wantStatus || !strings.Contains(body.Error, tt.wantMsg) {
			t.Errorf("%v: got %d %q, want %d containing %q", tt.err, rec.Code, body.Error, tt.wantStatus, tt.wantMsg)
		}
		if strings.Contains(rec.Body.String(), "db.internal") || strings.Contains(rec.Body.String(), ": ") {
			t.Errorf("%v: body leaks internal error text: %s", tt.err, rec.Body.String())
		}
		if tt.wantStatus == http.StatusInternalServerError && !strings.Contains(logs.String(), "db.internal") {
			t.Errorf("a 500 should log the underlying error, got: %s", logs.String())
		}
	}
}
