package controlplane_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// recordingStore is a test-only adapter recording calls to the core-owned Store
// port so recorder behavior can be asserted without SQL/Bun/HTTP types.
type recordingStore struct {
	appends     atomic.Int64
	appendErr   error
	lastEvent   cp.Event
	retention   atomic.Int64
	retentionFn func(controlplane.RetentionCommand) (controlplane.RetentionResult, error)
	ready       error
}

func (s *recordingStore) Append(_ context.Context, ev cp.Event) (cp.RecordResult, error) {
	s.appends.Add(1)
	s.lastEvent = ev
	if s.appendErr != nil {
		return cp.RecordResult{}, s.appendErr
	}
	seq := s.appends.Load()
	return cp.RecordResult{
		ID:         cp.EventID{StoreID: "rec", Sequence: seq},
		Dedupe:     cp.DedupeInserted,
		RecordedAt: ev.OccurredAt,
	}, nil
}

func (s *recordingStore) Sessions(context.Context, cp.SessionQuery) (cp.Page[cp.SessionSummary], error) {
	return cp.Page[cp.SessionSummary]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) Attempts(context.Context, cp.AttemptQuery) (cp.Page[cp.AttemptRow], error) {
	return cp.Page[cp.AttemptRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) Usage(context.Context, cp.UsageQuery) (cp.Page[cp.UsageRow], error) {
	return cp.Page[cp.UsageRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) UsageAggregate(context.Context, cp.UsageAggregateQuery) (cp.Page[cp.UsageAggregate], error) {
	return cp.Page[cp.UsageAggregate]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) PolicyAudit(context.Context, cp.EvidenceQuery) (cp.Page[cp.PolicyAuditRow], error) {
	return cp.Page[cp.PolicyAuditRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) Events(context.Context, cp.EventQuery) (cp.Page[cp.Event], error) {
	return cp.Page[cp.Event]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) ApplyRetention(_ context.Context, cmd controlplane.RetentionCommand) (controlplane.RetentionResult, error) {
	s.retention.Add(1)
	if s.retentionFn != nil {
		return s.retentionFn(cmd)
	}
	return controlplane.RetentionResult{Marked: 1}, nil
}
func (s *recordingStore) CheckReadiness(context.Context) error { return s.ready }

var _ controlplane.Store = (*recordingStore)(nil)

func recorderEvent() cp.Event {
	return cp.Event{
		Category:       cp.CategoryAuth,
		OccurredAt:     time.Now(),
		RecordedAt:     time.Now(),
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Detail:         &cp.AuthDetail{Outcome: "allow"},
		Source:         cp.SourceRef{Name: "test"},
	}
}

func TestRecorderDisabledReturnsErrDisabledAndDoesNotTouchStore(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityDisabled, RecordingPolicy: cp.RecordingDisabled})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingDisabled,
		Clock:  fixedClock{t: time.Now()},
	})
	_, err := rec.Record(context.Background(), recorderEvent())
	if !errors.Is(err, controlplane.ErrDisabled) {
		t.Fatalf("disabled recorder must return ErrDisabled, got %v", err)
	}
	if store.appends.Load() != 0 {
		t.Fatalf("disabled recorder must not call store.Append, got %d", store.appends.Load())
	}
}

func TestRecorderBestEffortRecordsAndReturnsResult(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	res, err := rec.Record(context.Background(), recorderEvent())
	if err != nil {
		t.Fatalf("best-effort record returned error: %v", err)
	}
	if res.Dedupe != cp.DedupeInserted || res.ID.IsZero() {
		t.Fatalf("best-effort record result lost: %#v", res)
	}
	if store.appends.Load() != 1 {
		t.Fatalf("expected one append, got %d", store.appends.Load())
	}
}

