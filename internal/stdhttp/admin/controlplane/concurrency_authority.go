package controlplane

import (
	"context"
	"net/http"
	"strings"
	"time"

	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	concurrencydomain "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// ConcurrencyOptions configures the protected concurrency-lease query handler.
type ConcurrencyOptions struct {
	Provider        authority.ConcurrencyProvider
	Service         ConcurrencyAuthorityQueries // optional; enables capacity/readiness summaries
	DefaultPageSize int
	MaxPageSize     int
}

// ConcurrencyAuthorityQueries is the narrow readiness/capacity surface for lease HTTP routes.
type ConcurrencyAuthorityQueries interface {
	ReadinessDomain(ctx context.Context) (concurrencydomain.Readiness, error)
	RulesSnapshot(ctx context.Context) (concurrencyapp.RuleSnapshot, error)
	Query(ctx context.Context, q concurrencyapp.QueryCommand) (concurrencyapp.QueryResult, error)
}

// NewConcurrencyAuthorityHandler returns protected HTTP routes for lease queries.
// Mount under the same diagnostics-protected /authority prefix as accounting queries.
func NewConcurrencyAuthorityHandler(opts ConcurrencyOptions) http.Handler {
	provider := opts.Provider
	svc := opts.Service
	defaultPageSize := opts.DefaultPageSize
	if defaultPageSize <= 0 {
		defaultPageSize = 100
	}
	maxPageSize := opts.MaxPageSize
	if maxPageSize <= 0 {
		maxPageSize = 100
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/leases/status", func(w http.ResponseWriter, r *http.Request) {
		if provider == nil && svc == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		status := cp.ConcurrencyAuthorityStatus{
			State:          cp.ConcurrencyAuthorityReady,
			EvidenceState:  cp.EvidenceRecorded,
			RedactionState: cp.RedactionRedacted,
			LastUpdatedAt:  time.Now().UTC(),
		}
		if svc != nil {
			ready, err := svc.ReadinessDomain(r.Context())
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
				return
			}
			status.State = mapConcurrencyReadiness(ready.State)
			status.Reason = ready.Reason
		}
		writeJSON(w, http.StatusOK, status)
	})
	mux.HandleFunc("/leases", func(w http.ResponseWriter, r *http.Request) {
		if provider == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("from")); raw != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_filter", "filter": "from"})
			return
		}
		for _, filter := range []string{
			"to", "principal", "tenant", "workspace", "project", "department",
			"cost_center", "backend", "model", "route", "perspective", "boundary", "lifecycle",
		} {
			if strings.TrimSpace(r.URL.Query().Get(filter)) != "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_filter", "filter": filter})
				return
			}
		}
		limit, err := parseLimit(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
			return
		}
		if limit == 0 {
			limit = defaultPageSize
		}
		if limit > maxPageSize {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too_broad"})
			return
		}
		q := authority.LeaseQuery{
			RequestID: strings.TrimSpace(r.URL.Query().Get("request_id")),
			LeaseID:   strings.TrimSpace(r.URL.Query().Get("lease_id")),
			RuleID:    strings.TrimSpace(r.URL.Query().Get("rule_id")),
			State:     authority.LeaseState(strings.TrimSpace(r.URL.Query().Get("state"))),
			Limit:     limit,
			Cursor:    strings.TrimSpace(r.URL.Query().Get("cursor")),
		}
		page, qerr := provider.QueryLeases(r.Context(), q)
		if qerr != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
			return
		}
		out := cp.Page[cp.ConcurrencyLeaseRow]{
			Items: make([]cp.ConcurrencyLeaseRow, 0, len(page.Leases)),
		}
		if page.NextCursor != "" {
			out.Next = cp.Cursor{Token: page.NextCursor}
		}
		for _, lease := range page.Leases {
			out.Items = append(out.Items, cp.ConcurrencyLeaseRow{
				LeaseID:        lease.LeaseID,
				RequestID:      lease.RequestID,
				RuleID:         lease.RuleID,
				RuleVersion:    lease.Version.Version,
				DimensionKey:   lease.DimensionKey,
				State:          cp.ConcurrencyLeaseState(lease.State),
				Generation:     lease.Generation,
				ExpiresAt:      lease.ExpiresAt,
				ReleasedAt:     lease.ReleasedAt,
				EvidenceState:  cp.EvidenceRecorded,
				RedactionState: cp.RedactionRedacted,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"state": "ready", "page": out})
	})
	mux.HandleFunc("/leases/capacity", func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "disabled"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		rows, err := capacityRows(r.Context(), svc)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"state": "ready", "rows": rows})
	})
	return mux
}

func mapConcurrencyReadiness(state concurrencydomain.ReadinessState) cp.ConcurrencyAuthorityState {
	switch state {
	case concurrencydomain.ReadinessStateReady:
		return cp.ConcurrencyAuthorityReady
	case concurrencydomain.ReadinessStateDegraded:
		return cp.ConcurrencyAuthorityDegraded
	case concurrencydomain.ReadinessStateUnavailable:
		return cp.ConcurrencyAuthorityUnavailable
	case concurrencydomain.ReadinessStateDisabled:
		return cp.ConcurrencyAuthorityDisabled
	default:
		return cp.ConcurrencyAuthorityReady
	}
}

func capacityRows(ctx context.Context, svc ConcurrencyAuthorityQueries) ([]cp.ConcurrencyCapacityRow, error) {
	snap, err := svc.RulesSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]cp.ConcurrencyCapacityRow, 0, len(snap.Rules))
	for _, rule := range snap.Rules {
		// Bound by page max; filter by rule_id so multi-rule stores are not undercounted.
		res, qerr := svc.Query(ctx, concurrencyapp.QueryCommand{
			RuleID: rule.ID,
			Limit:  500,
			Now:    now,
		})
		if qerr != nil {
			return nil, qerr
		}
		active := 0
		expiring := 0
		dimKey := ""
		for _, lease := range res.Leases {
			if lease.RuleID != rule.ID {
				continue
			}
			state := lease.EffectiveState(now)
			switch state {
			case concurrencydomain.LeaseStateActive:
				if !lease.ExpiresAt.IsZero() && !now.Before(lease.ExpiresAt.Add(-rule.EffectiveRenewBefore())) {
					expiring++
					active++
				} else {
					active++
				}
			case concurrencydomain.LeaseStateExpiring:
				expiring++
				active++
			}
			if dimKey == "" {
				dimKey = string(lease.Dimensions.Key())
			}
		}
		remaining := max(rule.Limit-active, 0)
		out = append(out, cp.ConcurrencyCapacityRow{
			RuleID:         rule.ID,
			RuleVersion:    rule.Version,
			DimensionKey:   dimKey,
			Limit:          rule.Limit,
			Active:         active,
			Expiring:       expiring,
			RemainingSlots: remaining,
		})
	}
	return out, nil
}
