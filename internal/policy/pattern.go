package policy

import (
	"cmp"
	"path"
	"strings"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// MaxPatternLen bounds an endpoint rule's path pattern, in bytes.
const MaxPatternLen = 512

// segmentKind says how a pattern segment matches. The order is the
// precedence of ADR 0010: a higher kind is more specific.
type segmentKind uint8

const (
	// kindRest is "**": zero or more segments, only ever last.
	kindRest segmentKind = iota + 1

	// kindEnd stands for the position after a pattern's last segment. It
	// ranks above kindRest so that /api beats /api/** for the path /api.
	kindEnd

	// kindAny is "*": exactly one segment.
	kindAny

	// kindLiteral matches one segment equal to its text.
	kindLiteral
)

type segment struct {
	kind    segmentKind
	literal string
}

// Pattern is a parsed endpoint rule path pattern (ADR 0010). The zero value
// is not a valid pattern; use ParsePattern.
type Pattern struct {
	raw  string
	segs []segment
}

// ParsePattern parses and validates an endpoint rule path pattern. Its
// errors are *domain.ValidationError values for the path_pattern field.
func ParsePattern(s string) (Pattern, error) {
	switch {
	case s == "" || s[0] != '/':
		return Pattern{}, invalidPattern("must start with /")
	case len(s) > MaxPatternLen:
		return Pattern{}, invalidPattern("must be at most 512 bytes")
	case s == "/":
		return Pattern{raw: s}, nil
	}

	parts := strings.Split(s[1:], "/")
	segs := make([]segment, len(parts))
	for i, part := range parts {
		switch {
		case part == "":
			return Pattern{}, invalidPattern("must not contain empty segments or end with /")
		case part == "." || part == "..":
			return Pattern{}, invalidPattern("must not contain . or .. segments")
		case part == "**":
			if i != len(parts)-1 {
				return Pattern{}, invalidPattern("may use ** only as its last segment")
			}
			segs[i] = segment{kind: kindRest}
		case part == "*":
			segs[i] = segment{kind: kindAny}
		case strings.Contains(part, "*"):
			return Pattern{}, invalidPattern("must use * and ** only as whole segments")
		case hasControl(part):
			return Pattern{}, invalidPattern("must not contain control characters")
		default:
			segs[i] = segment{kind: kindLiteral, literal: part}
		}
	}
	return Pattern{raw: s, segs: segs}, nil
}

func invalidPattern(reason string) error {
	return &domain.ValidationError{Field: "path_pattern", Reason: reason}
}

func hasControl(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// String returns the pattern as written.
func (p Pattern) String() string { return p.raw }

// Match reports whether the pattern matches a request path, after the path
// is normalised as ADR 0010 describes.
func (p Pattern) Match(requestPath string) bool {
	return p.match(splitPath(requestPath))
}

// match is Match on a path already split by splitPath, so a decision
// splits the path once for all of a policy's rules.
func (p Pattern) match(segs []string) bool {
	for i, s := range p.segs {
		if s.kind == kindRest {
			return true
		}
		if i >= len(segs) {
			return false
		}
		// splitPath never yields an empty segment, so kindAny matches
		// whatever is here.
		if s.kind == kindLiteral && segs[i] != s.literal {
			return false
		}
	}
	return len(segs) == len(p.segs)
}

// kindAt returns the kind of the segment at position i, or kindEnd past the
// last one.
func (p Pattern) kindAt(i int) segmentKind {
	if i < len(p.segs) {
		return p.segs[i].kind
	}
	return kindEnd
}

// comparePatterns orders patterns by specificity: positive when a is more
// specific than b, negative when less, zero when they have the same shape.
// Patterns are compared segment by segment, left to right, and the first
// position where their kinds differ decides (ADR 0010). It is a
// lexicographic order on kind sequences, so it is transitive and sorting by
// it is well defined.
func comparePatterns(a, b Pattern) int {
	for i := 0; ; i++ {
		ka, kb := a.kindAt(i), b.kindAt(i)
		if ka != kb {
			return cmp.Compare(ka, kb)
		}
		if ka == kindEnd || ka == kindRest {
			return 0
		}
	}
}

// splitPath normalises a request path as ADR 0010 requires and splits it
// into segments: "" and "/" give none, "/api//login/" gives [api login].
// Normalising first means a trailing or doubled slash, or a dot segment,
// cannot slip a request past a stricter rule.
func splitPath(p string) []string {
	if p == "" || p[0] != '/' {
		p = "/" + p
	}
	p = path.Clean(p)
	if p == "/" {
		return nil
	}
	return strings.Split(p[1:], "/")
}
