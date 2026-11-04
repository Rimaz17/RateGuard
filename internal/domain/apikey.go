package domain

import (
	"log/slog"
	"net/netip"
	"time"
)

// MaxActiveKeys is how many unrevoked API keys a client may hold at once.
// Two lets rotation overlap: issue the new key, move callers, revoke the
// old one (ADR 0006).
const MaxActiveKeys = 2

// APIKey describes a stored key. It carries the public prefix only: the
// full key is never stored, and its hash never leaves the storage layer
// except for authentication.
type APIKey struct {
	ID       string
	ClientID string

	// Prefix is the public part of the key, such as rg_cus_7hq2mfx4kd5ta3wb.
	// It identifies the key in logs and in the admin API, and is safe to
	// show.
	Prefix string

	CreatedAt time.Time

	// RevokedAt is the zero time while the key is active.
	RevokedAt time.Time
}

// Active reports whether the key has not been revoked.
func (k APIKey) Active() bool { return k.RevokedAt.IsZero() }

// NewAPIKey is a freshly generated key as storage receives it: the public
// prefix and the SHA-256 digest of the full key, never the key itself.
type NewAPIKey struct {
	Prefix string
	Hash   []byte
}

// IssuedKey is a key at the one moment its full value exists outside the
// caller's hands: right after creation, on its way into the response. It
// prints and logs as its prefix only.
type IssuedKey struct {
	Key APIKey

	// Plaintext is the full key. It is returned to the client exactly once
	// and must never be logged or stored.
	Plaintext string
}

// String implements fmt.Stringer so %v cannot leak the plaintext.
func (k IssuedKey) String() string { return k.Key.Prefix + "_[REDACTED]" }

// GoString implements fmt.GoStringer so %#v cannot leak the plaintext.
func (k IssuedKey) GoString() string { return k.String() }

// LogValue implements slog.LogValuer so logging cannot leak the plaintext.
func (k IssuedKey) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", k.Key.ID), slog.String("key_prefix", k.Key.Prefix))
}

// StoredKey is what authentication needs to check a presented key: the
// digest to compare, whether the key still counts, and its client's state.
type StoredKey struct {
	KeyID        string
	ClientID     string
	Prefix       string
	Hash         []byte
	Revoked      bool
	ClientStatus ClientStatus
}

// IdentityKind says how a caller was identified.
type IdentityKind string

// Identity kinds.
const (
	// IdentityAPIKey is a caller that presented a valid API key.
	IdentityAPIKey IdentityKind = "api_key"

	// IdentityIP is a caller with no API key, known only by its address.
	IdentityIP IdentityKind = "ip"
)

// Identity is who a request comes from, as rate limiting sees it.
type Identity struct {
	Kind IdentityKind

	// ClientID and KeyPrefix are set for IdentityAPIKey.
	ClientID  string
	KeyPrefix string

	// IP is set for IdentityIP.
	IP netip.Addr
}
