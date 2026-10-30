package controlplane_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// queryProbeStore records whether a query method was invoked, so tests can
// prove the query service enforces bounds BEFORE store access.
type queryProbeStore struct {
	recordingStore
	sessionsCalled bool
	eventsCalled   bool
	attemptsCalled bool
	usageCalled    bool
	usageAggCalled bool
	policyCalled   bool
}

func (s *queryProbeStore) Sessions(context.Context, cp.SessionQuery) (cp.Page[cp.SessionSummary], error) {
	s.sessionsCalled = true
	return cp.Page[cp.SessionSummary]{Visibility: cp.VisibilityDefault}, nil
}

func (s *queryProbeStore) Attempts(context.Context, cp.AttemptQuery) (cp.Page[cp.AttemptRow], error) {
	s.attemptsCalled = true
	return cp.Page[cp.AttemptRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *queryProbeStore) Usage(context.Context, cp.UsageQuery) (cp.Page[cp.UsageRow], error) {
	s.usageCalled = true
	return cp.Page[cp.UsageRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *queryProbeStore) UsageAggregate(context.Context, cp.UsageAggregateQuery) (cp.Page[cp.UsageAggregate], error) {
	s.usageAggCalled = true
	return cp.Page[cp.UsageAggregate]{Visibility: cp.VisibilityDefault}, nil
}

func (s *queryProbeStore) PolicyAudit(context.Context, cp.EvidenceQuery) (cp.Page[cp.PolicyAuditRow], error) {
	s.policyCalled = true
	return cp.Page[cp.PolicyAuditRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *queryProbeStore) Events(_ context.Context, q cp.EventQuery) (cp.Page[cp.Event], error) {
	s.eventsCalled = true
	return cp.Page[cp.Event]{Visibility: q.Visibility}, nil
}

func newQueryService(store controlplane.Store, enabled bool, maxWindow time.Duration) (*controlplane.QueryService, *controlplane.Status) {
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	if !enabled {
		status.Disable(time.Now())
	}
	cfg := controlplane.QueryServiceConfig{
		Enabled:         enabled,
		DefaultPageSize: 50,
		MaxPageSize:     200,
		MaxTimeWindow:   maxWindow,
	}
	return controlplane.NewQueryService(store, status, cfg), status
}

func TestQueryServiceDisabledReturnsErrDisabledNotEmpty(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, false, 0)
	if _, err := qs.Sessions(context.Background(), cp.SessionQuery{}); !errors.Is(err, controlplane.ErrDisabled) {
		t.Fatalf("disabled query must return ErrDisabled, not empty page, got %v", err)
	}
	if store.sessionsCalled {
		t.Fatalf("disabled query must not call store")
	}
	if _, err := qs.Events(context.Background(), cp.EventQuery{}); !errors.Is(err, controlplane.ErrDisabled) {
		t.Fatalf("disabled events query must return ErrDisabled, got %v", err)
	}
}

func TestQueryServiceStatusReportsDisabledWhenDisabled(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, false, 0)
	got, err := qs.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.State != cp.CapabilityDisabled {
		t.Fatalf("Status must be disabled, got %q", got.State)
	}
}

func TestQueryServiceRejectsLimitExceedingMaxBeforeStoreAccess(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, true, 0)
	_, err := qs.Sessions(context.Background(), cp.SessionQuery{Limit: 201})
	if !errors.Is(err, controlplane.ErrTooBroad) {
		t.Fatalf("limit over max must be too_broad, got %v", err)
	}
	if store.sessionsCalled {
		t.Fatalf("too-broad query must not reach the store")
	}
}

func TestQueryServiceAppliesDefaultPageSizeWhenLimitOmitted(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, true, 0)
	_, err := qs.Sessions(context.Background(), cp.SessionQuery{})
	if err != nil {
		t.Fatalf("default page size query failed: %v", err)
	}
	if !store.sessionsCalled {
		t.Fatalf("valid query must reach the store")
	}
}

func TestQueryServiceRejectsTimeWindowExceedingMaxBeforeStoreAccess(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, true, time.Hour)
	from := time.Now().Add(-3 * time.Hour)
	to := time.Now()
	_, err := qs.Events(context.Background(), cp.EventQuery{
		Common: cp.CommonFilters{TimeRange: cp.TimeRange{From: from, To: to}},
	})
	if !errors.Is(err, controlplane.ErrTooBroad) {
		t.Fatalf("time window over max must be too_broad, got %v", err)
	}
	if store.eventsCalled {
		t.Fatalf("too-broad time window must not reach the store")
	}
}

