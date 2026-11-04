package policy

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// compiledPolicy is a policy ready for decisions: its patterns parsed, its
// rules sorted most specific first, and every possible result built in
// advance. It is immutable once compiled, so the cache shares it between
// goroutines without locking.
type compiledPolicy struct {
	enabled bool

	// base is the policy's own limit, for requests no rule matches.
	base domain.ResolvedLimit

	// rules are sorted by compareRules, so the first match is the most
	// specific one.
	rules []compiledRule
}

type compiledRule struct {
	method  string
	pattern Pattern
	limit   domain.ResolvedLimit
}

// compile prepares a policy and its rules for resolution. A stored rule with
// an invalid pattern can only come from editing the database by hand; it
// fails the whole load rather than being skipped, because a silently
// missing rule would leave an endpoint under a looser limit.
func compile(p domain.Policy, rules []domain.EndpointRule) (*compiledPolicy, error) {
	cp := &compiledPolicy{
		enabled: p.Enabled,
		base: domain.ResolvedLimit{
			PolicyID:      p.ID,
			PolicyName:    p.Name,
			Capacity:      p.Capacity,
			WindowSeconds: p.WindowSeconds,
			RefillRate:    p.RefillRate,
		},
		rules: make([]compiledRule, 0, len(rules)),
	}
	for _, r := range rules {
		pattern, err := ParsePattern(r.PathPattern)
		if err != nil {
			return nil, fmt.Errorf("endpoint rule %s of policy %s: %w", r.ID, p.ID, err)
		}
		cp.rules = append(cp.rules, compiledRule{
			method:  r.Method,
			pattern: pattern,
			limit: domain.ResolvedLimit{
				PolicyID:      p.ID,
				PolicyName:    p.Name,
				RuleID:        r.ID,
				Capacity:      r.Capacity,
				WindowSeconds: r.WindowSeconds,
				RefillRate:    r.RefillRate(),
			},
		})
	}
	slices.SortFunc(cp.rules, compareRules)
	return cp, nil
}

// limitFor returns the limit for one request: the most specific matching
// rule's, or the policy's own.
func (cp *compiledPolicy) limitFor(method, requestPath string) domain.ResolvedLimit {
	if len(cp.rules) == 0 {
		return cp.base
	}
	method = strings.ToUpper(method)
	segs := splitPath(requestPath)
	for _, r := range cp.rules {
		if (r.method == domain.AnyMethod || r.method == method) && r.pattern.match(segs) {
			return r.limit
		}
	}
	return cp.base
}

// compareRules sorts the most specific rule first (ADR 0010): by pattern,
// then a named method before AnyMethod. Two rules that tie on both can
// never match the same request, so the last two keys only make the order
// deterministic.
func compareRules(a, b compiledRule) int {
	if c := comparePatterns(b.pattern, a.pattern); c != 0 {
		return c
	}
	if c := cmp.Compare(methodRank(b.method), methodRank(a.method)); c != 0 {
		return c
	}
	if c := strings.Compare(a.pattern.raw, b.pattern.raw); c != 0 {
		return c
	}
	return strings.Compare(a.method, b.method)
}

func methodRank(m string) int {
	if m == domain.AnyMethod {
		return 0
	}
	return 1
}
