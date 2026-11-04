package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// maxKeyAttempts bounds retries after a prefix collision. Prefixes carry 80
// random bits, so even one retry should never be needed.
const maxKeyAttempts = 3

// Store persists clients. Registration writes the client and its
// first key together so neither can exist without the other.
type Store interface {
	CreateWithKey(ctx context.Context, name string, key domain.NewAPIKey) (domain.Client, domain.APIKey, error)
	Get(ctx context.Context, id string) (domain.Client, error)
	List(ctx context.Context, page domain.Page) ([]domain.Client, error)
}

// KeyStore persists API keys. Create must enforce maxActive atomically.
type KeyStore interface {
	Create(ctx context.Context, clientID string, key domain.NewAPIKey, maxActive int) (domain.APIKey, error)
	List(ctx context.Context, clientID string) ([]domain.APIKey, error)
	Revoke(ctx context.Context, clientID, keyID string) (domain.APIKey, error)
}

// Service registers clients and manages the lifecycle of their keys.
type Service struct {
	clients Store
	keys    KeyStore
}

// New returns a Service on the given stores.
func New(clients Store, keys KeyStore) *Service {
	return &Service{clients: clients, keys: keys}
}

// Register creates a client and issues its first key. The returned
// IssuedKey holds the only copy of the full key there will ever be.
func (s *Service) Register(ctx context.Context, name string) (domain.Client, domain.IssuedKey, error) {
	for range maxKeyAttempts {
		plaintext, nk := newStorableKey(name)
		c, k, err := s.clients.CreateWithKey(ctx, name, nk)
		if errors.Is(err, domain.ErrAPIKeyPrefixTaken) {
			continue
		}
		if err != nil {
			return domain.Client{}, domain.IssuedKey{}, fmt.Errorf("registering client: %w", err)
		}
		return c, domain.IssuedKey{Key: k, Plaintext: plaintext}, nil
	}
	return domain.Client{}, domain.IssuedKey{}, fmt.Errorf("registering client: %w after %d attempts", domain.ErrAPIKeyPrefixTaken, maxKeyAttempts)
}

// Get returns one client, or domain.ErrClientNotFound.
func (s *Service) Get(ctx context.Context, id string) (domain.Client, error) {
	c, err := s.clients.Get(ctx, id)
	if err != nil {
		return domain.Client{}, fmt.Errorf("getting client: %w", err)
	}
	return c, nil
}

// List returns one page of clients in registration order.
func (s *Service) List(ctx context.Context, page domain.Page) ([]domain.Client, error) {
	cs, err := s.clients.List(ctx, page)
	if err != nil {
		return nil, fmt.Errorf("listing clients: %w", err)
	}
	return cs, nil
}

// IssueKey gives the client another key, for rotation: issue the new key,
// move callers onto it, then revoke the old one. A client already holding
// domain.MaxActiveKeys active keys gets domain.ErrActiveKeyLimit.
func (s *Service) IssueKey(ctx context.Context, clientID string) (domain.IssuedKey, error) {
	c, err := s.clients.Get(ctx, clientID)
	if err != nil {
		return domain.IssuedKey{}, fmt.Errorf("issuing key: %w", err)
	}
	for range maxKeyAttempts {
		plaintext, nk := newStorableKey(c.Name)
		k, err := s.keys.Create(ctx, c.ID, nk, domain.MaxActiveKeys)
		if errors.Is(err, domain.ErrAPIKeyPrefixTaken) {
			continue
		}
		if err != nil {
			return domain.IssuedKey{}, fmt.Errorf("issuing key: %w", err)
		}
		return domain.IssuedKey{Key: k, Plaintext: plaintext}, nil
	}
	return domain.IssuedKey{}, fmt.Errorf("issuing key: %w after %d attempts", domain.ErrAPIKeyPrefixTaken, maxKeyAttempts)
}

// ListKeys returns every key the client has held, revoked ones included,
// oldest first. An unknown client is domain.ErrClientNotFound rather than
// an empty list.
func (s *Service) ListKeys(ctx context.Context, clientID string) ([]domain.APIKey, error) {
	if _, err := s.clients.Get(ctx, clientID); err != nil {
		return nil, fmt.Errorf("listing keys: %w", err)
	}
	keys, err := s.keys.List(ctx, clientID)
	if err != nil {
		return nil, fmt.Errorf("listing keys: %w", err)
	}
	return keys, nil
}

// RevokeKey revokes one of the client's keys; requests using it fail from
// then on. Revoking an already revoked key succeeds and changes nothing.
func (s *Service) RevokeKey(ctx context.Context, clientID, keyID string) (domain.APIKey, error) {
	k, err := s.keys.Revoke(ctx, clientID, keyID)
	if err != nil {
		return domain.APIKey{}, fmt.Errorf("revoking key: %w", err)
	}
	return k, nil
}

// newStorableKey generates a key and returns it together with the form
// storage receives: the prefix and digest, never the key itself.
func newStorableKey(clientName string) (string, domain.NewAPIKey) {
	plaintext, prefix := generateKey(clientName)
	return plaintext, domain.NewAPIKey{Prefix: prefix, Hash: hashKey(plaintext)}
}
