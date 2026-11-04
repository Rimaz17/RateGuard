package policy

import (
	"path"
	"strings"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// MaxPatternLen bounds an endpoint rule's path pattern, in bytes.
const MaxPatternLen = 512

// segmentKind says how a pattern segment matches.
type segmentKind uint8

const (
	// kindRest is "**": zero or more segments, only ever last.
	kindRest segmentKind = iota + 1

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
