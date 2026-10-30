package anthropicmessages

import (
	"errors"
	"net/http"
	"strings"

	asdk "github.com/anthropics/anthropic-sdk-go"

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

func classifyAnthropicAPIError(err error) (kind apiFailureKind, retryAfter string) {
	var apiErr *asdk.Error
	if err == nil {
		return apiFailureNone, ""
	}
	if !errors.As(err, &apiErr) || apiErr == nil {
		if transporterr.IsRetryable(err) {
			return apiFailureRetryable, ""
		}
		return apiFailureNone, ""
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized:
		return apiFailureAuthInvalid, ""
	case http.StatusTooManyRequests:
		ra := ""
		if apiErr.Response != nil {
			ra = strings.TrimSpace(apiErr.Response.Header.Get("Retry-After"))
		}
		return apiFailureRateLimited, ra
	case http.StatusRequestTimeout:
		return apiFailureRetryable, ""
	default:
		if apiErr.StatusCode >= 500 {
			return apiFailureRetryable, ""
		}
		return apiFailureNone, ""
	}
}
