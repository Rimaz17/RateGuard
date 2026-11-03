package config

import (
	"fmt"
	"net/netip"
	"strings"
)

// Prefixes is a comma-separated list of CIDR networks, such as
// "10.0.0.0/8, fd00::/8". Space around each entry is ignored; an empty
// value is an empty list.
type Prefixes []netip.Prefix

// UnmarshalText implements encoding.TextUnmarshaler, which the environment
// parser uses.
func (p *Prefixes) UnmarshalText(text []byte) error {
	var out Prefixes
	for _, field := range strings.Split(string(text), ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(field)
		if err != nil {
			return fmt.Errorf("%q is not a CIDR network such as 10.0.0.0/8", field)
		}
		out = append(out, prefix.Masked())
	}
	*p = out
	return nil
}

// Contains reports whether addr is in any of the networks.
func (p Prefixes) Contains(addr netip.Addr) bool {
	for _, prefix := range p {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
