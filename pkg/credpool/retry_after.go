package credpool

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxRetryAfterDeltaSeconds is the largest delta-seconds value that cannot
// overflow time.Duration arithmetic. Larger well-formed integers are rejected
// as untrustworthy rather than wrapping into a bogus near/far instant.
const maxRetryAfterDeltaSeconds = int64(math.MaxInt64 / int64(time.Second))

// CooldownFromRetryAfter parses a Retry-After header value (RFC 7231): either
// delta-seconds or an HTTP-date. On success, the returned time is strictly
// after now. Pure: it does not read or mutate credential pool state.
func CooldownFromRetryAfter(value string, now time.Time) (time.Time, bool) {
	s := strings.TrimSpace(value)
	if s == "" {
		return time.Time{}, false
	}
	if secs, err := strconv.ParseInt(s, 10, 64); err == nil {
		if secs <= 0 || secs > maxRetryAfterDeltaSeconds {
			return time.Time{}, false
		}
		until := now.Add(time.Duration(secs) * time.Second)
		if !until.After(now) {
			return time.Time{}, false
		}
		return until, true
	}
	tm, err := http.ParseTime(s)
	if err != nil {
		return time.Time{}, false
	}
	if !tm.After(now) {
		return time.Time{}, false
	}
	return tm, true
}

// CooldownFromRetryAfterOrFallback returns CooldownFromRetryAfter when parsing
// succeeds; otherwise it returns now+fallback (for provider-local conservative cooldowns).
func CooldownFromRetryAfterOrFallback(value string, now time.Time, fallback time.Duration) time.Time {
	if until, ok := CooldownFromRetryAfter(value, now); ok {
		return until
	}
	return now.Add(fallback)
}

// DefaultRateLimitFallback is used when Retry-After is missing or unparseable.
const DefaultRateLimitFallback = 60 * time.Second
