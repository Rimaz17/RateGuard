package httptransport

import (
	"context"
	"net"
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

type trustedPeerKey struct{}

// TrustPeer marks a connection's requests as coming from a trusted local
// peer. It has the signature of http.Server.ConnContext and is meant for
// the Unix-socket listener, whose peers are vetted by the socket file's
// permissions (ADR 0017).
func TrustPeer(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, trustedPeerKey{}, true)
}

func peerTrusted(ctx context.Context) bool {
	trusted, _ := ctx.Value(trustedPeerKey{}).(bool)
	return trusted
}

// ClientIP returns the address a request comes from, for callers that are
// identified by IP. It reports false when no address can be found.
//
// X-Forwarded-For is client-controlled, so it is consulted only when the
// direct peer is a trusted proxy, or a trusted local peer on the Unix
// socket (TrustPeer). Then the header is walked from the right, the end
// each trusted hop appends to, past every trusted proxy; the first
// untrusted address is the caller. Entries to its left were supplied by
// the caller and are ignored.
func ClientIP(r *http.Request, trusted TrustedProxies) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		// No IP peer, as on a Unix socket. Only a trusted one may say
		// whom it is forwarding for.
		if !peerTrusted(r.Context()) {
			return netip.Addr{}, false
		}
		addr := forwardedFor(r, trusted, netip.Addr{})
		return addr, addr.IsValid()
	}
	current := normalise(ap.Addr())
	if trusted == nil || !trusted.Contains(current) {
		return current, true
	}
	return forwardedFor(r, trusted, current), true
}

// forwardedFor walks X-Forwarded-For from the right and returns the first
// address that is not a trusted proxy, or the last trusted hop, starting
// from current, when there is none.
func forwardedFor(r *http.Request, trusted TrustedProxies, current netip.Addr) netip.Addr {
	isTrusted := func(a netip.Addr) bool { return trusted != nil && trusted.Contains(a) }
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
			// A trusted hop appends only addresses, so this came from
			// further left. The last trusted hop is the best we know.
			return current
		}
		addr = normalise(addr)
		if !isTrusted(addr) {
			return addr
		}
		current = addr
	}
	return current
}

// normalise drops IPv6 zones and unwraps IPv4-mapped IPv6 addresses, so
// one caller always gets one identity.
func normalise(addr netip.Addr) netip.Addr {
	return addr.Unmap().WithZone("")
}
