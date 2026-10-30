package systemone

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// WireErrors renders System One error envelopes for the shared create pipeline.
type WireErrors struct{}

func (wire WireErrors) WriteDecodeError(w http.ResponseWriter, err error) error {
	return wire.WriteExecuteError(w, classifyExecute(err))
}

func writeJSON(w http.ResponseWriter, status int, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(data)
	return err
}

func writeDetail(w http.ResponseWriter, status int, message string) error {
	return writeJSON(w, status, map[string]string{"detail": message})
}

func classifyExecute(err error) execerr.Outcome {
	if aipapi.IsDecisionReject(err) {
		return execerr.Outcome{Kind: execerr.KindClientReject, Status: http.StatusUnprocessableEntity, Err: err}
	}
	return execerr.ClassifyExecute(err)
}

func (WireErrors) WriteExecuteError(w http.ResponseWriter, outcome execerr.Outcome) error {
	if rejection, ok := errors.AsType[*aipapi.DecisionRejectError](outcome.Err); ok {
		field := aipapi.NormalizeClientMessage(rejection.Field)
		if field == "" {
			field = "body"
		}
		return writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []any{map[string]any{
			"loc": strings.Split(field, "."), "msg": aipapi.NormalizeClientMessage(rejection.Message), "type": "value_error",
		}}})
	}
	return writeDetail(w, outcome.Status, outcome.Message)
}

func (WireErrors) WriteBodyTooLarge(w http.ResponseWriter) error {
	return writeDetail(w, http.StatusRequestEntityTooLarge, "request body too large")
}

func (WireErrors) WriteReadBodyFailed(w http.ResponseWriter) error {
	return writeDetail(w, http.StatusBadRequest, "could not read request body")
}

func (WireErrors) WriteExecutorNotConfigured(w http.ResponseWriter) error {
	return writeDetail(w, http.StatusServiceUnavailable, "executor not configured")
}

func (WireErrors) WritePreflightCanceled(w http.ResponseWriter) error {
	return writeDetail(w, http.StatusRequestTimeout, "request canceled")
}

func (wire WireErrors) WriteInvalidJSON(w http.ResponseWriter) error {
	return wire.WriteExecuteError(w, classifyExecute(reject("body", "invalid JSON request")))
}

func (WireErrors) WriteAdmissionReject(w http.ResponseWriter, decision decodeqos.Decision) error {
	return writeDetail(w, decision.Status, "request admission unavailable")
}

func (wire WireErrors) WriteInvalidRequest(w http.ResponseWriter) error {
	return wire.WriteExecuteError(w, classifyExecute(reject("body", "invalid decision request")))
}

func (WireErrors) WriteEncodeFailed(w http.ResponseWriter) error {
	return writeDetail(w, http.StatusInternalServerError, "could not encode decision response")
}
