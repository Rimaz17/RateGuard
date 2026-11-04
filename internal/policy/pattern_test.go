package policy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/policy"
)

func TestParsePatternAcceptsValidPatterns(t *testing.T) {
	for _, s := range []string{
		"/",
		"/**",
		"/*",
		"/api/login",
		"/api/orders/*",
		"/api/orders/*/items",
		"/api/**",
		"/api/*/**",
		"/v1/files/report.json",
		"/" + strings.Repeat("a", policy.MaxPatternLen-1),
	} {
		p, err := policy.ParsePattern(s)
		if err != nil {
			t.Errorf("ParsePattern(%q): %v", s, err)
			continue
		}
		if p.String() != s {
			t.Errorf("ParsePattern(%q).String() = %q", s, p.String())
		}
	}
}

func TestParsePatternRejectsInvalidPatterns(t *testing.T) {
	tests := []struct{ in, reason string }{
		{"", "must start with /"},
		{"api/login", "must start with /"},
		{"*", "must start with /"},
		{"/" + strings.Repeat("a", policy.MaxPatternLen), "must be at most 512 bytes"},
		{"/api/", "must not contain empty segments or end with /"},
		{"//api", "must not contain empty segments or end with /"},
		{"/api//login", "must not contain empty segments or end with /"},
		{"/api/./login", "must not contain . or .. segments"},
		{"/api/../login", "must not contain . or .. segments"},
		{"/**/login", "may use ** only as its last segment"},
		{"/api/**/**", "may use ** only as its last segment"},
		{"/api/v*", "must use * and ** only as whole segments"},
		{"/api/***", "must use * and ** only as whole segments"},
		{"/api/lo*in", "must use * and ** only as whole segments"},
		{"/api/log\nin", "must not contain control characters"},
		{"/api/\x7f", "must not contain control characters"},
	}
	for _, tt := range tests {
		_, err := policy.ParsePattern(tt.in)
		var ve *domain.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("ParsePattern(%q) error = %v, want a ValidationError", tt.in, err)
			continue
		}
		if ve.Field != "path_pattern" || ve.Reason != tt.reason {
			t.Errorf("ParsePattern(%q) = %q %q, want path_pattern %q", tt.in, ve.Field, ve.Reason, tt.reason)
		}
	}
}

func TestPatternMatch(t *testing.T) {
	tests := []struct {
		pattern string
		match   []string
		noMatch []string
	}{
		{
			pattern: "/",
			match:   []string{"/", "", "//", "/.", "/a/.."},
			noMatch: []string{"/a", "/api/login"},
		},
		{
			pattern: "/api/login",
			// Every one of these is the same endpoint once normalised, so a
			// strict rule cannot be dodged by spelling the path differently.
			match:   []string{"/api/login", "/api/login/", "/api//login", "//api/login", "api/login", "/api/./login", "/api/x/../login", "/api/login/."},
			noMatch: []string{"/api/Login", "/api/login/x", "/api", "/api/logins", "/api/log", "/apilogin", "/api/login%20"},
		},
		{
			pattern: "/api/orders/*",
			match:   []string{"/api/orders/1", "/api/orders/abc/"},
			noMatch: []string{"/api/orders", "/api/orders/", "/api/orders/1/items"},
		},
		{
			pattern: "/api/orders/*/items",
			match:   []string{"/api/orders/1/items"},
			noMatch: []string{"/api/orders/items", "/api/orders/1/2/items", "/api/orders/1/items/3"},
		},
		{
			pattern: "/api/**",
			match:   []string{"/api", "/api/", "/api/x", "/api/x/y/z"},
			noMatch: []string{"/", "/apix", "/other/api"},
		},
		{
			pattern: "/**",
			match:   []string{"/", "/a", "/a/b/c"},
		},
		{
			pattern: "/*",
			match:   []string{"/a"},
			noMatch: []string{"/", "/a/b"},
		},
	}
	for _, tt := range tests {
		p, err := policy.ParsePattern(tt.pattern)
		if err != nil {
			t.Fatalf("ParsePattern(%q): %v", tt.pattern, err)
		}
		for _, path := range tt.match {
			if !p.Match(path) {
				t.Errorf("%q should match %q", tt.pattern, path)
			}
		}
		for _, path := range tt.noMatch {
			if p.Match(path) {
				t.Errorf("%q should not match %q", tt.pattern, path)
			}
		}
	}
}

// FuzzPatternMatchIgnoresSpelling checks that matching depends only on the
// normalised path: adding a trailing slash or doubling a slash never
// changes the outcome, whatever the pattern.
func FuzzPatternMatchIgnoresSpelling(f *testing.F) {
	f.Add("/api/login", "/api/login")
	f.Add("/api/**", "/api/x/y")
	f.Add("/api/*/items", "/api/1/items")
	f.Add("/", "/")
	f.Fuzz(func(t *testing.T, pattern, path string) {
		p, err := policy.ParsePattern(pattern)
		if err != nil {
			return
		}
		want := p.Match(path)
		for _, variant := range []string{path + "/", strings.Replace("/"+path, "/", "//", 1), path + "/."} {
			if got := p.Match(variant); got != want {
				t.Errorf("%q: Match(%q) = %v but Match(%q) = %v", pattern, path, want, variant, got)
			}
		}
	})
}
