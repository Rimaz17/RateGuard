package api_test

import (
	"strings"
	"testing"

	"github.com/Rimaz17/RateGuard/api"
)

func TestOpenAPIIsTheContract(t *testing.T) {
	spec := api.OpenAPI()
	if !strings.HasPrefix(spec, "openapi: 3.1") {
		t.Fatalf("embedded document starts %q, want an OpenAPI 3.1 document", spec[:min(len(spec), 40)])
	}
	// The decision endpoints are the contract integrators need most.
	for _, path := range []string{"\n  /v1/check:\n", "\n  /v1/peek:\n", "\n  /openapi.yaml:\n"} {
		if !strings.Contains(spec, path) {
			t.Errorf("embedded document has no %s path", strings.TrimSpace(path))
		}
	}
}
