package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// QueryServiceConfig configures a [QueryService] (requirement 2.6, 7.4).
type QueryServiceConfig struct {
	// Enabled reports whether the protected query surface is exposed. When
	// false, every read view returns [ErrDisabled] and Status reports the
	// capability as disabled, rather than returning a misleading empty page
	// (requirement 2.9, 7.5).
	Enabled bool
	// DefaultPageSize is applied when a query omits Limit. Defaults to 100.
	DefaultPageSize int
	// MaxPageSize is the upper bound for any query page. Limits above this
	// are rejected as too broad before store access (requirement 2.6, 7.4).
	MaxPageSize int
	// MaxTimeWindow bounds the time range of broad event queries. Zero means
	// no bound. Ranges wider than this are rejected as too broad before store
	// access (requirement 2.6, 7.4).
	MaxTimeWindow time.Duration
}

// QueryService serves bounded cross-session read views with stable
// continuation and unsupported-filter reporting (requirements 2.1–2.9, 3.4,
// 3.5, 3.6, 6.3, 6.4, 7.1, 7.4, 9.1, 9.4, 9.5).
//
// Bounds validation (limit, time window, cursor syntax, visibility) happens
// before any store access. Unsupported-filter reporting and shape-bound cursor
// validation are delegated to the store, which owns the cursor envelope and
// the recorded-evidence filter capabilities (design "Query Flow").
type QueryService struct {
	store     QuerySource
	status    *Status
	enabled   bool
	defPage   int
	maxPage   int
	maxWindow time.Duration
}

// NewQueryService constructs a QueryService bound to the supplied store and
// status. status may be nil; in that case Status reports the configured
// enabled/disabled state without consulting shared status state.
func NewQueryService(store QuerySource, status *Status, cfg QueryServiceConfig) *QueryService {
	def := cfg.DefaultPageSize
	if def <= 0 {
		def = 100
	}
	maxPageSize := cfg.MaxPageSize
	if maxPageSize <= 0 {
		maxPageSize = 500
	}
	if def > maxPageSize {
		def = maxPageSize
	}
	return &QueryService{
		store:     store,
		status:    status,
		enabled:   cfg.Enabled,
		defPage:   def,
		maxPage:   maxPageSize,
		maxWindow: cfg.MaxTimeWindow,
	}
}

// Status reports the current capability state (requirement 7.1, 2.9).
func (s *QueryService) Status(context.Context) (cp.CapabilityStatus, error) {
	if !s.enabled {
		return cp.CapabilityStatus{State: cp.CapabilityDisabled, RecordingPolicy: cp.RecordingDisabled}, nil
	}
	if s.status != nil {
		return s.status.Snapshot(), nil
	}
	return cp.CapabilityStatus{State: cp.CapabilityReady}, nil
}

// Sessions serves bounded session summaries (requirement 2.1).
func (s *QueryService) Sessions(ctx context.Context, q cp.SessionQuery) (cp.Page[cp.SessionSummary], error) {
	limit, vis, err := s.prepareQuery(q.Cursor, q.Limit, q.Common.TimeRange, q.Visibility)
	if err != nil {
		return cp.Page[cp.SessionSummary]{}, err
	}
	q.Limit = limit
	q.Visibility = vis
	return s.store.Sessions(ctx, q)
}

// Attempts serves bounded backend attempt rows (requirement 2.2, 3.2).
func (s *QueryService) Attempts(ctx context.Context, q cp.AttemptQuery) (cp.Page[cp.AttemptRow], error) {
	limit, vis, err := s.prepareQuery(q.Cursor, q.Limit, q.Common.TimeRange, q.Visibility)
	if err != nil {
		return cp.Page[cp.AttemptRow]{}, err
	}
	q.Limit = limit
	q.Visibility = vis
	return s.store.Attempts(ctx, q)
}

// Usage serves bounded usage rows (requirement 2.3, 9.2).
func (s *QueryService) Usage(ctx context.Context, q cp.UsageQuery) (cp.Page[cp.UsageRow], error) {
	if err := cp.ValidateUsageQuery(q); err != nil {
		if errors.Is(err, cp.ErrQueryTooBroad) {
			return cp.Page[cp.UsageRow]{}, fmt.Errorf("%w: %v", ErrTooBroad, err)
		}
		if errors.Is(err, cp.ErrQueryUnsupported) {
			return cp.Page[cp.UsageRow]{}, NewUnsupportedFilterError([]string{"class"})
		}
		return cp.Page[cp.UsageRow]{}, err
	}
	limit, vis, err := s.prepareQuery(q.Cursor, q.Limit, q.Common.TimeRange, q.Visibility)
	if err != nil {
		return cp.Page[cp.UsageRow]{}, err
	}
	q.Limit = limit
	q.Visibility = vis
	return s.store.Usage(ctx, q)
}