func TestRecorderBestEffortFailureDegradesStatusButPreservesRequestOutcome(t *testing.T) {
	t.Parallel()
	store := &recordingStore{appendErr: errors.New("transient store down")}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	_, err := rec.Record(context.Background(), recorderEvent())
	if err != nil {
		t.Fatalf("best-effort failure must not surface error to caller (preserve request outcome), got %v", err)
	}
	got := status.Snapshot()
	if got.State != cp.CapabilityDegraded || got.Reason != cp.ReasonRecordingFailure {
		t.Fatalf("status must become degraded with recording_failure, got %#v", got)
	}
}

func TestRecorderRequiredPreWorkFailureFailsClosedBeforeUpstream(t *testing.T) {
	t.Parallel()
	store := &recordingStore{appendErr: errors.New("store down")}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingRequiredPreWork})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy:   cp.RecordingRequiredPreWork,
		Required: []cp.Category{cp.CategoryAuth},
		Clock:    fixedClock{t: time.Now()},
	})
	_, err := rec.Record(context.Background(), recorderEvent())
	if err == nil {
		t.Fatalf("required pre-work failure must return error to fail closed before upstream work")
	}
	if !errors.Is(err, controlplane.ErrUnavailable) && !errors.Is(err, controlplane.ErrDegraded) {
		t.Fatalf("required pre-work failure must classify as unavailable or degraded, got %v", err)
	}
	got := status.Snapshot()
	if got.State != cp.CapabilityDegraded && got.State != cp.CapabilityUnavailable {
		t.Fatalf("status must reflect failure after required pre-work failure, got %q", got.State)
	}
}

func TestRecorderRequiredPreWorkSucceedsForBestEffortCategory(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingRequiredPreWork})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy:   cp.RecordingRequiredPreWork,
		Required: []cp.Category{cp.CategoryAuth},
		Clock:    fixedClock{t: time.Now()},
	})
	// usage category is NOT in required set, so required_pre_work policy treats it as best-effort.
	ev := recorderEvent()
	ev.Category = cp.CategoryUsage
	ev.Detail = nil
	ev.Detail = &cp.UsageDetail{Plane: cp.UsagePlaneObserved, Availability: cp.UsageAvailabilityObserved}
	_, err := rec.Record(context.Background(), ev)
	if err != nil {
		t.Fatalf("non-required category under required_pre_work must be best-effort, got %v", err)
	}
}

func TestRecorderBestEffortStoreSideUnsafeEvidenceSurfacesWithoutDegrading(t *testing.T) {
	t.Parallel()
	store := &recordingStore{appendErr: fmt.Errorf("%w: store rejected evidence", controlplane.ErrUnsafeEvidence)}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	_, err := rec.RecordBestEffort(context.Background(), recorderEvent())
	if !errors.Is(err, controlplane.ErrUnsafeEvidence) {
		t.Fatalf("RecordBestEffort must surface store-side ErrUnsafeEvidence, got %v", err)
	}
	got := status.Snapshot()
	if got.State != cp.CapabilityReady {
		t.Fatalf("store-side unsafe evidence must not degrade status, got %q", got.State)
	}
}

func TestRecorderBestEffortPathNeverFailsClosedForPostOutput(t *testing.T) {
	t.Parallel()
	store := &recordingStore{appendErr: errors.New("post-output failure")}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingRequiredPreWork})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy:   cp.RecordingRequiredPreWork,
		Required: []cp.Category{cp.CategoryAuth},
		Clock:    fixedClock{t: time.Now()},
	})
	// RecordBestEffort is the post-output / non-protected path: it must never
	// fail closed, even for a category that is otherwise required pre-work.
	_, err := rec.RecordBestEffort(context.Background(), recorderEvent())
	if err != nil {
		t.Fatalf("RecordBestEffort must never fail closed (no retry/failover/replacement), got %v", err)
	}
	if got := status.Snapshot().State; got != cp.CapabilityDegraded {
		t.Fatalf("post-output failure must degrade status, got %q", got)
	}
}

