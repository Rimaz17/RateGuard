package domain_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

func TestIssuedKeyNeverRendersPlaintext(t *testing.T) {
	const secret = "rg_cus_aaaaaaaaaaaaaaaa_supersecretpartofthekeythatmustneverleakanywhere00"
	k := domain.IssuedKey{
		Key:       domain.APIKey{ID: "key-1", Prefix: "rg_cus_aaaaaaaaaaaaaaaa"},
		Plaintext: secret,
	}

	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("issued", slog.Any("key", k))

	for name, out := range map[string]string{
		"%v":   fmt.Sprintf("%v", k),
		"%+v":  fmt.Sprintf("%+v", k),
		"%#v":  fmt.Sprintf("%#v", k),
		"slog": logs.String(),
	} {
		if strings.Contains(out, "supersecret") {
			t.Errorf("%s rendering leaks the key: %s", name, out)
		}
		if !strings.Contains(out, "rg_cus_aaaaaaaaaaaaaaaa") {
			t.Errorf("%s rendering = %s, want it to show the prefix", name, out)
		}
	}
}

func TestAPIKeyActive(t *testing.T) {
	if !(domain.APIKey{}).Active() {
		t.Error("a key with no revocation time should be active")
	}
	if (domain.APIKey{RevokedAt: time.Now()}).Active() {
		t.Error("a revoked key should not be active")
	}
}
