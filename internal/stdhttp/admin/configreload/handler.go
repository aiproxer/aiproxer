package configreload

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// Handler serves fixed reload/status paths against a ReloadCoordinator.
type Handler struct {
	opts  Options
	coord ReloadCoordinator
}

// NewHandler constructs the management HTTP handler. opts must already Validate.
func NewHandler(opts Options, coord ReloadCoordinator) (*Handler, error) {
	if coord == nil {
		return nil, errors.New("configreload management: nil ReloadCoordinator")
	}
	resolved := opts.resolved()
	if err := resolved.Validate(); err != nil {
		return nil, err
	}
	return &Handler{opts: resolved, coord: coord}, nil
}

// ServeHTTP routes fixed reload and status paths only.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case ReloadPath:
		h.handleReload(w, r)
	case StatusPath:
		h.handleStatus(w, r)
	default:
		http.NotFound(w, r)
	}
}

// Mux returns a ServeMux mounting only the fixed management paths.
func (h *Handler) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(ReloadPath, http.HandlerFunc(h.handleReload))
	mux.Handle(StatusPath, http.HandlerFunc(h.handleStatus))
	return mux
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorize(w, r) || !h.browserGuard(w, r, true) {
		return
	}
	writeJSON(w, http.StatusOK, statusDTO(h.coord.Status(), h.coord.FixedSourcePath()))
}

func (h *Handler) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		// Preflight never authorizes and never triggers reload (req 12.7).
		writeCategory(w, http.StatusForbidden, "preflight-rejected")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorize(w, r) || !h.browserGuard(w, r, false) {
		return
	}
	if err := h.validateBody(r); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, ResultDTO{
			Category:       string(sdkreload.ResultInvalid),
			ReasonCategory: err.Error(),
		})
		return
	}
	// Fixed source only: coordinator re-reads the startup path (req 1.7, 12.4).
	// Host-owned context: client cancel must not abort an accepted attempt (req 12.9).
	hostCtx := context.WithoutCancel(r.Context())
	if hostCtx == nil {
		hostCtx = context.Background()
	}
	res := h.coord.Reload(hostCtx, sdkreload.Trigger{
		Kind:       sdkreload.TriggerAPI,
		AcceptedAt: time.Now().UTC(),
		SafeActor:  "management-api",
	})
	writeJSON(w, HTTPStatusFor(res.Category), resultDTO(res))
}

func (h *Handler) validateBody(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	limit := h.opts.MaxBodyBytes
	if limit <= 0 {
		limit = DefaultMaxBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return errors.New("body-read-failed")
	}
	if int64(len(body)) > limit {
		return errors.New("body-too-large")
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}
	if ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return errors.New("wrong-content-type")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return errors.New("invalid-json")
	}
	if len(obj) == 0 {
		return nil
	}
	for _, k := range []string{"path", "config", "yaml", "url", "source", "command", "plugin", "install"} {
		if _, ok := obj[k]; ok {
			return errors.New("source-override-forbidden")
		}
	}
	return errors.New("non-empty-body")
}