// UsageAggregate serves bounded usage aggregates (requirement 2.3, 6.4).
// Aggregate rows are logically distinct from detailed usage rows; the store
// projects them separately so aggregates are never presented as detailed raw
// records (requirement 6.4).
func (s *QueryService) UsageAggregate(ctx context.Context, q cp.UsageAggregateQuery) (cp.Page[cp.UsageAggregate], error) {
	limit, vis, err := s.prepareQuery(q.Cursor, q.Limit, q.Common.TimeRange, q.Visibility)
	if err != nil {
		return cp.Page[cp.UsageAggregate]{}, err
	}
	q.Limit = limit
	q.Visibility = vis
	return s.store.UsageAggregate(ctx, q)
}

// PolicyAudit serves bounded policy and audit evidence rows (requirement 2.4,
// 9.3).
func (s *QueryService) PolicyAudit(ctx context.Context, q cp.EvidenceQuery) (cp.Page[cp.PolicyAuditRow], error) {
	limit, vis, err := s.prepareQuery(q.Cursor, q.Limit, q.Common.TimeRange, q.Visibility)
	if err != nil {
		return cp.Page[cp.PolicyAuditRow]{}, err
	}
	q.Limit = limit
	q.Visibility = vis
	return s.store.PolicyAudit(ctx, q)
}

// Events serves bounded raw control-plane events (requirement 9.1).
func (s *QueryService) Events(ctx context.Context, q cp.EventQuery) (cp.Page[cp.Event], error) {
	limit, vis, err := s.prepareQuery(q.Cursor, q.Limit, q.Common.TimeRange, q.Visibility)
	if err != nil {
		return cp.Page[cp.Event]{}, err
	}
	q.Limit = limit
	q.Visibility = vis
	return s.store.Events(ctx, q)
}

// prepareQuery performs the shared pre-store validation and resolution for the
// six bounded read views: capability gate, page-size bound, time-window bound,
// cursor syntax, visibility validation, and default page size / visibility
// resolution (requirement 2.6, 2.7, 2.9, 7.4). It returns the resolved limit
// and visibility the store should receive. The six public methods remain
// distinct so the store call and per-shape query mutation stay readable.
func (s *QueryService) prepareQuery(cursor cp.Cursor, limit int, tr cp.TimeRange, visibility cp.Visibility) (int, cp.Visibility, error) {
	if err := s.preCheck(cursor, limit, tr, visibility); err != nil {
		return 0, "", err
	}
	return s.resolveLimit(limit), resolveQueryVisibility(visibility), nil
}

// preCheck validates the capability, limit, time window, cursor syntax, and
// visibility before any store access (requirement 2.6, 2.7, 2.9, 7.4).
func (s *QueryService) preCheck(cursor cp.Cursor, limit int, tr cp.TimeRange, visibility cp.Visibility) error {
	if !s.enabled {
		return ErrDisabled
	}
	if limit > s.maxPage {
		return fmt.Errorf("%w: limit %d exceeds max page size %d", ErrTooBroad, limit, s.maxPage)
	}
	if err := validateTimeWindow(tr, s.maxWindow); err != nil {
		return err
	}
	if err := validateCursorSyntax(cursor); err != nil {
		return err
	}
	if visibility != "" && !visibility.IsKnown() {
		return fmt.Errorf("%w: unknown visibility %q", ErrInvalidQuery, visibility)
	}
	return nil
}

// resolveLimit applies the default page size when a query omits Limit. Limits
// above the max are rejected by preCheck before this runs.
func (s *QueryService) resolveLimit(limit int) int {
	if limit <= 0 {
		return s.defPage
	}
	return limit
}

// validateTimeWindow rejects time ranges wider than the configured maximum
// (requirement 2.6, 7.4). A zero MaxTimeWindow means no bound.
func validateTimeWindow(tr cp.TimeRange, maxWindow time.Duration) error {
	if tr.From.IsZero() || tr.To.IsZero() {
		return nil
	}
	span := tr.To.Sub(tr.From)
	if span < 0 {
		return fmt.Errorf("%w: time range is inverted", ErrInvalidQuery)
	}
	if maxWindow > 0 && span > maxWindow {
		return fmt.Errorf("%w: time window %v exceeds max %v", ErrTooBroad, span, maxWindow)
	}
	return nil
}

// validateCursorSyntax performs a syntactic check on the opaque cursor token
// before store access (requirement 2.7, 7.4). The store owns the cursor
// envelope and shape-hash binding; the core query service only rejects tokens
// that cannot be valid cursor envelopes (malformed base64url or non-JSON
// bodies) so a tampered or truncated token never reaches the store.
func validateCursorSyntax(c cp.Cursor) error {
	if c.IsZero() {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Token)
	if err != nil {
		return fmt.Errorf("%w: malformed cursor token: %v", ErrInvalidQuery, err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("%w: malformed cursor body: %v", ErrInvalidQuery, err)
	}
	return nil
}

// resolveQueryVisibility defaults an empty visibility to default. Privileged
// visibility is preserved so authorized consumers can request privileged views
// (requirement 4.6, 6.5).
func resolveQueryVisibility(v cp.Visibility) cp.Visibility {
	if v == "" {
		return cp.VisibilityDefault
	}
	return v
}

// Compile-time assertion that QueryService satisfies the SDK Queries contract.
var _ cp.Queries = (*QueryService)(nil)
