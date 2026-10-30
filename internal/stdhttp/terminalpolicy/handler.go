// Package terminalpolicy provides the provider-neutral HTTP adapter for the
// process-owned terminal-decision policy.
package terminalpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
)

const defaultMaxBodyBytes int64 = 64 << 10

var (
	// ErrUnauthenticated identifies an authentication failure before a
	// principal or operator scope is available.
	ErrUnauthenticated = errors.New("terminal policy: unauthenticated")
	// ErrSecureSessionRequired identifies a client principal without current
	// secure-session authority.
	ErrSecureSessionRequired = errors.New("terminal policy: secure session required")
	// ErrForbidden identifies an authenticated principal without target scope.
	ErrForbidden = errors.New("terminal policy: forbidden")
	// ErrSessionNotFound identifies an authorized operator target that does not
	// exist. It is intentionally distinct from authorization failure.
	ErrSessionNotFound = errors.New("terminal policy: session not found")
)

// Options configures the narrow endpoint adapter. Authority callbacks are
// supplied by existing authentication and secure-session middleware; this
// package does not inspect credentials or create an authorization system.
type Options struct {
	Store *sessionpolicy.Store

	// FeatureStatus reports whether the generic feature is known and whether
	// its provider is currently active. A known but inactive feature remains
	// mountable and is reported with available=false.
	FeatureStatus func(context.Context, string) (known, available bool, err error)

	// ResolveClientScope resolves the current authoritative client scope. The
	// feature ID is supplied separately from the request path for callers that
	// bind authority to the admitted request.
	ResolveClientScope func(context.Context, *http.Request, string) (sessionpolicy.Key, sessionpolicy.Authority, error)

	// AuthorizeOperatorTarget validates an authenticated operator and target
	// session. The callback receives only bounded path identities.
	AuthorizeOperatorTarget func(context.Context, *http.Request, string, string) (sessionpolicy.Key, sessionpolicy.Authority, error)

	// GenerationDefault supplies the immutable generation default used by the
	// core store's effective-state calculation.
	GenerationDefault func(string) bool

	MaxBodyBytes int64
}

// NewHandler constructs the exact client and operator policy resources.
func NewHandler(opts Options) (http.Handler, error) {
	if opts.Store == nil {
		return nil, errors.New("terminal policy: store is required")
	}
	if opts.FeatureStatus == nil {
		return nil, errors.New("terminal policy: feature status is required")
	}
	if opts.ResolveClientScope == nil {
		return nil, errors.New("terminal policy: client scope resolver is required")
	}
	if opts.AuthorizeOperatorTarget == nil {
		return nil, errors.New("terminal policy: operator authorizer is required")
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = defaultMaxBodyBytes
	}
	if opts.GenerationDefault == nil {
		opts.GenerationDefault = func(string) bool { return false }
	}
	return &handler{opts: opts}, nil
}

type handler struct{ opts Options }

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.opts.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "policy_unavailable")
		return
	}
	if r == nil || r.URL == nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	route, ok := parseRoute(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}

	if r.Method == http.MethodPut {
		value, ok := decodePut(w, r, h.opts.MaxBodyBytes)
		if !ok {
			return
		}
		if err := h.serveMutation(w, r, route, value); err != nil {
			return
		}
		return
	}
	if err := h.serveMutation(w, r, route, nil); err != nil {
		return
	}
}

type route struct {
	operator  bool
	sessionID string
	featureID string
}

func parseRoute(path string) (route, bool) {
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return route{}, false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 5 && parts[0] == "v1" && parts[1] == "aip" && parts[2] == "session" && parts[3] == "features" {
		featureID := boundedPathPart(parts[4])
		return route{featureID: featureID}, featureID != ""
	}
	return parseOperatorRoute(parts)
}

func parseOperatorRoute(parts []string) (route, bool) {
	if len(parts) != 4 || parts[0] != "admin" || parts[1] != "session-features" {
		return route{}, false
	}
	sessionID := boundedPathPart(parts[2])
	featureID := boundedPathPart(parts[3])
	if sessionID == "" || featureID == "" {
		return route{}, false
	}
	return route{operator: true, sessionID: sessionID, featureID: featureID}, true
}

func boundedPathPart(part string) string {
	part = strings.TrimSpace(part)
	if part == "" || strings.ContainsAny(part, `/\\`) || !utf8.ValidString(part) {
		return ""
	}
	return part
}

