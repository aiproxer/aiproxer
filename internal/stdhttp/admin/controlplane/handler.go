// Package controlplane mounts the protected operator status and query HTTP
// surface for the control-plane persistence/query/event-ledger capability
// (spec control-plane-persistence-query-event-ledger; tasks 5.3, 5.4).
//
// The handler mounts only when control-plane query exposure is explicitly
// enabled and the diagnostics shared-secret posture allows it (enforced by the
// composition root, which wraps this handler with diag.WrapDiagnosticsProtect).
// It exposes safe JSON status and bounded query pages; it never becomes a
// client-facing LLM protocol response path and never leaks raw DSNs, SQL,
// driver text, or privileged raw evidence (requirements 2.1–2.9, 4.6, 4.7,
// 7.1, 7.4, 8.6, 9.1, 9.4, 10.4, 10.5).
package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Options configures a protected control-plane HTTP handler.
type Options struct {
	// Queries is the bounded query service. When nil, every route returns 404
	// so the handler is inert when the capability is disabled or unavailable.
	Queries cp.Queries
	// ReadinessReport, when non-nil, exposes independent authority/journal
	// readiness at /readiness (requirements 15.7, 15.8).
	ReadinessReport cp.ReadinessReportReader
	// DefaultVisibility is applied when a request omits the visibility query
	// parameter. Empty defaults to cp.VisibilityDefault so privileged raw
	// evidence is not surfaced (requirement 4.6, 6.5).
	DefaultVisibility cp.Visibility
}

