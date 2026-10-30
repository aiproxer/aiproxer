package controlplane

import (
	"errors"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// AuthorityOptions configures the protected accounting-authority HTTP handler.
type AuthorityOptions struct {
	Queries         AccountingAuthorityQueries
	DefaultPageSize int
	MaxPageSize     int
}

// QueryState classifies the bounded live authority query posture on the wire.
// It aliases the canonical open-source vocabulary so the HTTP layer and the
// host share one set of state values.
type QueryState = usagelimit.QueryState

const (
	QueryStateDisabled     = usagelimit.QueryStateDisabled
	QueryStateEmpty        = usagelimit.QueryStateEmpty
	QueryStateUnsupported  = usagelimit.QueryStateUnsupported
	QueryStateTooBroad     = usagelimit.QueryStateTooBroad
	QueryStateAdvisoryOnly = usagelimit.QueryStateAdvisoryOnly
	QueryStateDegraded     = usagelimit.QueryStateDegraded
	QueryStateUnavailable  = usagelimit.QueryStateUnavailable
	QueryStateReady        = usagelimit.QueryStateReady
)

// LimitStatusResult and DecisionHistoryResult are the bounded wire DTOs of the
// live authority query surface.
type LimitStatusResult = usagelimit.LimitStatusResult

type DecisionHistoryResult = usagelimit.DecisionHistoryResult

// AccountingAuthorityQueries is the narrow status/query surface for authority HTTP routes.
type AccountingAuthorityQueries interface {
	cp.AccountingStatusReader
	usagelimit.QueryReader
}

// NewAccountingAuthorityHandler returns a protected HTTP handler for live
// authority status and bounded query views.
func NewAccountingAuthorityHandler(opts AuthorityOptions) http.Handler {
	queries := opts.Queries
	defaultPageSize := opts.DefaultPageSize
	if defaultPageSize <= 0 {
		defaultPageSize = 100
	}
	maxPageSize := opts.MaxPageSize
	if maxPageSize <= 0 {
		maxPageSize = 100
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if queries == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if queries == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		status, err := queries.Status(r.Context())
		if err != nil {
			writeAuthorityQueryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
	mux.HandleFunc("/limits", func(w http.ResponseWriter, r *http.Request) {
		serveAuthorityPage(w, r, queries, defaultPageSize, maxPageSize, func(limit int) (QueryState, cp.Page[cp.AccountingLimitStatusRow], error) {
			q := cp.AccountingLimitStatusQuery{Limit: limit}
			fillAuthorityListQuery(r, &q.Common, &q.RuleID, &q.Unit, &q.Currency, &q.Authority, &q.Perspective, &q.LifecycleScope, &q.Basis, &q.Class, &q.Cursor, &q.Visibility, &q.SettlementState, &q.EvidenceState, &q.RedactionState)
			res, err := queries.Limits(r.Context(), q)
			if err != nil {
				return "", cp.Page[cp.AccountingLimitStatusRow]{}, err
			}
			return res.State, res.Page, nil
		})
	})
	mux.HandleFunc("/decision-history", func(w http.ResponseWriter, r *http.Request) {
		serveAuthorityPage(w, r, queries, defaultPageSize, maxPageSize, func(limit int) (QueryState, cp.Page[cp.AccountingDecisionRow], error) {
			q := cp.AccountingDecisionQuery{Limit: limit}
			fillAuthorityListQuery(r, &q.Common, &q.RuleID, &q.Unit, &q.Currency, &q.Authority, &q.Perspective, &q.LifecycleScope, &q.Basis, &q.Class, &q.Cursor, &q.Visibility, &q.SettlementState, &q.EvidenceState, &q.RedactionState)
			res, err := queries.Decisions(r.Context(), q)
			if err != nil {
				return "", cp.Page[cp.AccountingDecisionRow]{}, err
			}
			return res.State, res.Page, nil
		})
	})
	return mux
}

func fillAuthorityListQuery(
	r *http.Request,
	common *cp.CommonFilters,
	ruleID, unit, currency *string,
	authority *cp.AccountingAuthoritySource,
	perspective *cp.UsagePerspective,
	lifecycle *cp.UsageLifecycleScope,
	basis *string,
	class *cp.QueryClass,
	cursor *cp.Cursor,
	visibility *cp.Visibility,
	settlement *cp.AccountingSettlementState,
	evidence *cp.EvidenceState,
	redaction *cp.RedactionState,
) {
	*common = parseCommonFilters(r)
	*ruleID = strings.TrimSpace(r.URL.Query().Get("rule_id"))
	*unit = strings.TrimSpace(r.URL.Query().Get("unit"))
	*currency = strings.TrimSpace(r.URL.Query().Get("currency"))
	*authority = cp.AccountingAuthoritySource(strings.TrimSpace(r.URL.Query().Get("authority")))
	*perspective = cp.UsagePerspective(strings.TrimSpace(r.URL.Query().Get("perspective")))
	*lifecycle = cp.UsageLifecycleScope(strings.TrimSpace(r.URL.Query().Get("lifecycle_scope")))
	*basis = strings.TrimSpace(r.URL.Query().Get("basis"))
	*class = cp.QueryClass(strings.TrimSpace(r.URL.Query().Get("class")))
	*cursor = cp.Cursor{Token: strings.TrimSpace(r.URL.Query().Get("cursor"))}
	*visibility = cp.Visibility(strings.TrimSpace(r.URL.Query().Get("visibility")))
	if raw := strings.TrimSpace(r.URL.Query().Get("settlement_state")); raw != "" {
		*settlement = cp.AccountingSettlementState(raw)
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("evidence_state")); raw != "" {
		*evidence = cp.EvidenceState(raw)
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("redaction_state")); raw != "" {
		*redaction = cp.RedactionState(raw)
	}
}

type authorityPageResponse[T any] struct {
	State QueryState `json:"state"`
	Page  cp.Page[T] `json:"page"`
}

func serveAuthorityPage[T any](w http.ResponseWriter, r *http.Request, queries AccountingAuthorityQueries, defaultLimit, maxLimit int, call func(limit int) (QueryState, cp.Page[T], error)) {
	if queries == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	limit, err := parseLimit(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
		return
	}
	if limit == 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too_broad"})
		return
	}
	state, page, err := call(limit)
	if err != nil {
		writeAuthorityQueryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, authorityPageResponse[T]{State: state, Page: page})
}

func writeAuthorityQueryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, usagelimit.ErrDisabled):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
	case errors.Is(err, usagelimit.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
	case errors.Is(err, usagelimit.ErrDegraded):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "degraded"})
	case errors.Is(err, usagelimit.ErrInvalidQuery):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
	case errors.Is(err, usagelimit.ErrUnsupportedFilter):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_filter"})
	case errors.Is(err, usagelimit.ErrEvaluationTimeout):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
	}
}
