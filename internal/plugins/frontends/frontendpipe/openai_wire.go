package frontendpipe

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openaiwire"
)

// OpenAIWire implements WireErrors for OpenAI Responses and legacy Chat Completions shapes.
type OpenAIWire struct{}

func (OpenAIWire) WriteBodyTooLarge(w http.ResponseWriter) error {
	return openaiwire.WriteErrorJSON(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "")
}

func (OpenAIWire) WriteReadBodyFailed(w http.ResponseWriter) error {
	return openaiwire.WriteErrorJSON(w, http.StatusBadRequest, "could not read request body", "invalid_request_error", "")
}

func (OpenAIWire) WriteExecutorNotConfigured(w http.ResponseWriter) error {
	return openaiwire.WriteErrorJSON(w, http.StatusInternalServerError, "executor not configured", "api_error", "")
}

func (OpenAIWire) WritePreflightCanceled(w http.ResponseWriter) error {
	return openaiwire.WriteErrorJSON(w, http.StatusServiceUnavailable, execerr.InternalWireMessage, "api_error", "")
}

func (OpenAIWire) WriteInvalidJSON(w http.ResponseWriter) error {
	return openaiwire.WriteErrorJSON(w, http.StatusBadRequest, "invalid request JSON", "invalid_request_error", "")
}

func (OpenAIWire) WriteAdmissionReject(w http.ResponseWriter, d decodeqos.Decision) error {
	return openaiwire.WriteErrorJSON(w, d.Status, d.Message, "api_error", "")
}

func (OpenAIWire) WriteInvalidRequest(w http.ResponseWriter) error {
	return openaiwire.WriteErrorJSON(w, http.StatusBadRequest, "invalid request", "invalid_request_error", "")
}

func (OpenAIWire) WriteEncodeFailed(w http.ResponseWriter) error {
	return openaiwire.WriteErrorJSON(w, http.StatusInternalServerError, execerr.InternalWireMessage, "api_error", "")
}

func (OpenAIWire) WriteExecuteError(w http.ResponseWriter, out execerr.Outcome) error {
	switch out.Kind {
	case execerr.KindSessionDenial, execerr.KindPolicyDenied, execerr.KindPolicyFailure, execerr.KindPolicyMalformed:
		return openaiwire.WriteErrorJSON(w, out.Status, out.Message, execerr.OpenAIWireErrorType(out.Status), "")
	case execerr.KindBillingDenied:
		return openaiwire.WriteErrorJSON(w, out.Status, out.Message, "insufficient_quota", "insufficient_quota")
	case execerr.KindBillingUnavailable, execerr.KindBackendUnavailable:
		return openaiwire.WriteErrorJSON(w, out.Status, out.Message, "api_error", "")
	case execerr.KindClientReject:
		code := "unsupported_parameter"
		if out.Status == http.StatusRequestEntityTooLarge {
			code = ""
		}
		return openaiwire.WriteErrorJSON(w, out.Status, out.Message, "invalid_request_error", code)
	default:
		return openaiwire.WriteErrorJSON(w, out.Status, execerr.InternalWireMessage, "api_error", "")
	}
}

// MapOpenAIExecuteError returns wire fields for OpenAI-shaped execute errors (for tests).
func MapOpenAIExecuteError(out execerr.Outcome) (status int, message, errType, code string) {
	status = out.Status
	message = out.Message
	switch out.Kind {
	case execerr.KindSessionDenial, execerr.KindPolicyDenied, execerr.KindPolicyFailure, execerr.KindPolicyMalformed:
		errType = execerr.OpenAIWireErrorType(out.Status)
	case execerr.KindBillingDenied:
		errType = "insufficient_quota"
		code = "insufficient_quota"
	case execerr.KindBillingUnavailable, execerr.KindBackendUnavailable:
		errType = "api_error"
	case execerr.KindClientReject:
		errType = "invalid_request_error"
		code = "unsupported_parameter"
		if out.Status == http.StatusRequestEntityTooLarge {
			code = ""
		}
	default:
		message = execerr.InternalWireMessage
		errType = "api_error"
	}
	return status, message, errType, code
}
