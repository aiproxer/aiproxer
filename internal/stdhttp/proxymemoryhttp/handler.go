// Package proxymemoryhttp is the standard-HTTP adapter of the proxy-memory
// feature: the authenticated REST controls of design §11.2 and the MCP tool
// endpoint of design §11.3.
//
// The adapter never re-sanitizes or reshapes service output. The service
// returns Redact fixpoints already (design §11.1), so this package only maps
// them onto wire shapes, and it never reveals whether memory exists: a missing
// artifact, another owner's artifact and an unknown path all answer with the
// same content-free not-found document.
package proxymemoryhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// Route templates of design §11.2, mounted verbatim by the standard HTTP
// composition (§14.4).
const (
	mountPath = "/v1/aip/memory"
	mcpPath   = mountPath + "/mcp"
)

// handler serves the memory REST controls and the MCP endpoint.
type handler struct {
	svc     *proxymemory.Service
	metrics *proxymemory.Metrics
}

// NewHandler returns the memory HTTP handler for the mounted routes, or nil
// when svc is nil so the caller skips the mount entirely.
//
// The optional metrics receives the two events owned by this layer: tool_call
// for every successful MCP tool call (design §11.3) and store_unavailable for
// every 500-equivalent failure (design §Error Handling). It is nil-safe, like
// the optional telemetry parameter of
// reasoningpreservation.NewStreamObserverFactory; the service surface of
// design §11.1 exposes no counter, so this constructor is the seam that
// supplies it.
func NewHandler(svc *proxymemory.Service, metrics ...*proxymemory.Metrics) http.Handler {
	if svc == nil {
		return nil
	}
	var m *proxymemory.Metrics
	if len(metrics) > 0 {
		m = metrics[0]
	}
	return &handler{svc: svc, metrics: m}
}

// ServeHTTP routes one mounted memory request. Authentication and eligibility
// precede routing, so an unauthenticated caller gets 401 and a non-eligible
// caller gets the same 404 as an unknown path, learning nothing about memory
// (design §11.2, Requirement 7.6).
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	sc, ok := httpauth.ScopeFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	p, ok := h.svc.Caller(sc)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}

	route, scopeID := memoryRoute(r.URL.Path)
	switch route {
	case routeCollection:
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		h.handleList(w, r, p)
	case routeItem:
		switch r.Method {
		case http.MethodGet:
			h.handleStatus(w, r, p, scopeID)
		case http.MethodDelete:
			h.handleForget(w, r, p, scopeID)
		default:
			writeMethodNotAllowed(w, http.MethodGet, http.MethodDelete)
		}
	case routeMCP:
		h.handleMCP(w, r, p)
	case routeSettings:
		switch r.Method {
		case http.MethodGet:
			h.handleSettings(w, r, p)
		case http.MethodPut:
			h.handleSettingsUpdate(w, r, p)
		case http.MethodDelete:
			h.handleSettingsDelete(w, r, p)
		default:
			writeMethodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodDelete)
		}
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}

// routeKind is the closed set of mounted route templates (design §11.2).
type routeKind int

const (
	routeUnknown routeKind = iota
	routeCollection
	routeItem
	routeMCP
	routeSettings
)

// memoryRoute classifies one request path against the mounted templates: the
// collection, the MCP endpoint, the settings resource of design §4, and one
// item route whose single trailing segment is the opaque scope identifier.
// Everything else is unknown.
//
// The settings segment is classified before the per-scope item route, so a
// settings request can never be parsed as a scope identifier.
func memoryRoute(path string) (routeKind, string) {
	rest, ok := strings.CutPrefix(path, mountPath)
	if !ok {
		return routeUnknown, ""
	}
	switch {
	case rest == "":
		return routeCollection, ""
	case rest == "/mcp":
		return routeMCP, ""
	case rest == settingsSegment:
		return routeSettings, ""
	case strings.HasPrefix(rest, "/") && rest != "/" && !strings.Contains(rest[1:], "/"):
		return routeItem, rest[1:]
	default:
		return routeUnknown, ""
	}
}

// bankJSON is one bank status on the wire (design §11.2). Times are RFC 3339
// UTC.
type bankJSON struct {
	Scope           string `json:"scope"`
	ProjectLabel    string `json:"project_label"`
	Version         int64  `json:"version"`
	PublishedAt     string `json:"published_at"`
	PendingSessions int    `json:"pending_sessions"`
	Extractions     int    `json:"extractions"`
	PendingNotes    int    `json:"pending_notes"`
}

// bankDetailJSON is one bank status plus its published summary.
type bankDetailJSON struct {
	bankJSON
	MemorySummary string `json:"memory_summary"`
}

// banksJSON is the list envelope; Banks is always an array, never null.
type banksJSON struct {
	Banks []bankJSON `json:"banks"`
}

func toBankJSON(s proxymemory.BankStatus) bankJSON {
	return bankJSON{
		Scope:           s.ScopeID,
		ProjectLabel:    s.ProjectLabel,
		Version:         s.Version,
		PublishedAt:     time.UnixMilli(s.PublishedAtMS).UTC().Format(time.RFC3339),
		PendingSessions: s.PendingSessions,
		Extractions:     s.Extractions,
		PendingNotes:    s.PendingNotes,
	}
}

func (h *handler) handleList(w http.ResponseWriter, r *http.Request, p proxymemory.Principal) {
	banks, err := h.svc.List(r.Context(), p)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	out := make([]bankJSON, 0, len(banks))
	for _, b := range banks {
		out = append(out, toBankJSON(b))
	}
	writeJSON(w, http.StatusOK, banksJSON{Banks: out})
}

func (h *handler) handleStatus(w http.ResponseWriter, r *http.Request, p proxymemory.Principal, scopeID string) {
	status, summary, err := h.svc.Status(r.Context(), p, scopeID)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bankDetailJSON{bankJSON: toBankJSON(status), MemorySummary: summary})
}

func (h *handler) handleForget(w http.ResponseWriter, r *http.Request, p proxymemory.Principal, scopeID string) {
	if err := h.svc.Forget(r.Context(), p, scopeID); err != nil {
		h.writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeServiceError maps one service failure onto the REST statuses of design
// §11.2 and §Error Handling. One body serves an unknown artifact and another
// owner's artifact alike: the response is no existence oracle.
func (h *handler) writeServiceError(w http.ResponseWriter, err error) {
	var policyErr *proxymemory.PolicyError
	switch {
	case errors.Is(err, proxymemory.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, proxymemory.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_input")
	case errors.As(err, &policyErr):
		writePolicyError(w, policyErr)
	default:
		// Every other store failure is served as a content-free outage and
		// counted; no error text reaches the client.
		h.metrics.Inc(proxymemory.EventStoreUnavailable)
		writeError(w, http.StatusInternalServerError, "unavailable")
	}
}

func writeMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
