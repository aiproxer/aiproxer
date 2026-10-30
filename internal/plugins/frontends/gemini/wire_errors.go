package gemini

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
)

// WireErrors implements frontendpipe.WireErrors for Gemini generateContent errors.
type WireErrors struct{}

func (WireErrors) WriteBodyTooLarge(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusRequestEntityTooLarge, "request body too large")
}

func (WireErrors) WriteReadBodyFailed(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusBadRequest, "could not read request body")
}

func (WireErrors) WriteExecutorNotConfigured(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusInternalServerError, "executor not configured")
}

func (WireErrors) WritePreflightCanceled(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusServiceUnavailable, execerr.InternalWireMessage)
}

func (WireErrors) WriteInvalidJSON(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusBadRequest, "invalid request JSON")
}

func (WireErrors) WriteAdmissionReject(w http.ResponseWriter, d decodeqos.Decision) error {
	return WriteErrorJSON(w, d.Status, d.Message)
}

func (WireErrors) WriteInvalidRequest(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusBadRequest, "invalid request")
}

func (WireErrors) WriteEncodeFailed(w http.ResponseWriter) error {
	return WriteErrorJSON(w, http.StatusInternalServerError, execerr.InternalWireMessage)
}

func (WireErrors) WriteExecuteError(w http.ResponseWriter, out execerr.Outcome) error {
	status, msg, gstatus := frontendpipe.MapGeminiExecuteError(out)
	return WriteErrorJSONWithStatus(w, status, msg, gstatus)
}
