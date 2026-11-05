package policy_test

import (
	"slices"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/policy"
)

// recordingInvalidator records what it was asked to drop.
type recordingInvalidator struct {
	changes []policy.Change
	flushes int
}

func (r *recordingInvalidator) Invalidate(c policy.Change) { r.changes = append(r.changes, c) }
func (r *recordingInvalidator) Flush()                     { r.flushes++ }

// recordingClientCache records the clients it was asked to drop.
type recordingClientCache struct {
	clients []string
	flushes int
}

func (r *recordingClientCache) InvalidateClient(id string) { r.clients = append(r.clients, id) }
func (r *recordingClientCache) Flush()                     { r.flushes++ }

func TestInvalidatorsFanOut(t *testing.T) {
	a, b := &recordingInvalidator{}, &recordingInvalidator{}
	fan := policy.Invalidators{a, b}

	fan.Invalidate(policy.ForClient(customerApp))
	fan.Invalidate(policy.ForPolicy(freeID))
	fan.Flush()

	want := []policy.Change{policy.ForClient(customerApp), policy.ForPolicy(freeID)}
	for name, r := range map[string]*recordingInvalidator{"first": a, "second": b} {
		if !slices.Equal(r.changes, want) || r.flushes != 1 {
			t.Errorf("%s invalidator saw %v and %d flushes, want %v and 1", name, r.changes, r.flushes, want)
		}
	}
}

func TestClientChangesActsOnClientChangesOnly(t *testing.T) {
	keys := &recordingClientCache{}
	inv := policy.ClientChanges(keys)

	inv.Invalidate(policy.ForPolicy(freeID))
	inv.Invalidate(policy.ForClient(customerApp))
	inv.Flush()

	if !slices.Equal(keys.clients, []string{customerApp}) || keys.flushes != 1 {
		t.Errorf("client cache dropped %v with %d flushes, want [%s] and 1", keys.clients, keys.flushes, customerApp)
	}
}