func TestRecorderRejectsUnsafeEvidenceRegardlessOfPolicy(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	ev := recorderEvent()
	ev.Summary = "Bearer secrettoken"
	if _, err := rec.Record(context.Background(), ev); err == nil {
		t.Fatalf("recorder must reject unsafe evidence regardless of policy")
	}
	if store.appends.Load() != 0 {
		t.Fatalf("recorder must not call store.Append for unsafe evidence")
	}
}

// TestRecorderRejectsRecordedAtBeforeOccurredAtAsUnsafeEvidence regression-locks
// the RecordedAt-before-OccurredAt ordering guard at the recorder layer,
// completing the four-layer (SDK, core validator, normalizer, recorder)
// coverage. The recorder has no own guard — it relies on the normalizer +
// validator chain via prepareAppend, which wraps any validation error as
// ErrUnsafeEvidence (programmer error, not transient store failure). This
// test produces a valid event via the normalizer, mutates it so RecordedAt
// is earlier than OccurredAt (both non-zero), and asserts:
//  1. The error wraps ErrUnsafeEvidence (programmer error classification).
//  2. The explicit-guard substring "recorded_at precedes occurred_at"
//     surfaces through the wrap.
//  3. The store is NOT called (0 appends).
//  4. The status is NOT degraded (programmer error must not pollute the
//     capability state).
func TestRecorderRejectsRecordedAtBeforeOccurredAtAsUnsafeEvidence(t *testing.T) {
	t.Parallel()
	n := controlplane.NewNormalizer(
		fixedClock{t: time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)},
		cp.SourceRef{Name: "test-source", Version: "v1"},
		controlplane.NewScopeFlattener(),
	)
	ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
		Time:    time.Date(2026, 7, 4, 0, 1, 0, 0, time.UTC),
		TraceID: "trace-1",
		Outcome: auth.OutcomeAllow,
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	// Mutate so RecordedAt is earlier than OccurredAt (both non-zero):
	// OccurredAt is later; RecordedAt stays as the normalizer's clock value (earlier).
	ev.OccurredAt = ev.RecordedAt.Add(time.Minute)

	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	_, err = rec.Record(context.Background(), ev)
	if !errors.Is(err, controlplane.ErrUnsafeEvidence) {
		t.Fatalf("ordering violation must be classified as ErrUnsafeEvidence (programmer error), got %v", err)
	}
	if !strings.Contains(err.Error(), "recorded_at precedes occurred_at") {
		t.Fatalf("error must surface explicit-guard 'recorded_at precedes occurred_at', got: %v", err)
	}
	if store.appends.Load() != 0 {
		t.Fatalf("recorder must not call store.Append for ordering violation, got %d appends", store.appends.Load())
	}
	if got := status.Snapshot().State; got != cp.CapabilityReady {
		t.Fatalf("ordering violation must not degrade status (programmer error, not transient), got %q", got)
	}
}

