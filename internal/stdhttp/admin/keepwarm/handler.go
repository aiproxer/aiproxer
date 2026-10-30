package keepwarm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/aiproxer/aiproxer/internal/jsonbody"
	keepwarm "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
)

type Service interface {
	Disable(string) (keepwarm.SessionPolicy, error)
	Clear(string) error
	Get(string) (keepwarm.SessionPolicy, bool)
}

type Options struct {
	Enabled      bool
	MaxBodyBytes int64
	Service      Service
	// ResolveALegID must derive the A-leg from authenticated proxy/session
	// authority. The request body is never trusted for policy identity.
	ResolveALegID func(context.Context, *http.Request) (string, error)
	Audit         func(context.Context, string, string)
}

type request struct {
	// ALegID is accepted only for diagnostics in tests/adapters that explicitly
	// resolve it; production ResolveALegID remains authoritative.
	ALegID string `json:"a_leg_id,omitempty"`
}

type stateResponse struct {
	Disabled bool   `json:"disabled"`
	Revision uint64 `json:"revision,omitempty"`
}

func NewHandler(opts Options) http.Handler {
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 64 << 10
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !opts.Enabled {
			writeError(w, http.StatusNotFound, "disabled")
			return
		}
		if opts.Service == nil || opts.ResolveALegID == nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		aLegID, err := opts.ResolveALegID(r.Context(), r)
		if err != nil || strings.TrimSpace(aLegID) == "" {
			writeError(w, http.StatusForbidden, "unauthorized")
			return
		}
		aLegID = strings.TrimSpace(aLegID)
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != "/" && r.URL.Path != "" && !strings.HasPrefix(r.URL.Path, "/state/") {
				http.NotFound(w, r)
				return
			}
			state, ok := opts.Service.Get(aLegID)
			if !ok {
				writeJSON(w, http.StatusOK, stateResponse{})
				return
			}
			writeJSON(w, http.StatusOK, stateResponse{Disabled: state.Disabled, Revision: state.Revision})
		case http.MethodPost, http.MethodPut:
			if r.URL.Path != "/disable" && !strings.HasPrefix(r.URL.Path, "/disable/") {
				http.NotFound(w, r)
				return
			}
			if err := decodeBounded(w, r, maxBody); err != nil {
				return
			}
			state, err := opts.Service.Disable(aLegID)
			if err != nil {
				writePolicyError(w, err)
				return
			}
			if opts.Audit != nil {
				opts.Audit(r.Context(), "disable", aLegID)
			}
			writeJSON(w, http.StatusOK, stateResponse{Disabled: state.Disabled, Revision: state.Revision})
		case http.MethodDelete:
			if r.URL.Path != "/disable" && r.URL.Path != "/clear" && !strings.HasPrefix(r.URL.Path, "/clear/") && !strings.HasPrefix(r.URL.Path, "/disable/") {
				http.NotFound(w, r)
				return
			}
			if err := opts.Service.Clear(aLegID); err != nil {
				writePolicyError(w, err)
				return
			}
			if opts.Audit != nil {
				opts.Audit(r.Context(), "clear", aLegID)
			}
			writeJSON(w, http.StatusOK, stateResponse{})
		default:
			w.Header().Set("Allow", "GET, POST, PUT, DELETE")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	})
}

func decodeBounded(w http.ResponseWriter, r *http.Request, max int64) error {
	if err := jsonbody.Decode(w, r, &request{}, jsonbody.Policy{
		MaxBytes:   max,
		AllowEmpty: true,
	}); err != nil {
		if !errors.Is(err, context.Canceled) {
			writeError(w, http.StatusBadRequest, "invalid_json")
		}
		return err
	}
	return nil
}

// PathALegID resolves the validated A-leg authority encoded by an
// authenticated admin route. It intentionally never reads a request body.
func PathALegID(_ context.Context, r *http.Request) (string, error) {
	if r == nil {
		return "", keepwarm.ErrInvalidConfig
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 2 || (parts[0] != "disable" && parts[0] != "clear" && parts[0] != "state") {
		return "", keepwarm.ErrInvalidConfig
	}
	id, err := url.PathUnescape(parts[1])
	if err != nil || strings.TrimSpace(id) == "" || len(id) > 256 || strings.ContainsAny(id, "/\\") {
		return "", keepwarm.ErrInvalidConfig
	}
	return strings.TrimSpace(id), nil
}

func writePolicyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, keepwarm.ErrPolicyCapacity):
		writeError(w, http.StatusConflict, "policy_capacity")
	case errors.Is(err, keepwarm.ErrPolicyNotFound):
		writeError(w, http.StatusNotFound, "policy_not_found")
	default:
		writeError(w, http.StatusServiceUnavailable, "policy_unavailable")
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
