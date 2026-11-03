package domain

import "time"

// ClientStatus says whether a client may use RateGuard.
type ClientStatus string

// Client statuses. The database enforces the same set with a CHECK
// constraint.
const (
	ClientActive   ClientStatus = "active"
	ClientDisabled ClientStatus = "disabled"
)

// Client is a registered application that calls RateGuard, such as a
// customer app or a merchant app.
type Client struct {
	ID   string
	Name string

	// PolicyID is the client's rate-limit policy, or "" when none has been
	// assigned yet.
	PolicyID string

	Status    ClientStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}
