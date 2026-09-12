package client

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/platform"
)

// RateLimit is the platform's request rate limit as of one response.
type RateLimit struct {
	// Limit is the number of requests allowed in the current window.
	Limit int
	// Remaining is the number of requests left in the current window.
	Remaining int
	// Reset is the time until the window resets. Zero if the header was absent or invalid.
	Reset time.Duration
}

// parseRateLimit reads the RateLimit-* headers. It returns nil unless RateLimit-Limit and
// RateLimit-Remaining are non-negative integers.
func parseRateLimit(h http.Header) *RateLimit {
	limit, ok := nonNegative(h.Get(platform.HeaderRateLimitLimit))
	if !ok {
		return nil
	}
	remaining, ok := nonNegative(h.Get(platform.HeaderRateLimitRemaining))
	if !ok {
		return nil
	}
	rl := &RateLimit{Limit: limit, Remaining: remaining}
	if reset, ok := nonNegative(h.Get(platform.HeaderRateLimitReset)); ok {
		rl.Reset = time.Duration(reset) * time.Second
	}
	return rl
}

func nonNegative(v string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	return n, err == nil && n >= 0
}
