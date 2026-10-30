package openaicred

import (
	"errors"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/transporterr"
	"github.com/openai/openai-go/v3"
)

// FailureKind classifies an OpenAI HTTP API error for credential-pool handling.
type FailureKind int

const (
	// FailureNone means the error is not a classified OpenAI API HTTP failure.
	FailureNone FailureKind = iota
	FailureRateLimited
	FailureAuthInvalid
	// FailureRetryable means a transient upstream (5xx, 408) or transport failure
	// (timeout, connection reset/refused) worth a pre-output failover.
	FailureRetryable
)

// ClassifyHTTPStatus classifies an HTTP status code and optional headers for credential-pool handling.
func ClassifyHTTPStatus(statusCode int, header http.Header) (kind FailureKind, retryAfter string) {
	switch statusCode {
	case http.StatusUnauthorized:
		return FailureAuthInvalid, ""
	case http.StatusTooManyRequests:
		if header != nil {
			return FailureRateLimited, strings.TrimSpace(header.Get("Retry-After"))
		}
		return FailureRateLimited, ""
	case http.StatusRequestTimeout:
		return FailureRetryable, ""
	default:
		if statusCode >= 500 {
			return FailureRetryable, ""
		}
		return FailureNone, ""
	}
}

// ClassifyHTTPResponse classifies an *http.Response for credential-pool handling.
func ClassifyHTTPResponse(resp *http.Response) (kind FailureKind, retryAfter string) {
	if resp == nil {
		return FailureNone, ""
	}
	return ClassifyHTTPStatus(resp.StatusCode, resp.Header)
}

// HTTPStatusError represents an error carrying an HTTP status code and response headers.
type HTTPStatusError interface {
	error
	HTTPStatusCode() int
	HTTPHeader() http.Header
}

// ClassifyOpenAIAPIError inspects *openai.Error or HTTPStatusError (including wrapped).
// On rate limit it returns the Retry-After header value when present (may be empty).
// Transport-level failures are classified via [transporterr.IsRetryable].
func ClassifyOpenAIAPIError(err error) (kind FailureKind, retryAfter string) {
	if err == nil {
		return FailureNone, ""
	}
	var statusErr HTTPStatusError
	if errors.As(err, &statusErr) && statusErr != nil {
		return ClassifyHTTPStatus(statusErr.HTTPStatusCode(), statusErr.HTTPHeader())
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) || apiErr == nil {
		if transporterr.IsRetryable(err) {
			return FailureRetryable, ""
		}
		return FailureNone, ""
	}
	var header http.Header
	if apiErr.Response != nil {
		header = apiErr.Response.Header
	}
	return ClassifyHTTPStatus(apiErr.StatusCode, header)
}
