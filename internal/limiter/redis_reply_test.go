package limiter

import (
	"errors"
	"math"
	"testing"
	"time"
)

// The reply format is private to the script and the store, so it is tested
// here. The script itself runs against a real Redis in test/integration.

func TestParseReply(t *testing.T) {
	tests := []struct {
		name    string
		reply   []any
		allowed bool
		tokens  float64
		lag     time.Duration
	}{
		{"allowed", []any{int64(1), "99", "0"}, true, 99, 0},
		{"refused with a fraction", []any{int64(0), "0.83333333333333337", "0"}, false, 0.83333333333333337, 0},
		{"lag in microseconds", []any{int64(0), "0", "1500"}, false, 0, 1500 * time.Microsecond},
		{"lag saturates", []any{int64(0), "0", "1e300"}, false, 0, math.MaxInt64},
		{"infinite lag saturates", []any{int64(0), "0", "inf"}, false, 0, math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, tokens, lag, err := parseReply(tt.reply)
			if err != nil {
				t.Fatalf("parseReply: %v", err)
			}
			if allowed != tt.allowed || tokens != tt.tokens || lag != tt.lag {
				t.Errorf("parseReply = %v, %v, %v; want %v, %v, %v", allowed, tokens, lag, tt.allowed, tt.tokens, tt.lag)
			}
		})
	}
}

func TestParseReplyRefusesMalformedReplies(t *testing.T) {
	for name, reply := range map[string][]any{
		"too short":          {int64(1), "1"},
		"too long":           {int64(1), "1", "0", "0"},
		"allowed not an int": {"1", "1", "0"},
		"allowed out of set": {int64(2), "1", "0"},
		"tokens not a value": {int64(1), int64(5), "0"},
		"tokens garbage":     {int64(1), "lots", "0"},
		"tokens negative":    {int64(1), "-1", "0"},
		"tokens NaN":         {int64(1), "nan", "0"},
		"tokens infinite":    {int64(1), "inf", "0"},
		"lag negative":       {int64(1), "1", "-5"},
		"lag NaN":            {int64(1), "1", "nan"},
	} {
		if _, _, _, err := parseReply(reply); !errors.Is(err, errBadReply) {
			t.Errorf("%s: parseReply(%v) error = %v, want errBadReply", name, reply, err)
		}
	}
}
