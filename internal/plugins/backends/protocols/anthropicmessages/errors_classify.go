package anthropicmessages

import (
	"errors"
	"net/http"
	"strings"

	asdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/transporterr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// inlineSystemRejectReason is the fixed, client-safe reason returned when the
// provider refuses a request carrying a mid-history system message. It names no
// model, provider body or request content.
const inlineSystemRejectReason = "upstream rejected the mid-history system message"

// classifyInlineSystemReject turns a provider HTTP 400 into a typed client
// reject when the request projection carried a mid-history system message: the
// model may not accept inline system placement, and the proxy must report that
// as a client error rather than an opaque 500. Every other error, and every 400
// for a request without an inline system message, is returned unchanged.
func classifyInlineSystemReject(err error, p asdk.MessageNewParams) error {
	if err == nil || !hasInlineSystemMessage(p) {
		return err
	}
	var apiErr *asdk.Error
	if !errors.As(err, &apiErr) || apiErr == nil || apiErr.StatusCode != http.StatusBadRequest {
		return err
	}
	return &inlineSystemRejectError{reject: &aipapi.RejectError{Reason: inlineSystemRejectReason}, upstream: err}
}

// inlineSystemRejectError reports only the fixed reason as its text. The
// upstream error stays reachable through Unwrap for diagnostics, so provider
// bodies cannot reach client-visible messages through Error().
type inlineSystemRejectError struct {
	reject   *aipapi.RejectError
	upstream error
}

func (e *inlineSystemRejectError) Error() string { return e.reject.Error() }

func (e *inlineSystemRejectError) Unwrap() []error { return []error{e.reject, e.upstream} }

func hasInlineSystemMessage(p asdk.MessageNewParams) bool {
	for _, m := range p.Messages {
		if m.Role == asdk.MessageParamRoleSystem {
			return true
		}
	}
	return false
}

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
