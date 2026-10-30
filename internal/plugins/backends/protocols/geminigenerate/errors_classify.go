package geminigenerate

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/transporterr"
)

type apiFailureKind int

const (
	apiFailureNone apiFailureKind = iota
	apiFailureRateLimited
	apiFailureAuthInvalid
	// apiFailureRetryable is a transient upstream (5xx, 408) or transport failure
	// (timeout, connection reset/refused) worth a pre-output failover.
	apiFailureRetryable
)

func classifyGenaiAPIError(err error) (kind apiFailureKind, retryAfter string) {
	if err == nil {
		return apiFailureNone, ""
	}
	var pae *genai.APIError
	if errors.As(err, &pae) && pae != nil {
		return classifyFromAPIError(*pae)
	}
	if vae, ok := errors.AsType[genai.APIError](err); ok {
		return classifyFromAPIError(vae)
	}
	if transporterr.IsRetryable(err) {
		return apiFailureRetryable, ""
	}
	return apiFailureNone, ""
}

func classifyFromAPIError(ae genai.APIError) (apiFailureKind, string) {
	kind, ra := classifyGenaiCode(ae.Code, "")
	if kind != apiFailureRateLimited {
		return kind, ra
	}
	if ra == "" {
		ra = retryAfterFromGenaiDetails(ae.Details)
	}
	return kind, ra
}

func classifyGenaiCode(code int, retryAfter string) (apiFailureKind, string) {
	switch code {
	case http.StatusUnauthorized:
		return apiFailureAuthInvalid, ""
	case http.StatusTooManyRequests:
		return apiFailureRateLimited, strings.TrimSpace(retryAfter)
	case http.StatusRequestTimeout:
		return apiFailureRetryable, ""
	default:
		if code >= 500 {
			return apiFailureRetryable, ""
		}
		return apiFailureNone, ""
	}
}

// retryAfterFromGenaiDetails maps google.rpc.RetryInfo in the error JSON (when present)
// to a Retry-After-style delta-seconds string for the ordered quota-aware
// coordinator. The official genai client does not attach HTTP response headers
// to [genai.APIError], so server Retry-After headers are not available here;
// Google APIs sometimes include RetryInfo in the error details instead.
func retryAfterFromGenaiDetails(details []map[string]any) string {
	for _, d := range details {
		typ, _ := d["@type"].(string)
		if !strings.Contains(typ, "RetryInfo") {
			continue
		}
		rd, ok := d["retryDelay"].(string)
		if !ok {
			continue
		}
		rd = strings.TrimSpace(rd)
		if rd == "" {
			continue
		}
		dur, err := time.ParseDuration(rd)
		if err != nil || dur <= 0 {
			continue
		}
		secs := max(1, int(math.Ceil(dur.Seconds())))
		return strconv.Itoa(secs)
	}
	return ""
}
