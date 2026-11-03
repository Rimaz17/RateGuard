package client

import (
	"crypto/sha256"
	"regexp"
	"strings"
	"testing"
)

// The key format is an internal detail with no exported constructor, so
// these tests sit inside the package. Everything else in client is tested
// through its exported API.

var keyShape = regexp.MustCompile(`^rg_[a-z0-9]{1,3}_[a-z2-7]{16}_[a-z2-7]{52}$`)

func TestGenerateKeyShape(t *testing.T) {
	key, prefix := generateKey("Customer App")

	if !keyShape.MatchString(key) {
		t.Errorf("key %q does not match %s", key, keyShape)
	}
	if !strings.HasPrefix(key, prefix+"_") {
		t.Errorf("prefix %q is not the start of the key", prefix)
	}
	if !strings.HasPrefix(prefix, "rg_cus_") || len(prefix) != len("rg_cus_")+lookupLen {
		t.Errorf("prefix = %q, want rg_cus_ and 16 lookup characters", prefix)
	}
	if len(key) > maxKeyLen {
		t.Errorf("key is %d characters, longer than maxKeyLen %d", len(key), maxKeyLen)
	}
}

func TestGenerateKeyIsUnique(t *testing.T) {
	keys := make(map[string]bool)
	prefixes := make(map[string]bool)
	for range 10000 {
		key, prefix := generateKey("x")
		if keys[key] || prefixes[prefix] {
			t.Fatalf("duplicate key or prefix after %d keys", len(keys))
		}
		keys[key], prefixes[prefix] = true, true
	}
}

func TestTagFor(t *testing.T) {
	tests := []struct{ name, want string }{
		{"Customer App", "cus"},
		{"Merchant App", "mer"},
		{"ADMIN", "adm"},
		{"42 Labs", "42l"},
		{"  x-y ", "xy"},
		{"Été", "t"},
		{"日本語", "app"},
		{"", "app"},
		{"---", "app"},
	}
	for _, tt := range tests {
		if got := tagFor(tt.name); got != tt.want {
			t.Errorf("tagFor(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestParseKey(t *testing.T) {
	valid, prefix := generateKey("Merchant App")

	if got, ok := parseKey(valid); !ok || got != prefix {
		t.Fatalf("parseKey(generated) = %q, %v; want %q, true", got, ok, prefix)
	}

	lookup := strings.Repeat("a", lookupLen)
	secret := strings.Repeat("b", secretLen)
	invalid := map[string]string{
		"empty":              "",
		"wrong scheme":       "rk_cus_" + lookup + "_" + secret,
		"missing part":       "rg_cus_" + lookup + secret,
		"extra part":         "rg_cus_" + lookup + "_" + secret + "_x",
		"empty tag":          "rg__" + lookup + "_" + secret,
		"long tag":           "rg_cust_" + lookup + "_" + secret,
		"uppercase tag":      "rg_CUS_" + lookup + "_" + secret,
		"short lookup":       "rg_cus_" + lookup[1:] + "_" + secret,
		"short secret":       "rg_cus_" + lookup + "_" + secret[1:],
		"long secret":        "rg_cus_" + lookup + "_" + secret + "b",
		"non-base32 lookup":  "rg_cus_" + lookup[1:] + "1" + "_" + secret,
		"uppercase secret":   "rg_cus_" + lookup + "_" + strings.ToUpper(secret),
		"whitespace":         " rg_cus_" + lookup + "_" + secret,
		"oversized":          strings.Repeat("a", 10000),
		"prefix alone":       prefix,
		"key with padding":   valid + "=",
		"multibyte in tag":   "rg_cü_" + lookup + "_" + secret,
		"bearer still on it": "Bearer " + valid,
	}
	for name, key := range invalid {
		if got, ok := parseKey(key); ok {
			t.Errorf("%s: parseKey(%q) = %q, true; want false", name, key, got)
		}
	}
}

func TestHashKeyIsSHA256(t *testing.T) {
	key, _ := generateKey("x")
	want := sha256.Sum256([]byte(key))
	if got := hashKey(key); string(got) != string(want[:]) || len(got) != 32 {
		t.Errorf("hashKey = %x, want %x", got, want)
	}
}

func TestRedact(t *testing.T) {
	key, prefix := generateKey("Customer App")
	if got := Redact(key); got != prefix {
		t.Errorf("Redact(key) = %q, want the prefix %q", got, prefix)
	}
	for _, s := range []string{"hunter2", "", "rg_cus_secret"} {
		got := Redact(s)
		if got != "[unparseable key]" {
			t.Errorf("Redact(%q) = %q, want the placeholder", s, got)
		}
	}
}
