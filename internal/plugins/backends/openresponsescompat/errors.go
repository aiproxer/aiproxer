package openresponsescompat

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Stable error roots for the generic OpenResponses backend mapping. Errors
// returned through the Open seam carry the instance prefix and never echo
// request bodies, response bodies, or resolved credential values.
var (
	// ErrOperationUnsupported rejects operations the backend does not serve.
	ErrOperationUnsupported = errors.New("openresponsescompat: operation is not supported")

	// ErrUnrepresentable is the root for canonical semantics that cannot be
	// encoded to the pinned profile without silent semantic loss.
	ErrUnrepresentable = errors.New("openresponsescompat: request semantics not representable")

	// ErrLimitExceeded is the root for request/response limit exceedances.
	ErrLimitExceeded = errors.New("openresponsescompat: limit exceeded")

	// ErrMalformedResponse is the root for upstream responses that violate the
	// pinned profile status/content-type/body/resource contract.
	ErrMalformedResponse = errors.New("openresponsescompat: malformed upstream response")
)

// LimitError carries the bounded identity of one exceeded configured limit.
type LimitError struct {
	Param  string
	Limit  int
	Actual int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("openresponsescompat: %s limit %d exceeded by %d", e.Param, e.Limit, e.Actual)
}

func (e *LimitError) Unwrap() error { return ErrLimitExceeded }

func limitError(param string, actual, limit int) error {
	return &LimitError{Param: param, Limit: limit, Actual: actual}
}

// httpFailureKind classifies an upstream HTTP failure for credential and
// failover policy without exposing provider body text.
type httpFailureKind int

const (
	httpFailureTerminal    httpFailureKind = iota
	httpFailureAuthInvalid                 // 401
	httpFailureRateLimited                 // 429
	httpFailureRetryable                   // 408, 5xx
)

// httpFailureError is the classified upstream HTTP failure. Error() is stable
// and never includes the provider response body or resolved secrets.
type httpFailureError struct {
	Status int
	Kind   httpFailureKind
	// RetryAfter carries the raw upstream Retry-After hint for 429s (possibly
	// empty). The quota-aware selector uses it for the threshold decision;
	// Error() never renders it.
	RetryAfter string
}

func (e *httpFailureError) Error() string {
	return fmt.Sprintf("openresponsescompat: upstream HTTP %d", e.Status)
}

// classifyHTTPStatus maps an upstream HTTP status onto a failure kind and
// wraps retryable/rate-limited failures so core may fail over pre-output.
// The response header supplies the raw Retry-After hint for 429 selection
// without affecting the stable error text.
func classifyHTTPStatus(status int, header http.Header) error {
	var kind httpFailureKind
	switch {
	case status == http.StatusUnauthorized:
		kind = httpFailureAuthInvalid
	case status == http.StatusTooManyRequests:
		kind = httpFailureRateLimited
	case status == http.StatusRequestTimeout || status >= 500:
		kind = httpFailureRetryable
	default:
		kind = httpFailureTerminal
	}
	err := &httpFailureError{Status: status, Kind: kind}
	if header != nil {
		err.RetryAfter = strings.TrimSpace(header.Get("Retry-After"))
	}
	if kind == httpFailureRetryable || kind == httpFailureRateLimited {
		return aipapi.RecoverablePreOutputError(err)
	}
	return err
}

// sanitizeErrorCode bounds a provider error code to the canonical event bound.
func sanitizeErrorCode(code string) string {
	return truncateRuneSafe(strings.TrimSpace(code), aipapi.MaxEventCodeFieldBytes)
}

// sanitizeErrorMessage applies a strict wire-message policy. Provider error
// text is never allowlisted because it may contain credentials, URLs, payloads,
// internal paths, or other sensitive diagnostics. Stable error codes remain
// available separately for client classification.
func sanitizeErrorMessage(_ string) string {
	return "upstream reported an error"
}

func truncateRuneSafe(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}
