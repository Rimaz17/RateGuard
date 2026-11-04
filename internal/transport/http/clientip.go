package httptransport

import (
	"net/http"
	"net/netip"
	"strings"
)

// maxForwardedHops bounds how far ClientIP walks X-Forwarded-For, so an
// enormous header costs no more than a short one.
const maxForwardedHops = 16

// TrustedProxies reports whether an address belongs to a proxy whose
// X-Forwarded-For entries can be believed. config.Prefixes satisfies it.
type TrustedProxies interface {
	Contains(addr netip.Addr) bool
}

// ClientIP returns the address a request comes from, for callers that are
// identified by IP. It reports false when the connection has no IP
// address at all, as on a Unix socket.
//
// X-Forwarded-For is client-controlled, so it is consulted only when the
// direct peer is a trusted proxy. Then the header is walked from the
// right, the end each trusted proxy appends to, past every trusted hop;
// the first untrusted address is the caller. Entries to its left were
// supplied by the caller and are ignored.
func ClientIP(r *http.Request, trusted TrustedProxies) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	current := normalise(ap.Addr())
	if trusted == nil || !trusted.Contains(current) {
		return current, true
	}

	xff := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	for range maxForwardedHops {
		if strings.TrimSpace(xff) == "" {
			break
		}
		var entry string
		if i := strings.LastIndexByte(xff, ','); i >= 0 {
			entry, xff = xff[i+1:], xff[:i]
		} else {
			entry, xff = xff, ""
		}
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			// A trusted proxy appends only addresses, so this came from
			// further left. The last trusted hop is the best we know.
			return current, true
		}
		addr = normalise(addr)
		if !trusted.Contains(addr) {
			return addr, true
		}
		current = addr
	}
	return current, true
}

// normalise drops IPv6 zones and unwraps IPv4-mapped IPv6 addresses, so
// one caller always gets one identity.
func normalise(addr netip.Addr) netip.Addr {
	return addr.Unmap().WithZone("")
}