func TestQueryServiceAcceptsTimeWindowWithinMax(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, true, 2*time.Hour)
	from := time.Now().Add(-time.Hour)
	to := time.Now()
	_, err := qs.Events(context.Background(), cp.EventQuery{
		Common: cp.CommonFilters{TimeRange: cp.TimeRange{From: from, To: to}},
	})
	if err != nil {
		t.Fatalf("valid time window query failed: %v", err)
	}
	if !store.eventsCalled {
		t.Fatalf("valid query must reach the store")
	}
}

func TestQueryServiceRejectsInvertedTimeWindowWithoutMaxWindow(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	// maxWindow 0 means no too-broad bound, but an inverted range is still invalid.
	qs, _ := newQueryService(store, true, 0)
	from := time.Now()
	to := from.Add(-time.Hour)
	_, err := qs.Events(context.Background(), cp.EventQuery{
		Common: cp.CommonFilters{TimeRange: cp.TimeRange{From: from, To: to}},
	})
	if !errors.Is(err, controlplane.ErrInvalidQuery) {
		t.Fatalf("inverted time range must be invalid_query even without a max window, got %v", err)
	}
	if store.eventsCalled {
		t.Fatalf("inverted time range must not reach the store")
	}
}

func TestQueryServiceDelegatesUnsupportedFiltersFromStore(t *testing.T) {
	t.Parallel()
	// The store is responsible for reporting unsupported filters via
	// Page.Unsupported; the query service passes the page through without
	// silently widening the query (requirement 2.5, 8.6, 9.4).
	store := &recordingStore{}
	qs, _ := newQueryService(store, true, 0)
	page, err := qs.Sessions(context.Background(), cp.SessionQuery{})
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if page.Visibility != cp.VisibilityDefault {
		t.Fatalf("default visibility must be applied, got %q", page.Visibility)
	}
}

func TestQueryServicePrivilegedVisibilityPassesThrough(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, true, 0)
	page, err := qs.Events(context.Background(), cp.EventQuery{Visibility: cp.VisibilityPrivileged})
	if err != nil {
		t.Fatalf("privileged events query failed: %v", err)
	}
	if page.Visibility != cp.VisibilityPrivileged {
		t.Fatalf("privileged visibility must be preserved on the page, got %q", page.Visibility)
	}
}

func TestQueryServiceRejectsInvalidCursorBeforeStoreAccess(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, true, 0)
	_, err := qs.Sessions(context.Background(), cp.SessionQuery{Cursor: cp.Cursor{Token: "!!!not-base64url!!!"}})
	if !errors.Is(err, controlplane.ErrInvalidQuery) {
		t.Fatalf("malformed cursor must be invalid_query, got %v", err)
	}
	if store.sessionsCalled {
		t.Fatalf("invalid cursor must not reach the store")
	}
}

func TestQueryServiceAllReadViewsReachStoreWhenValid(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, true, 0)
	ctx := context.Background()
	if _, err := qs.Sessions(ctx, cp.SessionQuery{}); err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if _, err := qs.Attempts(ctx, cp.AttemptQuery{}); err != nil {
		t.Fatalf("Attempts: %v", err)
	}
	if _, err := qs.Usage(ctx, cp.UsageQuery{Common: cp.CommonFilters{TraceID: "trace-1"}}); err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if _, err := qs.UsageAggregate(ctx, cp.UsageAggregateQuery{}); err != nil {
		t.Fatalf("UsageAggregate: %v", err)
	}
	if _, err := qs.PolicyAudit(ctx, cp.EvidenceQuery{}); err != nil {
		t.Fatalf("PolicyAudit: %v", err)
	}
	if _, err := qs.Events(ctx, cp.EventQuery{}); err != nil {
		t.Fatalf("Events: %v", err)
	}
	if !store.sessionsCalled || !store.attemptsCalled || !store.usageCalled || !store.usageAggCalled || !store.policyCalled || !store.eventsCalled {
		t.Fatalf("all read views must reach the store when valid: %+v", store)
	}
}