// TestRecorderRejectsZeroTimestampsAsUnsafeEvidence regression-locks the
// recorder's handling of zero-timestamp events. The recorder has no own
// RecordedAt guard — it relies on the normalizer + validator chain via
// prepareAppend, which wraps any validation error as ErrUnsafeEvidence
// (programmer error, not transient store failure). This test pins:
//  1. A zero RecordedAt event is rejected with ErrUnsafeEvidence (not
//     silently dropped or degraded to a transient failure).
//  2. The explicit-guard substring "recorded_at is required" surfaces
//     through the wrap, so the test cannot pass for a different reason.
//  3. The store is NOT called (0 appends).
//  4. The status is NOT degraded (programmer error must not pollute the
//     capability state).
//  5. The same holds for zero OccurredAt.
//
// A future refactor that weakened this classification (e.g., treating
// zero-timestamp events as transient store failures and degrading status)
// would fail this test.
func TestRecorderRejectsZeroTimestampsAsUnsafeEvidence(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})

	t.Run("zero_recorded_at", func(t *testing.T) {
		t.Parallel()
		ev := recorderEvent()
		ev.RecordedAt = time.Time{}
		_, err := rec.Record(context.Background(), ev)
		if !errors.Is(err, controlplane.ErrUnsafeEvidence) {
			t.Fatalf("zero RecordedAt must be classified as ErrUnsafeEvidence (programmer error), got %v", err)
		}
		if !strings.Contains(err.Error(), "recorded_at is required") {
			t.Fatalf("error must surface explicit-guard 'recorded_at is required', got: %v", err)
		}
		if store.appends.Load() != 0 {
			t.Fatalf("recorder must not call store.Append for zero RecordedAt, got %d appends", store.appends.Load())
		}
		if got := status.Snapshot().State; got != cp.CapabilityReady {
			t.Fatalf("zero RecordedAt must not degrade status (programmer error, not transient), got %q", got)
		}
	})

	t.Run("zero_occurred_at", func(t *testing.T) {
		t.Parallel()
		ev := recorderEvent()
		ev.OccurredAt = time.Time{}
		_, err := rec.Record(context.Background(), ev)
		if !errors.Is(err, controlplane.ErrUnsafeEvidence) {
			t.Fatalf("zero OccurredAt must be classified as ErrUnsafeEvidence (programmer error), got %v", err)
		}
		if !strings.Contains(err.Error(), "occurred_at is required") {
			t.Fatalf("error must surface explicit-guard 'occurred_at is required', got: %v", err)
		}
		if store.appends.Load() != 0 {
			t.Fatalf("recorder must not call store.Append for zero OccurredAt, got %d appends", store.appends.Load())
		}
		if got := status.Snapshot().State; got != cp.CapabilityReady {
			t.Fatalf("zero OccurredAt must not degrade status (programmer error, not transient), got %q", got)
		}
	})
}

// TestRecorderRejectsUnsafeSummaryAsUnsafeEvidence regression-locks the
// unsafe-summary guard at the recorder layer, completing the four-layer
// (SDK, core validator, normalizer, recorder) coverage. The recorder has
// no own guard — it relies on the normalizer + core validator chain via
// prepareAppend, which wraps any validation error as ErrUnsafeEvidence
// (programmer error, not transient store failure). This test produces a
// valid event via the normalizer, mutates it to add an unsafe
// credential-bearing summary, and asserts:
//  1. The error wraps ErrUnsafeEvidence (programmer error classification).
//  2. The explicit-guard substring "unsafe token-like content" surfaces
//     through the wrap.
//  3. The store is NOT called (0 appends).
//  4. The status is NOT degraded (programmer error must not pollute the
//     capability state).
func TestRecorderRejectsUnsafeSummaryAsUnsafeEvidence(t *testing.T) {
	t.Parallel()
	n := controlplane.NewNormalizer(
		fixedClock{t: time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)},
		cp.SourceRef{Name: "test-source", Version: "v1"},
		controlplane.NewScopeFlattener(),
	)
	ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
		Time:    time.Date(2026, 7, 4, 0, 1, 0, 0, time.UTC),
		TraceID: "trace-1",
		Outcome: auth.OutcomeAllow,
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	// Mutate to add an unsafe summary (credential-like content).
	ev.Summary = "Bearer secrettoken"

	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	_, err = rec.Record(context.Background(), ev)
	if !errors.Is(err, controlplane.ErrUnsafeEvidence) {
		t.Fatalf("unsafe summary must be classified as ErrUnsafeEvidence (programmer error), got %v", err)
	}
	if !strings.Contains(err.Error(), "unsafe token-like content") {
		t.Fatalf("error must surface explicit-guard 'unsafe token-like content', got: %v", err)
	}
	if store.appends.Load() != 0 {
		t.Fatalf("recorder must not call store.Append for unsafe summary, got %d appends", store.appends.Load())
	}
	if got := status.Snapshot().State; got != cp.CapabilityReady {
		t.Fatalf("unsafe summary must not degrade status (programmer error, not transient), got %q", got)
	}
}

