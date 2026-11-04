package client_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/client"
	"github.com/Rimaz17/RateGuard/internal/domain"
)

// The repositories are stubbed: these tests cover only what the service adds
// on top of them. Registration and key handling against a real database are
// covered in test/integration.

type stubClientStore struct{}

func (stubClientStore) CreateWithKey(context.Context, string, domain.NewAPIKey) (domain.Client, domain.APIKey, error) {
	return domain.Client{}, domain.APIKey{}, errors.New("not used")
}

func (stubClientStore) Get(_ context.Context, id string) (domain.Client, error) {
	return domain.Client{ID: id, Name: "Customer App", Status: domain.ClientActive}, nil
}

func (stubClientStore) List(context.Context, domain.Page) ([]domain.Client, error) {
	return nil, nil
}

type stubKeyStore struct {
	revokeErr error
}

func (stubKeyStore) Create(_ context.Context, clientID string, nk domain.NewAPIKey, _ int) (domain.APIKey, error) {
	return domain.APIKey{ID: "key-2", ClientID: clientID, Prefix: nk.Prefix}, nil
}

func (stubKeyStore) List(context.Context, string) ([]domain.APIKey, error) {
	return nil, nil
}

func (s stubKeyStore) Revoke(_ context.Context, clientID, keyID string) (domain.APIKey, error) {
	if s.revokeErr != nil {
		return domain.APIKey{}, s.revokeErr
	}
	return domain.APIKey{ID: keyID, ClientID: clientID}, nil
}

type recordingNotifier struct {
	clients []string
}

func (n *recordingNotifier) NotifyClient(_ context.Context, clientID string) {
	n.clients = append(n.clients, clientID)
}

func TestRevokeKeyAnnouncesTheClient(t *testing.T) {
	notices := &recordingNotifier{}
	svc := client.New(stubClientStore{}, stubKeyStore{}, notices)

	if _, err := svc.RevokeKey(t.Context(), clientA, "key-1"); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	if !slices.Equal(notices.clients, []string{clientA}) {
		t.Errorf("notices = %v, want [%s]: other instances would keep the key cached", notices.clients, clientA)
	}
}

func TestFailedRevokeAnnouncesNothing(t *testing.T) {
	notices := &recordingNotifier{}
	svc := client.New(stubClientStore{}, stubKeyStore{revokeErr: domain.ErrAPIKeyNotFound}, notices)

	if _, err := svc.RevokeKey(t.Context(), clientA, "key-1"); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatalf("RevokeKey error = %v, want ErrAPIKeyNotFound", err)
	}
	if len(notices.clients) != 0 {
		t.Errorf("notices = %v, want none for a write that did not happen", notices.clients)
	}
}

func TestIssueKeyAnnouncesNothing(t *testing.T) {
	notices := &recordingNotifier{}
	svc := client.New(stubClientStore{}, stubKeyStore{}, notices)

	// A new key cannot be in any cache, so issuing one needs no notice.
	if _, err := svc.IssueKey(t.Context(), clientA); err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	if len(notices.clients) != 0 {
		t.Errorf("notices = %v, want none", notices.clients)
	}
}
