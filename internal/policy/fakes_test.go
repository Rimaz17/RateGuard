package policy_test

import (
	"context"
	"sync"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// fakeStore stands in for the Postgres repositories behind the resolver
// and the service. These unit tests are about resolution rules and cache
// behaviour; the real repositories on a real database are covered in
// test/integration.
type fakeStore struct {
	mu        sync.Mutex
	clients   map[string]string // client ID to policy ID
	policies  map[string]domain.Policy
	rules     map[string][]domain.EndpointRule
	defaultID string

	// err, when set, fails every read.
	err error

	// hold, when set, makes GetWithRules read the policy and then wait for
	// the channel to close before returning, like a slow query that has
	// already taken its snapshot.
	hold chan struct{}

	clientReads, policyReads, defaultReads int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		clients:  map[string]string{},
		policies: map[string]domain.Policy{},
		rules:    map[string][]domain.EndpointRule{},
	}
}

func (s *fakeStore) PolicyID(_ context.Context, clientID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clientReads++
	if s.err != nil {
		return "", s.err
	}
	id, ok := s.clients[clientID]
	if !ok {
		return "", domain.ErrClientNotFound
	}
	return id, nil
}

func (s *fakeStore) GetWithRules(ctx context.Context, id string) (domain.Policy, []domain.EndpointRule, error) {
	s.mu.Lock()
	s.policyReads++
	err := s.err
	p, ok := s.policies[id]
	rules := append([]domain.EndpointRule(nil), s.rules[id]...)
	hold := s.hold
	s.mu.Unlock()

	if hold != nil {
		// Like a real query, the wait ends early if the context does.
		select {
		case <-hold:
		case <-ctx.Done():
			return domain.Policy{}, nil, ctx.Err()
		}
	}
	if err != nil {
		return domain.Policy{}, nil, err
	}
	if !ok {
		return domain.Policy{}, nil, domain.ErrPolicyNotFound
	}
	return p, rules, nil
}

// reads returns how many times each source method has been called.
func (s *fakeStore) reads() (clients, policies, defaults int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientReads, s.policyReads, s.defaultReads
}

func (s *fakeStore) DefaultID(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultReads++
	if s.err != nil {
		return "", s.err
	}
	return s.defaultID, nil
}

// set runs f with the store locked, to change what later reads see.
func (s *fakeStore) set(f func(s *fakeStore)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *fakeStore) addPolicy(id, name string, capacity int, rules ...domain.EndpointRule) {
	s.set(func(s *fakeStore) {
		s.policies[id] = domain.Policy{
			ID: id, Name: name, Capacity: capacity, WindowSeconds: 60,
			RefillRate: domain.DefaultRefillRate(capacity, 60), Enabled: true,
		}
		for i := range rules {
			rules[i].PolicyID = id
		}
		s.rules[id] = rules
	})
}

func rule(id, method, pattern string, capacity int) domain.EndpointRule {
	return domain.EndpointRule{ID: id, Method: method, PathPattern: pattern, Capacity: capacity, WindowSeconds: 60}
}
