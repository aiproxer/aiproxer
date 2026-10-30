package stdhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Session-identity header names used by the A-leg cancel endpoint.
// Sync tests in mount_constants_test.go verify alignment with the
// canonical values in the sessionwire package.
const (
	headerAuthoritativeSessionID = aipsdk.HeaderSessionID
	headerResumeToken            = aipsdk.HeaderResumeToken
)

const aLegCancelPrefix = "/aip/v1/a-legs/"

func mountALegCancel(mux *http.ServeMux, frontends HTTPFrontendInput) {
	exec := frontends.Executor
	mux.HandleFunc(aLegCancelPrefix, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id, ok := aLegIDFromCancelPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		err := exec.CancelALeg(r.Context(), aipapi.ALegCancelRequest{
			ALegID:      id,
			SessionID:   frontends.HTTPHeaders.SessionIDValue(r.Header),
			ResumeToken: frontends.HTTPHeaders.ResumeTokenValue(r.Header),
			FrontendID:  "aip",
			Reason:      "proxy_cancel",
		})
		if err != nil {
			status, msg, typ := cancelErrorWire(err)
			_ = writeOpenAIErrorJSON(w, status, msg, typ)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			ALegID string `json:"a_leg_id"`
			Status string `json:"status"`
		}{ALegID: id, Status: "cancelled"})
	})
}

func aLegIDFromCancelPath(path string) (string, bool) {
	rest := strings.TrimPrefix(strings.TrimSpace(path), aLegCancelPrefix)
	if rest == path {
		return "", false
	}
	rest = strings.Trim(rest, "/")
	id, suffix, ok := strings.Cut(rest, "/")
	if !ok || suffix != "cancel" || strings.TrimSpace(id) == "" || strings.Contains(suffix, "/") {
		return "", false
	}
	return id, true
}

func cancelErrorWire(err error) (int, string, string) {
	switch {
	case errors.Is(err, domain.ErrMissingPrincipal):
		return http.StatusUnauthorized, "missing cancellation identity", "authentication_error"
	case errors.Is(err, domain.ErrOwnerMismatch):
		return http.StatusForbidden, "cancellation forbidden", "invalid_request_error"
	case errors.Is(err, domain.ErrSessionNotFound):
		return http.StatusNotFound, "cancellation target not found", "invalid_request_error"
	default:
		return http.StatusInternalServerError, "internal error", "api_error"
	}
}

func writeOpenAIErrorJSON(w http.ResponseWriter, status int, message, errType string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}{
		Error: struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}{Message: message, Type: errType},
	})
}
