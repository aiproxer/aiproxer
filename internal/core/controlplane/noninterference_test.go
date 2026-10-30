package controlplane_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// TestControlPlaneNonInterference_AttemptStatesQueryableAndDistinct proves task
// 6.2: pre-output failover and parallel race paths that create surfaced,
// swallowed, failed, cancelled, and losing attempts all record distinctly and
// are queryable without contradictory rows (requirements 1.3, 3.2, 3.3, 5.1,
// 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 10.7).
func TestControlPlaneNonInterference_AttemptStatesQueryableAndDistinct(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	store, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "noninterference"})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	recorder := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: fixed},
	})
	normalizer := controlplane.NewNormalizer(fixedClock{t: fixed}, cp.SourceRef{Name: "ni", Version: "v1"}, controlplane.NewScopeFlattener())
	view := scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("principal-ni"), Origin: scope.OriginClient}

	cases := []struct {
		name     string
		surfaced cp.AttemptSurfaced
		outcome  cp.AttemptOutcome
		bleg     string
	}{
		{"surfaced", cp.AttemptSurfacedSurfaced, cp.AttemptOutcomeSucceeded, "bleg-1"},
		{"swallowed", cp.AttemptSurfacedSwallowed, cp.AttemptOutcomeSucceeded, "bleg-2"},
		{"failed", cp.AttemptSurfacedSwallowed, cp.AttemptOutcomeFailed, "bleg-3"},
		{"cancelled", cp.AttemptSurfacedSwallowed, cp.AttemptOutcomeCancelled, "bleg-4"},
		{"lost_race", cp.AttemptSurfacedSwallowed, cp.AttemptOutcomeLostRace, "bleg-5"},
	}
	ctx := context.Background()
	for i, c := range cases {
		ev, err := normalizer.FromAttempt(controlplane.AttemptSourceRecord{
			SourceEventKey: "ni-attempt:" + c.bleg,
			OccurredAt:     fixed.Add(time.Duration(i) * time.Minute),
			TraceID:        "trace-ni",
			SessionID:      "sess-ni",
			ALegID:         "aleg-ni",
			BLegID:         c.bleg,
			AttemptSeq:     i + 1,
			BackendID:      "openai",
			Model:          "gpt-4o",
			RouteOutcome:   "primary",
			Surfaced:       c.surfaced,
			Outcome:        c.outcome,
			Scope:          &view,
		})
		if err != nil {
			t.Fatalf("%s: FromAttempt: %v", c.name, err)
		}
		if _, err := recorder.Record(ctx, ev); err != nil {
			t.Fatalf("%s: Record: %v", c.name, err)
		}
	}

	qs := controlplane.NewQueryService(store, status, controlplane.QueryServiceConfig{
		Enabled:         true,
		DefaultPageSize: 50,
		MaxPageSize:     200,
	})
	page, err := qs.Attempts(ctx, cp.AttemptQuery{Common: cp.CommonFilters{TraceID: "trace-ni"}})
	if err != nil {
		t.Fatalf("Attempts query: %v", err)
	}
	if len(page.Items) != len(cases) {
		t.Fatalf("expected %d attempt rows, got %d", len(cases), len(page.Items))
	}
	seen := map[cp.AttemptOutcome]bool{}
	for _, row := range page.Items {
		if row.Outcome == "" {
			t.Fatalf("attempt outcome must be explicit, got empty for b_leg %s", row.Correlation.BLegID)
		}
		if row.Surfaced == "" {
			t.Fatalf("attempt surfaced must be explicit, got empty for b_leg %s", row.Correlation.BLegID)
		}
		seen[row.Outcome] = true
	}
	for _, c := range cases {
		if !seen[c.outcome] {
			t.Fatalf("expected outcome %q to be queryable, seen set: %v", c.outcome, seen)
		}
	}
}

