package anthropic

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
)

// WireErrors implements frontendpipe.WireErrors for Anthropic Messages API errors.
type WireErrors struct{}

func (WireErrors) WriteBodyTooLarge(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error")
}

func (WireErrors) WriteReadBodyFailed(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusBadRequest, "could not read request body", "invalid_request_error")
}

func (WireErrors) WriteExecutorNotConfigured(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusInternalServerError, "executor not configured", "api_error")
}

func (WireErrors) WritePreflightCanceled(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusServiceUnavailable, execerr.InternalWireMessage, "api_error")
}

func (WireErrors) WriteInvalidJSON(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusBadRequest, "invalid request JSON", "invalid_request_error")
}

func (WireErrors) WriteAdmissionReject(w http.ResponseWriter, d decodeqos.Decision) error {
	return WriteErrorJSON(w, d.Status, d.Message, "api_error")
}

func (WireErrors) WriteInvalidRequest(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusBadRequest, "invalid request", "invalid_request_error")
}

func (WireErrors) WriteEncodeFailed(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusInternalServerError, execerr.InternalWireMessage, "api_error")
}

func (WireErrors) WriteExecuteError(w http.ResponseWriter, out execerr.Outcome) error {
	status, msg, errType := frontendpipe.MapAnthropicExecuteError(out)
	return WriteErrorJSON(w, status, msg, errType)
}
