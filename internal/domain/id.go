package domain

// MaxPageLimit caps how many items one list call may return.
const MaxPageLimit = 200

// IsID reports whether s is a canonical UUID: 32 hex digits grouped
// 8-4-4-4-12. Every RateGuard ID takes that form, so checking before a
// query turns a malformed ID into a plain "not found" rather than a
// database error.
func IsID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !isHex(c) {
				return false
			}
		}
	}
	return true
}

func isHex(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

// Page selects one page of a list ordered by ID, which for time-ordered
// UUIDs is creation order. Keyset paging stays stable while rows are added,
// unlike an offset.
type Page struct {
	// After is the last ID of the previous page, or "" for the first page.
	After string

	// Limit is the page size, from 1 to MaxPageLimit.
	Limit int
}