// TestRecorderRejectsEmptySourceNameAsUnsafeEvidence regression-locks the
// empty-source-name guard at the recorder layer, completing the four-layer
// (SDK, core validator, normalizer, recorder) coverage. The recorder has
// no own guard — it relies on the normalizer + core validator chain via
// prepareAppend, which wraps any validation error as ErrUnsafeEvidence
// (programmer error, not transient store failure). This test produces a
// valid event via the normalizer, mutates it to clear the source name,
// and asserts:
//  1. The error wraps ErrUnsafeEvidence (programmer error classification).
//  2. The explicit-guard substring "source.name is required" surfaces
//     through the wrap.
//  3. The store is NOT called (0 appends).
//  4. The status is NOT degraded (programmer error must not pollute the
//     capability state).
func TestRecorderRejectsEmptySourceNameAsUnsafeEvidence(t *testing.T) {
	t.Parallel()
	n := controlplane.NewNormalizer(
		fixedClock{t: time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)},
		cp.SourceRef{Name: "test-source", Version: "v1"},
		controlplane.NewScopeFlattener(),
	)
	ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
		Time:    time.Date(2026, 7, 4, 0, 1, 0, 0, time.UTC),
		TraceID: "trace-1",
		Outcome: auth.OutcomeAllow,
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	// Mutate to clear the source name.
	ev.Source = cp.SourceRef{}

	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	_, err = rec.Record(context.Background(), ev)
	if !errors.Is(err, controlplane.ErrUnsafeEvidence) {
		t.Fatalf("empty source name must be classified as ErrUnsafeEvidence (programmer error), got %v", err)
	}
	if !strings.Contains(err.Error(), "source.name is required") {
		t.Fatalf("error must surface explicit-guard 'source.name is required', got: %v", err)
	}
	if store.appends.Load() != 0 {
		t.Fatalf("recorder must not call store.Append for empty source name, got %d appends", store.appends.Load())
	}
	if got := status.Snapshot().State; got != cp.CapabilityReady {
		t.Fatalf("empty source name must not degrade status (programmer error, not transient), got %q", got)
	}
}