func (h *handler) serveMutation(w http.ResponseWriter, r *http.Request, route route, enabled *bool) error {
	key, authority, err := h.resolveScope(r, route)
	if err != nil {
		writeAuthorizationError(w, route.operator, err)
		return err
	}
	known, available, err := h.opts.FeatureStatus(r.Context(), route.featureID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "policy_unavailable")
		return err
	}
	if !known {
		writeError(w, http.StatusNotFound, "feature_not_found")
		return nil
	}
	if key.FeatureID != route.featureID || (route.operator && key.SecureSessionIncarnation != route.sessionID) {
		writeAuthorizationError(w, route.operator, ErrForbidden)
		return ErrForbidden
	}
	defaultEnabled := h.opts.GenerationDefault(route.featureID)
	if r.Method == http.MethodGet {
		state, err := h.opts.Store.Snapshot(r.Context(), authority, key, defaultEnabled)
		if err != nil {
			writeStoreError(w, err)
			return err
		}
		writeJSON(w, http.StatusOK, responseFor(route, state, available, false))
		return nil
	}

	actor := sessionpolicy.ActorClient
	if route.operator {
		actor = sessionpolicy.ActorOperator
	}
	state := sessionpolicy.TriStateUnset
	if enabled != nil {
		state = sessionpolicy.TriStateEnabled
		if !*enabled {
			state = sessionpolicy.TriStateDisabled
		}
	}
	updated, err := h.opts.Store.Set(r.Context(), authority, key, actor, state)
	if err != nil {
		writeStoreError(w, err)
		return err
	}
	updated.EffectiveEnabled = effectiveEnabled(updated.ClientState, updated.OperatorState, defaultEnabled)
	writeJSON(w, http.StatusOK, responseFor(route, updated, available, true))
	return nil
}

func (h *handler) resolveScope(r *http.Request, route route) (sessionpolicy.Key, sessionpolicy.Authority, error) {
	if route.operator {
		return h.opts.AuthorizeOperatorTarget(r.Context(), r, route.sessionID, route.featureID)
	}
	return h.opts.ResolveClientScope(r.Context(), r, route.featureID)
}

type response struct {
	FeatureID     string `json:"feature_id"`
	Available     bool   `json:"available"`
	ClientState   string `json:"client_state"`
	OperatorState string `json:"operator_state,omitempty"`
	Effective     bool   `json:"effective_enabled"`
	Revision      uint64 `json:"revision"`
	AppliesFrom   string `json:"applies_from,omitempty"`
}

func responseFor(route route, state sessionpolicy.Snapshot, available, mutation bool) response {
	resp := response{
		FeatureID:   route.featureID,
		Available:   available,
		ClientState: string(state.ClientState),
		Effective:   state.EffectiveEnabled,
		Revision:    state.Revision,
	}
	if route.operator {
		resp.OperatorState = string(state.OperatorState)
	}
	if mutation {
		resp.AppliesFrom = "next_request"
	}
	return resp
}

func effectiveEnabled(client, operator sessionpolicy.TriState, generationDefault bool) bool {
	if client == sessionpolicy.TriStateDisabled || operator == sessionpolicy.TriStateDisabled {
		return false
	}
	if client == sessionpolicy.TriStateEnabled || operator == sessionpolicy.TriStateEnabled {
		return true
	}
	return generationDefault
}

type putBody struct {
	Enabled *bool `json:"enabled"`
}

func (b *putBody) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	start, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := start.(json.Delim); !ok || delim != '{' {
		return errors.New("object required")
	}
	seen := false
	for dec.More() {
		name, err := dec.Token()
		if err != nil {
			return err
		}
		field, ok := name.(string)
		if !ok || field != "enabled" || seen {
			return errors.New("exact enabled field required")
		}
		var enabled bool
		if err := dec.Decode(&enabled); err != nil {
			return err
		}
		b.Enabled = &enabled
		seen = true
	}
	end, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := end.(json.Delim); !ok || delim != '}' || !seen {
		return errors.New("exact enabled field required")
	}
	return nil
}

func decodePut(w http.ResponseWriter, r *http.Request, maxBytes int64) (*bool, bool) {
	if r.ContentLength > maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large")
		return nil, false
	}
	if r.Body == nil || r.ContentLength == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return nil, false
	}
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return nil, false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	dec.DisallowUnknownFields()
	var body putBody
	if err := dec.Decode(&body); err != nil || body.Enabled == nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request")
		}
		return nil, false
	}
	var extra struct{}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request")
		}
		return nil, false
	}
	return body.Enabled, true
}

func isBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

func writeAuthorizationError(w http.ResponseWriter, operator bool, err error) {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, ErrSecureSessionRequired):
		writeError(w, http.StatusForbidden, "secure_session_required")
	case errors.Is(err, ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "session_not_found")
	case errors.Is(err, ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, sessionpolicy.ErrUnauthorized):
		if operator {
			writeError(w, http.StatusForbidden, "forbidden")
		} else {
			writeError(w, http.StatusForbidden, "secure_session_required")
		}
	default:
		if operator {
			writeError(w, http.StatusForbidden, "forbidden")
		} else {
			writeError(w, http.StatusForbidden, "secure_session_required")
		}
	}
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sessionpolicy.ErrCapacity):
		writeError(w, http.StatusConflict, "policy_capacity")
	case errors.Is(err, sessionpolicy.ErrClosed):
		writeError(w, http.StatusServiceUnavailable, "policy_unavailable")
	case errors.Is(err, sessionpolicy.ErrUnauthorized):
		writeError(w, http.StatusForbidden, "forbidden")
	default:
		writeError(w, http.StatusServiceUnavailable, "policy_unavailable")
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

var _ http.Handler = (*handler)(nil)
