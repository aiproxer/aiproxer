package routeoverride

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Options configures the GET/PUT/DELETE handler.
type Options struct {
	Service      routeoverride.CommandService
	MaxBodyBytes int64
	Log          *slog.Logger
}

// PutBody is the PUT JSON document. Exactly one field is accepted.
type PutBody struct {
	Selector string `json:"selector"`
}

// StateDTO is the protected GET/PUT/DELETE response. Selector is omitted when
// inactive. UpdatedAt is omitted only for never-mutated revision 0.
type StateDTO struct {
	ALegID    string     `json:"a_leg_id"`
	Active    bool       `json:"active"`
	Selector  string     `json:"selector,omitempty"`
	Revision  int64      `json:"revision"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// Handler is the generation-bound routing-override HTTP resource.
type Handler struct {
	service      routeoverride.CommandService
	maxBodyBytes int64
	log          *slog.Logger
}

// StateToDTO maps domain state onto the wire DTO.
func StateToDTO(s routeoverride.State) StateDTO {
	dto := StateDTO{
		ALegID:   s.ALegID,
		Active:   s.Active,
		Revision: s.Revision,
	}
	if s.Active {
		dto.Selector = s.Selector
	}
	if s.Revision != 0 && !s.UpdatedAt.IsZero() {
		ts := s.UpdatedAt.UTC()
		dto.UpdatedAt = &ts
	}
	return dto
}

// NewHandler returns the protected routing-override resource.
func NewHandler(opts Options) (http.Handler, error) {
	if opts.Service == nil {
		return nil, fmt.Errorf("stdhttp/admin/routeoverride: service is required")
	}
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = config.DefaultRoutingOverrideAdminMaxBodyBytes
	}
	return &Handler{service: opts.Service, maxBodyBytes: maxBody, log: opts.Log}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	if r == nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	aLegID, ok := parseALegID(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r, aLegID)
	case http.MethodPut:
		h.handlePut(w, r, aLegID)
	case http.MethodDelete:
		h.handleDelete(w, r, aLegID)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request, aLegID string) {
	st, err := h.service.Get(r.Context(), aLegID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, StateToDTO(st))
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request, aLegID string) {
	st, err := h.service.Clear(r.Context(), aLegID)
	if err != nil {
		h.audit(r.Context(), "clear", "error", aLegID, routeoverride.State{ALegID: aLegID})
		writeServiceError(w, err)
		return
	}
	action := "clear"
	if !st.Active && st.Revision == 0 {
		action = "noop"
	}
	h.audit(r.Context(), action, "ok", aLegID, st)
	writeJSON(w, http.StatusOK, StateToDTO(st))
}

func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request, aLegID string) {
	if r.ContentLength > h.maxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	if err := requireJSONContentType(r); err != nil {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	body := http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	var put PutBody
	if err := dec.Decode(&put); err != nil {
		writeJSONBodyError(w, err)
		return
	}
	switch err := dec.Decode(&struct{}{}); {
	case errors.Is(err, io.EOF):
		// exactly one JSON value
	case err == nil:
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	default:
		writeJSONBodyError(w, err)
		return
	}
	normalized := routeoverride.NormalizeSelector(put.Selector)
	if normalized == "" {
		writeError(w, http.StatusBadRequest, "invalid_selector")
		return
	}
	if len(normalized) > aipapi.MaxRouteSelectorBytes {
		writeError(w, http.StatusBadRequest, "invalid_selector")
		return
	}
	st, err := h.service.Replace(r.Context(), aLegID, normalized)
	if err != nil {
		h.audit(r.Context(), "replace", "error", aLegID, routeoverride.State{ALegID: aLegID})
		writeServiceError(w, err)
		return
	}
	action := "replace"
	if st.Active && st.Revision == 1 {
		action = "set"
	}
	h.audit(r.Context(), action, "ok", aLegID, st)
	writeJSON(w, http.StatusOK, StateToDTO(st))
}

func (h *Handler) audit(ctx context.Context, action, outcome, aLegID string, st routeoverride.State) {
	if h == nil {
		return
	}
	bytes := 0
	if st.Active {
		bytes = len(st.Selector)
	}
	diag.LogRouteOverrideMutation(ctx, h.log, diag.RouteOverrideMutation{
		Action:        action,
		Outcome:       outcome,
		Revision:      st.Revision,
		ALegID:        aLegID,
		Selector:      st.Selector,
		SelectorBytes: bytes,
		Active:        st.Active,
	})
}

func parseALegID(path string) (string, bool) {
	p := strings.Trim(path, "/")
	if p == "" || strings.Contains(p, "/") {
		return "", false
	}
	decoded, err := url.PathUnescape(p)
	if err != nil {
		return "", false
	}
	decoded = strings.TrimSpace(decoded)
	if decoded == "" {
		return "", false
	}
	return decoded, true
}

func requireJSONContentType(r *http.Request) error {
	raw := strings.TrimSpace(r.Header.Get("Content-Type"))
	if raw == "" {
		return errUnsupportedMedia
	}
	mt, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return errUnsupportedMedia
	}
	if !strings.EqualFold(mt, "application/json") {
		return errUnsupportedMedia
	}
	return nil
}

var errUnsupportedMedia = errors.New("unsupported media type")

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, routeoverride.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, routeoverride.ErrInvalidSelector):
		writeError(w, http.StatusBadRequest, "invalid_selector")
	case errors.Is(err, routeoverride.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "store_unavailable")
	case errors.Is(err, routeoverride.ErrRevisionExhausted):
		writeError(w, http.StatusInternalServerError, "revision_exhausted")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "store_unavailable")
	default:
		writeError(w, http.StatusServiceUnavailable, "store_unavailable")
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSONBodyError(w http.ResponseWriter, err error) {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