// TestRecorderRejectsAsUnsafeEvidence parametrizes the four-assertion
// recorder-layer regression across every covered control-plane guard. Mirrors
// the precedent set by TestRecorderRejectsZeroTimestampsAsUnsafeEvidence in this
// file: shared normalizer + recorder + event-construction boilerplate, per-case
// mutation function and asserted substring. Each sub-exercise verifies:
//  1. ErrUnsafeEvidence wraps the inner validation error (programmer error
//     classification, never a transient failure).
//  2. The explicit-guard substring surfaces through the wrap, so the test
//     cannot pass for a different reason (e.g. due to a guard reordering).
//  3. The store is NOT called (0 appends) — the recorder fails before any
//     side-effect, never silently dropping unsafe evidence into durability.
//  4. The capability status is NOT degraded — programmer errors must not
//     pollute the operator-facing health signal; ErrDisabled / ErrDegraded /
//     ErrUnavailable remain reserved for transient capability failures.
//
// A future refactor that weakens any of the four seals (silently dropping the
// error, demoting to a transient failure, retrying, or replacing) breaks every
// sub-case below. Keeping the cases consolidated in one parametric function
// with a single source-of-truth for the four assertions means the seal cannot
// drift between guards; adding a new control-plane guard means adding a row.
//
// Construction notes:
//   - Each sub-case uses FromAuthDecision to pass through the normalizer chain
//     (per the four-layer template) and mutates exactly one field so only the
//     target guard fires.
//   - MaxSummaryBytes+1 'x' bytes keeps each summary entry too large for the
//     unsafe-content check to pre-empt the size guard.
//   - Scope.Principal.PolicyLabels uses short itoa-pattern keys so per-entry
//     bytes stay under MaxScopeMapValue and only the entry-count bound fires.
//   - The itoa helper lives in validate_test.go (same controlplane_test package).
func TestRecorderRejectsAsUnsafeEvidence(t *testing.T) {
	t.Parallel()
	caseRecorder := func() (*controlplane.RecorderService, *recordingStore, *controlplane.Status) {
		store := &recordingStore{}
		status := controlplane.NewStatus(cp.CapabilityStatus{
			State:           cp.CapabilityReady,
			RecordingPolicy: cp.RecordingBestEffort,
		})
		rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
			Policy: cp.RecordingBestEffort,
			Clock:  fixedClock{t: time.Now()},
		})
		return rec, store, status
	}
	caseEvent := func(t *testing.T) cp.Event {
		t.Helper()
		n := controlplane.NewNormalizer(
			fixedClock{t: time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)},
			cp.SourceRef{Name: "test-source", Version: "v1"},
			controlplane.NewScopeFlattener(),
		)
		ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
			Time:    time.Date(2026, 7, 4, 0, 1, 0, 0, time.UTC),
			TraceID: "trace-1",
			Outcome: auth.OutcomeAllow,
		})
		if err != nil {
			t.Fatalf("FromAuthDecision: %v", err)
		}
		return ev
	}
	cases := []struct {
		name        string
		mutate      func(*cp.Event)
		expectedSub string
	}{
		{
			name:        "privileged_visibility_without_privileged_redaction",
			mutate:      func(e *cp.Event) { e.Visibility = cp.VisibilityPrivileged },
			expectedSub: "privileged visibility requires privileged redaction state",
		},
		{
			name:        "zero_detail_blocks",
			mutate:      func(e *cp.Event) { e.Detail = nil },
			expectedSub: "exactly one detail block is required",
		},
		{
			name:        "category_detail_mismatch_from_detail_swap",
			mutate:      func(e *cp.Event) { e.Detail = &cp.SessionDetail{Action: cp.SessionActionCreated} },
			expectedSub: "requires auth detail",
		},
		{
			name:        "oversized_summary",
			mutate:      func(e *cp.Event) { e.Summary = strings.Repeat("x", controlplane.MaxSummaryBytes+1) },
			expectedSub: "summary exceeds",
		},
		{
			name: "oversized_scope_map",
			mutate: func(e *cp.Event) {
				labels := make(map[string]string, controlplane.MaxScopeMapEntries+1)
				for i := range controlplane.MaxScopeMapEntries + 1 {
					labels["k"+itoa(i)] = "v"
				}
				e.Scope = cp.ScopeSnapshot{
					Principal: scope.PrincipalScopeView{PolicyLabels: labels},
				}
			},
			expectedSub: "scope.policy_labels exceeds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec, store, status := caseRecorder()
			ev := caseEvent(t)
			tc.mutate(&ev)
			_, err := rec.Record(context.Background(), ev)
			if !errors.Is(err, controlplane.ErrUnsafeEvidence) {
				t.Fatalf("%s must be classified as ErrUnsafeEvidence (programmer error), got %v", tc.name, err)
			}
			if !strings.Contains(err.Error(), tc.expectedSub) {
				t.Fatalf("%s must surface explicit-guard %q, got: %v", tc.name, tc.expectedSub, err)
			}
			if store.appends.Load() != 0 {
				t.Fatalf("%s must not call store.Append (programmer error, no side-effects), got %d appends", tc.name, store.appends.Load())
			}
			if got := status.Snapshot().State; got != cp.CapabilityReady {
				t.Fatalf("%s must not degrade status (programmer error, not transient), got %q", tc.name, got)
			}
		})
	}
}

func TestRecorderStatusReflectsCurrentState(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	got, err := rec.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.State != cp.CapabilityReady {
		t.Fatalf("Status must report ready, got %q", got.State)
	}
}
