package policy

import (
	"fmt"
	"strings"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// ChangeKind says what kind of entity a change touched.
type ChangeKind string

// Change kinds.
const (
	// KindClient means a client's policy assignment changed.
	KindClient ChangeKind = "client"

	// KindPolicy means a policy, its rules, or the default flag changed.
	KindPolicy ChangeKind = "policy"
)

// Change names an entity whose cached resolution data is stale. It travels
// between instances as "client:<id>" or "policy:<id>" (ADR 0005).
type Change struct {
	Kind ChangeKind
	ID   string
}

// ForClient is a change to the client with the given ID.
func ForClient(id string) Change { return Change{Kind: KindClient, ID: id} }

// ForPolicy is a change to the policy with the given ID.
func ForPolicy(id string) Change { return Change{Kind: KindPolicy, ID: id} }

// String returns the change's wire form.
func (c Change) String() string { return string(c.Kind) + ":" + c.ID }

// ParseChange reads a change from its wire form.
func ParseChange(s string) (Change, error) {
	kind, id, ok := strings.Cut(s, ":")
	if !ok || !domain.IsID(id) {
		return Change{}, fmt.Errorf("malformed change %q", s)
	}
	switch k := ChangeKind(kind); k {
	case KindClient, KindPolicy:
		return Change{Kind: k, ID: id}, nil
	default:
		return Change{}, fmt.Errorf("unknown change kind %q", kind)
	}
}