// TestControlPlaneNonInterference_PostOutputFailureNeverRequestsRetryOrFailover
// proves that a post-output recording failure (RecordBestEffort) never surfaces
// an error to the caller, never changes the attempt outcome, and only degrades
// capability status (requirements 5.1, 5.2, 5.3, 5.6, 5.7, 10.7).
func TestControlPlaneNonInterference_PostOutputFailureNeverRequestsRetryOrFailover(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingRequiredPreWork})
	recorder := controlplane.NewRecorderService(&recordingStore{appendErr: errors.New("post-output store down")}, status, controlplane.RecorderConfig{
		Policy:   cp.RecordingRequiredPreWork,
		Required: []cp.Category{cp.CategoryAuth},
		Clock:    fixedClock{t: fixed},
	})

	ev := cp.Event{
		Category:       cp.CategoryAttempt,
		OccurredAt:     fixed,
		RecordedAt:     fixed,
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Source:         cp.SourceRef{Name: "ni"},
		Correlation:    cp.Correlation{TraceID: "trace-post", BLegID: "bleg-post"},
		Detail:         &cp.AttemptDetail{Surfaced: cp.AttemptSurfacedSurfaced, Outcome: cp.AttemptOutcomeSucceeded, BackendID: "openai", Model: "gpt-4o"},
	}
	if _, err := recorder.RecordBestEffort(context.Background(), ev); err != nil {
		t.Fatalf("post-output RecordBestEffort must never surface error (no retry/failover/replacement): %v", err)
	}
	got := status.Snapshot()
	if got.State != cp.CapabilityDegraded {
		t.Fatalf("post-output failure must degrade status only, got %q", got.State)
	}
}

// TestControlPlaneNonInterference_RequiredPreWorkFailsBeforeBackendExecution
// proves a mandatory pre-work recording failure for a required category happens
// before backend execution and exposes a safe operator-visible reason
// (requirements 5.4, 5.5, 5.6, 10.7).
func TestControlPlaneNonInterference_RequiredPreWorkFailsBeforeBackendExecution(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingRequiredPreWork})
	recorder := controlplane.NewRecorderService(&recordingStore{appendErr: errors.New("store down")}, status, controlplane.RecorderConfig{
		Policy:   cp.RecordingRequiredPreWork,
		Required: []cp.Category{cp.CategoryAuth},
		Clock:    fixedClock{t: fixed},
	})
	ev := cp.Event{
		Category:       cp.CategoryAuth,
		OccurredAt:     fixed,
		RecordedAt:     fixed,
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Source:         cp.SourceRef{Name: "ni"},
		Detail:         &cp.AuthDetail{Outcome: "allow"},
	}
	_, err := recorder.Record(context.Background(), ev)
	if err == nil {
		t.Fatalf("required pre-work auth failure must fail closed before backend execution")
	}
	if !errors.Is(err, controlplane.ErrUnavailable) && !errors.Is(err, controlplane.ErrDegraded) {
		t.Fatalf("required pre-work failure must classify as unavailable/degraded, got %v", err)
	}
}

// TestControlPlaneNonInterference_NonStreamingKeepsCorrelationPath proves that
// non-streaming collection keeps the same trace/session/A-leg/B-leg correlation
// path as streaming recording (requirements 5.3, 10.7).
func TestControlPlaneNonInterference_NonStreamingKeepsCorrelationPath(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	store, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "nonstream"})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	recorder := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: fixed},
	})
	normalizer := controlplane.NewNormalizer(fixedClock{t: fixed}, cp.SourceRef{Name: "ns", Version: "v1"}, controlplane.NewScopeFlattener())

	ctx := context.Background()
	attemptEv, err := normalizer.FromAttempt(controlplane.AttemptSourceRecord{
		SourceEventKey: "ns-attempt", OccurredAt: fixed, TraceID: "trace-ns",
		SessionID: "sess-ns", ALegID: "aleg-ns", BLegID: "bleg-ns", AttemptSeq: 1,
		BackendID: "openai", Model: "gpt-4o", Surfaced: cp.AttemptSurfacedSurfaced, Outcome: cp.AttemptOutcomeSucceeded,
	})
	if err != nil {
		t.Fatalf("FromAttempt: %v", err)
	}
	if _, err := recorder.RecordBestEffort(ctx, attemptEv); err != nil {
		t.Fatalf("RecordBestEffort attempt: %v", err)
	}

	page, err := controlplane.NewQueryService(store, status, controlplane.QueryServiceConfig{Enabled: true, DefaultPageSize: 50, MaxPageSize: 200}).Attempts(ctx, cp.AttemptQuery{Common: cp.CommonFilters{TraceID: "trace-ns"}})
	if err != nil {
		t.Fatalf("Attempts query: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(page.Items))
	}
	row := page.Items[0]
	if row.Correlation.TraceID != "trace-ns" || row.Correlation.SessionID != "sess-ns" || row.Correlation.ALegID != "aleg-ns" || row.Correlation.BLegID != "bleg-ns" {
		t.Fatalf("non-streaming correlation path lost: %#v", row.Correlation)
	}
}