// NewHandler returns an http.Handler that exposes the protected control-plane
// status and query routes under base. base must be a non-empty absolute path
// prefix (no trailing slash); routes are mounted as base, base/status,
// base/sessions, base/attempts, base/usage, base/usage/aggregate,
// base/policy-audit, and base/events.
func NewHandler(opts Options) http.Handler {
	queries := opts.Queries
	readiness := opts.ReadinessReport
	defaultVisibility := opts.DefaultVisibility
	if defaultVisibility == "" {
		defaultVisibility = cp.VisibilityDefault
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if queries == nil {
			writeControlPlaneError(w, http.StatusNotFound, cp.ErrCodeDisabled)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeControlPlaneError(w, http.StatusMethodNotAllowed, cp.ErrCodeMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if queries == nil {
			writeControlPlaneError(w, http.StatusNotFound, cp.ErrCodeDisabled)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeControlPlaneError(w, http.StatusMethodNotAllowed, cp.ErrCodeMethodNotAllowed)
			return
		}
		status, err := queries.Status(r.Context())
		if err != nil {
			writeQueryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
	mux.HandleFunc("/readiness", func(w http.ResponseWriter, r *http.Request) {
		if readiness == nil {
			writeControlPlaneError(w, http.StatusNotFound, cp.ErrCodeDisabled)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeControlPlaneError(w, http.StatusMethodNotAllowed, cp.ErrCodeMethodNotAllowed)
			return
		}
		report, err := readiness.Report(r.Context())
		if err != nil {
			writeQueryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, report)
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		servePage(w, r, queries, defaultVisibility, func(vis cp.Visibility, common cp.CommonFilters, limit int, cursor cp.Cursor) (any, error) {
			return queries.Sessions(r.Context(), cp.SessionQuery{
				Common:     common,
				Limit:      limit,
				Cursor:     cursor,
				Visibility: vis,
			})
		})
	})
	mux.HandleFunc("/attempts", func(w http.ResponseWriter, r *http.Request) {
		servePage(w, r, queries, defaultVisibility, func(vis cp.Visibility, common cp.CommonFilters, limit int, cursor cp.Cursor) (any, error) {
			q := cp.AttemptQuery{
				Common:     common,
				Surfaced:   strings.TrimSpace(r.URL.Query().Get("surfaced")),
				Limit:      limit,
				Cursor:     cursor,
				Visibility: vis,
			}
			return queries.Attempts(r.Context(), q)
		})
	})
	mux.HandleFunc("/usage/aggregate", func(w http.ResponseWriter, r *http.Request) {
		servePage(w, r, queries, defaultVisibility, func(vis cp.Visibility, common cp.CommonFilters, limit int, cursor cp.Cursor) (any, error) {
			q := cp.UsageAggregateQuery{
				Common:     common,
				GroupBy:    parseCSV(r.URL.Query().Get("group_by")),
				Limit:      limit,
				Cursor:     cursor,
				Visibility: vis,
			}
			return queries.UsageAggregate(r.Context(), q)
		})
	})
	mux.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		servePage(w, r, queries, defaultVisibility, func(vis cp.Visibility, common cp.CommonFilters, limit int, cursor cp.Cursor) (any, error) {
			q := cp.UsageQuery{
				Common:       common,
				Plane:        strings.TrimSpace(r.URL.Query().Get("plane")),
				Availability: strings.TrimSpace(r.URL.Query().Get("availability")),
				Limit:        limit,
				Cursor:       cursor,
				Visibility:   vis,
			}
			return queries.Usage(r.Context(), q)
		})
	})
	mux.HandleFunc("/policy-audit", func(w http.ResponseWriter, r *http.Request) {
		servePage(w, r, queries, defaultVisibility, func(vis cp.Visibility, common cp.CommonFilters, limit int, cursor cp.Cursor) (any, error) {
			q := cp.EvidenceQuery{
				Common:     common,
				Effect:     strings.TrimSpace(r.URL.Query().Get("effect")),
				Category:   cp.Category(strings.TrimSpace(r.URL.Query().Get("category"))),
				Limit:      limit,
				Cursor:     cursor,
				Visibility: vis,
			}
			return queries.PolicyAudit(r.Context(), q)
		})
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		servePage(w, r, queries, defaultVisibility, func(vis cp.Visibility, common cp.CommonFilters, limit int, cursor cp.Cursor) (any, error) {
			q := cp.EventQuery{
				Common:     common,
				Category:   cp.Category(strings.TrimSpace(r.URL.Query().Get("category"))),
				Limit:      limit,
				Cursor:     cursor,
				Visibility: vis,
			}
			return queries.Events(r.Context(), q)
		})
	})
	return mux
}

// pageCaller is the adapter closure that builds and issues one bounded query.
type pageCaller func(vis cp.Visibility, common cp.CommonFilters, limit int, cursor cp.Cursor) (any, error)

// servePage parses shared query parameters, issues the query, and writes a
// bounded JSON page or a stable error classification (requirement 2.6, 2.7,
// 2.9, 7.4, 9.4).
func servePage(w http.ResponseWriter, r *http.Request, queries cp.Queries, defaultVisibility cp.Visibility, call pageCaller) {
	if queries == nil {
		writeControlPlaneError(w, http.StatusNotFound, cp.ErrCodeDisabled)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeControlPlaneError(w, http.StatusMethodNotAllowed, cp.ErrCodeMethodNotAllowed)
		return
	}
	limit, err := parseLimit(r)
	if err != nil {
		writeControlPlaneError(w, http.StatusBadRequest, cp.ErrCodeInvalidQuery)
		return
	}
	cursor := cp.Cursor{Token: strings.TrimSpace(r.URL.Query().Get("cursor"))}
	visibility := parseVisibility(r, defaultVisibility)
	common := parseCommonFilters(r)
	result, err := call(visibility, common, limit, cursor)
	if err != nil {
		writeQueryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// parseLimit parses the optional limit query parameter. Zero means "use the
// service default". Negative or non-integer values yield invalid_query.
func parseLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, errors.New("limit must be non-negative")
	}
	return n, nil
}

func parseVisibility(r *http.Request, def cp.Visibility) cp.Visibility {
	raw := strings.TrimSpace(r.URL.Query().Get("visibility"))
	if raw == "" {
		return def
	}
	return cp.Visibility(raw)
}

func parseCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseCommonFilters maps supported shared query parameters into the SDK
// CommonFilters struct (requirement 2.5, 9.1).
func parseCommonFilters(r *http.Request) cp.CommonFilters {
	q := r.URL.Query()
	common := cp.CommonFilters{
		BackendID:  strings.TrimSpace(q.Get("backend_id")),
		Model:      strings.TrimSpace(q.Get("model")),
		FrontendID: strings.TrimSpace(q.Get("frontend_id")),
		TraceID:    strings.TrimSpace(q.Get("trace_id")),
		SessionID:  strings.TrimSpace(q.Get("session_id")),
		ALegID:     strings.TrimSpace(q.Get("a_leg_id")),
		BLegID:     strings.TrimSpace(q.Get("b_leg_id")),
		Outcome:    strings.TrimSpace(q.Get("outcome")),
		ReasonCode: strings.TrimSpace(q.Get("reason_code")),
	}
	common.Scope = parseScopeFilters(q)
	common.TimeRange = parseTimeRange(q)
	return common
}

func parseScopeFilters(q url.Values) cp.ScopeFilters {
	return cp.ScopeFilters{
		PrincipalID:    parseScopeValue(q.Get("principal_id")),
		CredentialID:   parseScopeValue(q.Get("credential_id")),
		TenantID:       parseScopeValue(q.Get("tenant_id")),
		OrganizationID: parseScopeValue(q.Get("organization_id")),
		WorkspaceID:    parseScopeValue(q.Get("workspace_id")),
		ProjectID:      parseScopeValue(q.Get("project_id")),
		DepartmentID:   parseScopeValue(q.Get("department_id")),
		CostCenterID:   parseScopeValue(q.Get("cost_center_id")),
	}
}

// parseScopeValue maps a query parameter into a presence-aware scope.Value.
// Empty string stays unknown; the literal "null" expresses known-empty; any
// other value expresses a known non-empty dimension (requirement 4.3).
func parseScopeValue(raw string) scope.Value {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return scope.Unknown()
	}
	if raw == "null" {
		return scope.Known("")
	}
	return scope.Known(raw)
}

func parseTimeRange(q url.Values) cp.TimeRange {
	tr := cp.TimeRange{}
	if from := strings.TrimSpace(q.Get("from")); from != "" {
		if t, err := time.Parse(time.RFC3339, from); err == nil {
			tr.From = t
		}
	}
	if to := strings.TrimSpace(q.Get("to")); to != "" {
		if t, err := time.Parse(time.RFC3339, to); err == nil {
			tr.To = t
		}
	}
	return tr
}

// writeQueryError maps a control-plane query error to a stable HTTP status and
// safe error code without leaking raw infrastructure details (requirement 7.4,
// 9.4, 10.5).
func writeQueryError(w http.ResponseWriter, err error) {
	code := controlplane.Classify(err)
	switch code {
	case cp.ErrCodeDisabled:
		writeControlPlaneError(w, http.StatusNotFound, code)
	case cp.ErrCodeUnavailable:
		writeControlPlaneError(w, http.StatusServiceUnavailable, code)
	case cp.ErrCodeDegraded:
		writeControlPlaneError(w, http.StatusServiceUnavailable, code)
	case cp.ErrCodeInvalidQuery:
		writeControlPlaneError(w, http.StatusBadRequest, code)
	case cp.ErrCodeTooBroad:
		writeControlPlaneError(w, http.StatusBadRequest, code)
	case cp.ErrCodeUnsupportedFilter:
		writeControlPlaneError(w, http.StatusBadRequest, code)
	case cp.ErrCodeUnsafeEvidence:
		writeControlPlaneError(w, http.StatusBadRequest, code)
	default:
		writeControlPlaneError(w, http.StatusServiceUnavailable, cp.ErrCodeControlPlaneUnavailable)
	}
}

func writeControlPlaneError(w http.ResponseWriter, status int, code cp.ErrorCode) {
	writeJSON(w, status, map[string]string{"error": string(code)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
