package client

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// KeyFinder looks up a stored key by its public prefix, returning
// domain.ErrAPIKeyNotFound when there is none.
type KeyFinder interface {
	FindByPrefix(ctx context.Context, prefix string) (domain.StoredKey, error)
}

// Authenticator checks presented API keys.
type Authenticator struct {
	keys KeyFinder
}

// NewAuthenticator returns an Authenticator that looks keys up in keys.
func NewAuthenticator(keys KeyFinder) *Authenticator {
	return &Authenticator{keys: keys}
}

// Authenticate resolves a presented key to its client's identity.
//
// A malformed, unknown, wrong or revoked key is domain.ErrInvalidAPIKey,
// with nothing to tell those cases apart. A valid key whose client is
// disabled is domain.ErrClientDisabled. Errors never contain the key;
// at most its public prefix.
func (a *Authenticator) Authenticate(ctx context.Context, key string) (domain.Identity, error) {
	prefix, ok := parseKey(key)
	if !ok {
		return domain.Identity{}, domain.ErrInvalidAPIKey
	}

	stored, err := a.keys.FindByPrefix(ctx, prefix)
	if errors.Is(err, domain.ErrAPIKeyNotFound) {
		return domain.Identity{}, domain.ErrInvalidAPIKey
	}
	if err != nil {
		return domain.Identity{}, fmt.Errorf("looking up API key %s: %w", prefix, err)
	}

	// Constant time, so response timing reveals nothing about how much of
	// the digest matched (ADR 0006).
	if subtle.ConstantTimeCompare(hashKey(key), stored.Hash) != 1 {
		return domain.Identity{}, domain.ErrInvalidAPIKey
	}
	// Revocation and status are checked only after the digest matches, so
	// they are disclosed only to a holder of the real key.
	if stored.Revoked {
		return domain.Identity{}, domain.ErrInvalidAPIKey
	}
	if stored.ClientStatus != domain.ClientActive {
		return domain.Identity{}, domain.ErrClientDisabled
	}
	return domain.Identity{Kind: domain.IdentityAPIKey, ClientID: stored.ClientID, KeyPrefix: stored.Prefix}, nil
}
