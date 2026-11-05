package limiter

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

const (
	// keyPrefix starts every bucket key, so buckets are easy to find and
	// cannot collide with anything else in Redis.
	keyPrefix = "rg:bucket:"

	// allEndpoints is the endpoint part of the bucket that holds a policy's
	// own limit, as opposed to one endpoint rule's.
	allEndpoints = "all"

	// ipv6NetworkBits is how much of an IPv6 address identifies one caller.
	// A host is usually given a whole /64, and could otherwise rotate
	// through its addresses to escape its limit (ADR 0013).
	ipv6NetworkBits = 64
)

// errNoCaller means an identity names no caller a bucket could belong to.
var errNoCaller = errors.New("identity names no caller")

// keyFor returns the key of the bucket that the caller clientID draws from
// for endpoint: an endpoint rule's ID, or "" for the policy's own limit.
// clientID is what callerID returns, so it covers callers known by IP too.
//
// It is the only place a bucket key is built (ADR 0003). Sharding a hot key
// into sub-buckets later means changing this function and nothing else.
func keyFor(clientID, endpoint string) string {
	if endpoint == "" {
		endpoint = allEndpoints
	}
	// The endpoint goes first because it never contains a colon, while an
	// IPv6 caller always does; the key reads unambiguously either way.
	return keyPrefix + endpoint + ":" + clientID
}

// callerID names the caller an identity stands for: client:<id> for a
// keyed caller, ip:<address> for an IPv4 caller, and ip:<network>/64 for
// an IPv6 caller.
func callerID(id domain.Identity) (string, error) {
	switch id.Kind {
	case domain.IdentityAPIKey:
		if id.ClientID == "" {
			return "", fmt.Errorf("%w: keyed identity without a client", errNoCaller)
		}
		return "client:" + id.ClientID, nil
	case domain.IdentityIP:
		if !id.IP.IsValid() {
			return "", fmt.Errorf("%w: IP identity without an address", errNoCaller)
		}
		addr := id.IP.Unmap().WithZone("")
		if addr.Is4() {
			return "ip:" + addr.String(), nil
		}
		return "ip:" + netip.PrefixFrom(addr, ipv6NetworkBits).Masked().String(), nil
	default:
		return "", fmt.Errorf("%w: unknown identity kind %q", errNoCaller, id.Kind)
	}
}
