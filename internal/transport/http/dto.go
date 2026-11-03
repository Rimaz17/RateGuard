package httptransport

import (
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// Wire types for the admin API. They are kept apart from the domain types
// so the JSON contract (api/openapi.yaml) changes only on purpose.

type clientDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	PolicyID  *string   `json:"policy_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toClientDTO(c domain.Client) clientDTO {
	dto := clientDTO{
		ID:        c.ID,
		Name:      c.Name,
		Status:    string(c.Status),
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
	if c.PolicyID != "" {
		dto.PolicyID = &c.PolicyID
	}
	return dto
}

// apiKeyDTO describes a key by its public prefix. It has no field that
// could carry the key or its digest.
type apiKeyDTO struct {
	ID        string     `json:"id"`
	ClientID  string     `json:"client_id"`
	Prefix    string     `json:"prefix"`
	Active    bool       `json:"active"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

func toAPIKeyDTO(k domain.APIKey) apiKeyDTO {
	dto := apiKeyDTO{
		ID:        k.ID,
		ClientID:  k.ClientID,
		Prefix:    k.Prefix,
		Active:    k.Active(),
		CreatedAt: k.CreatedAt,
	}
	if !k.Active() {
		dto.RevokedAt = &k.RevokedAt
	}
	return dto
}

// issuedKeyDTO is the one response type that carries a full key. It is
// sent once, from the endpoint that created the key, with Cache-Control:
// no-store.
type issuedKeyDTO struct {
	apiKeyDTO
	Key string `json:"key"`
}

func toIssuedKeyDTO(k domain.IssuedKey) issuedKeyDTO {
	return issuedKeyDTO{apiKeyDTO: toAPIKeyDTO(k.Key), Key: k.Plaintext}
}
