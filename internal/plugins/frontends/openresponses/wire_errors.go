package openresponses

import (
	"errors"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
)

// WireErrors implements frontendpipe.WireErrors for OpenResponses envelopes.
type WireErrors struct{}

func (WireErrors) WriteBodyTooLarge(w http.ResponseWriter) error {
	writeWireError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "body_too_large", "Request body exceeds max limit")
	return nil
}

func (WireErrors) WriteReadBodyFailed(w http.ResponseWriter) error {
	writeWireError(w, http.StatusBadRequest, "invalid_request_error", "bad_request", "Failed to read request body")
	return nil
}

func (WireErrors) WriteExecutorNotConfigured(w http.ResponseWriter) error {
	writeWireError(w, http.StatusNotImplemented, "invalid_request_error", "operation_not_implemented", "OpenResponses responses is not enabled")
	return nil
}

func (WireErrors) WritePreflightCanceled(w http.ResponseWriter) error {
	writeWireError(w, http.StatusBadRequest, "invalid_request_error", "client_closed_request", "Request canceled")
	return nil
}

func (WireErrors) WriteInvalidJSON(w http.ResponseWriter) error {
	writeWireError(w, http.StatusBadRequest, "invalid_request_error", "bad_request", "Invalid request")
	return nil
}

func (WireErrors) WriteAdmissionReject(w http.ResponseWriter, d decodeqos.Decision) error {
	writeWireError(w, d.Status, "server_error", "decode_admission_rejected", d.Message)
	return nil
}

func (WireErrors) WriteInvalidRequest(w http.ResponseWriter) error {
	writeWireError(w, http.StatusBadRequest, "invalid_request_error", "bad_request", "Invalid request")
	return nil
}

func (WireErrors) WriteEncodeFailed(w http.ResponseWriter) error {
	writeWireError(w, http.StatusInternalServerError, "server_error", "encode_failed", "Failed to encode response")
	return nil
}

func (WireErrors) WriteExecuteError(w http.ResponseWriter, out execerr.Outcome) error {
	if out.Err != nil {
		status, typ, code, message := classifyExecutionError(out.Err)
		writeWireError(w, status, typ, code, message)
		return nil
	}
	status, typ, code, message := mapExecuteOutcome(out)
	writeWireError(w, status, typ, code, message)
	return nil
}

// mapExecuteOutcome maps a classified executor outcome onto OpenResponses wire
// fields. Status and message are the shared execerr contract; type and code are
// this frontend's protocol vocabulary, grouped by the same kinds the
// OpenAI-shaped frontends map in frontendpipe.MapOpenAIExecuteError. The
// executor error classification itself is never re-derived here: callers pass
// the execerr.ClassifyExecute outcome through unchanged.
func mapExecuteOutcome(out execerr.Outcome) (status int, typ, code, message string) {
	status, message = out.Status, out.Message
	switch out.Kind {
	case execerr.KindSessionDenial:
		// The denial's stable public code is the client-visible code; the type
		// tracks the HTTP class (401 missing principal, 503 self-service
		// storage/policy unavailability, 400 every other denial).
		code = out.SessionPublicCode
		if code == "" {
			code = "session_denied"
		}
		switch out.Status {
		case http.StatusUnauthorized:
			typ = "authentication_error"
		case http.StatusServiceUnavailable:
			typ = "server_error"
		default:
			typ = "invalid_request_error"
		}
	case execerr.KindPolicyDenied:
		typ, code = "permission_error", "policy_denied"
	case execerr.KindPolicyFailure:
		typ, code = "server_error", "policy_failure"
	case execerr.KindPolicyMalformed:
		typ, code = "server_error", "policy_malformed"
	case execerr.KindBillingDenied:
		typ, code = "rate_limit_error", "insufficient_credit"
	case execerr.KindBillingUnavailable:
		typ, code = "server_error", "billing_unavailable"
	case execerr.KindBackendUnavailable:
		typ, code = "server_error", "backend_unavailable"
	case execerr.KindClientReject:
		typ, code = "invalid_request_error", "invalid_request"
		if out.Status == http.StatusRequestEntityTooLarge {
			code = "context_length_exceeded"
		}
	default:
		// KindInternalError and any unclassified outcome: the shared message is
		// already wire-safe and the code stays generic.
		typ, code = "server_error", "internal_error"
		message = execerr.InternalWireMessage
	}
	return status, typ, code, message
}

func (WireErrors) WriteHookError(w http.ResponseWriter, err error) error {
	if se, ok := errors.AsType[*frontendpipe.StatusError](err); ok {
		writeWireError(w, se.HTTPStatus(), se.Type, se.Code, se.Message)
		return nil
	}
	writeWireError(w, http.StatusBadRequest, "invalid_request_error", "bad_request", "Invalid request")
	return nil
}
