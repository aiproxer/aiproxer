package frontendpipe

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
)

// MapAnthropicExecuteError maps executor outcomes to Anthropic wire error.type strings.
func MapAnthropicExecuteError(out execerr.Outcome) (status int, message, errType string) {
	status = out.Status
	message = out.Message
	if out.Kind == execerr.KindInternalError {
		message = execerr.InternalWireMessage
	}
	switch out.Kind {
	case execerr.KindSessionDenial:
		errType = execerr.OpenAIWireErrorType(out.Status)
	case execerr.KindClientReject:
		errType = "invalid_request_error"
	case execerr.KindBillingDenied:
		errType = "invalid_request_error"
	case execerr.KindPolicyDenied:
		errType = "permission_error"
	case execerr.KindPolicyFailure, execerr.KindPolicyMalformed, execerr.KindBillingUnavailable:
		errType = "api_error"
	default:
		errType = "api_error"
	}
	return status, message, errType
}

// MapGeminiExecuteError maps executor outcomes to Gemini error.status strings.
func MapGeminiExecuteError(out execerr.Outcome) (status int, message, gstatus string) {
	status = out.Status
	message = out.Message
	if out.Kind == execerr.KindInternalError {
		message = execerr.InternalWireMessage
	}
	gstatus = MapGeminiWireStatus(out)
	return status, message, gstatus
}

// MapGeminiWireStatus returns the Google RPC status string for a classified executor outcome.
func MapGeminiWireStatus(out execerr.Outcome) string {
	switch out.Kind {
	case execerr.KindPolicyDenied:
		return "PERMISSION_DENIED"
	case execerr.KindPolicyFailure, execerr.KindBillingUnavailable:
		return "UNAVAILABLE"
	case execerr.KindBillingDenied:
		return "RESOURCE_EXHAUSTED"
	case execerr.KindPolicyMalformed, execerr.KindInternalError:
		return "INTERNAL"
	case execerr.KindClientReject:
		switch out.Status {
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
			return "INVALID_ARGUMENT"
		case http.StatusForbidden:
			return "PERMISSION_DENIED"
		default:
			return geminiStatusFromHTTP(out.Status)
		}
	case execerr.KindSessionDenial:
		return geminiStatusFromHTTP(out.Status)
	default:
		return geminiStatusFromHTTP(out.Status)
	}
}

func geminiStatusFromHTTP(status int) string {
	switch {
	case status == http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case status == http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case status == http.StatusForbidden:
		return "PERMISSION_DENIED"
	case status == http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case status == http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case status >= 500:
		return "INTERNAL"
	default:
		return "UNKNOWN"
	}
}

// StatusError is a protocol-mapped HTTP error returned from AfterDecode or WrapStream.
type StatusError struct {
	Status  int
	Type    string
	Code    string
	Message string
	Err     error
}

func (e *StatusError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "request failed"
}

func (e *StatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// HTTPStatus returns a wire-safe status, never 0.
func (e *StatusError) HTTPStatus() int {
	if e == nil || e.Status < 100 || e.Status > 599 {
		return http.StatusBadRequest
	}
	return e.Status
}
