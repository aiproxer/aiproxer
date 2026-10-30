package ledgerstore

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// TestControlPlaneCoreServicesEndToEndWithMemoryStore proves the Phase 3 core
// services (normalizer, recorder, query, retention) work against the real
// in-memory store adapter end to end: record safe evidence, query it back
// through bounded pages, apply retention, and observe expired/redacted state.
func TestControlPlaneCoreServicesEndToEndWithMemoryStore(t *testing.T) {
	t.Parallel()
	store, err := NewMemoryStore(MemoryConfig{StoreID: "e2e"})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	recorder := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClockE2E{t: time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)},
	})
	normalizer := controlplane.NewNormalizer(
		fixedClockE2E{t: time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)},
		cp.SourceRef{Name: "e2e-source", Version: "v1"},
		controlplane.NewScopeFlattener(),
	)

	view := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("principal-e2e"),
		TenantID:    scope.Known("tenant-e2e"),
		Origin:      scope.OriginClient,
		Roles:       []string{"ops"},
	}

	// Record an auth decision.
	authEv, err := normalizer.FromAuthDecision(auth.AuthDecisionEvent{
		Time:        time.Date(2026, 7, 4, 0, 1, 0, 0, time.UTC),
		TraceID:     "trace-e2e",
		Frontend:    "openai-responses",
		Outcome:     auth.OutcomeAllow,
		ReasonCode:  "ok",
		PrincipalID: "principal-e2e",
		Scope:       &view,
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	if _, err := recorder.Record(context.Background(), authEv); err != nil {
		t.Fatalf("Record auth: %v", err)
	}

	// Record a session start.
	sessEv, err := normalizer.FromSessionStart(auth.SessionStartEvent{
		Time:      time.Date(2026, 7, 4, 0, 2, 0, 0, time.UTC),
		TraceID:   "trace-e2e",
		SessionID: "sess-e2e",
		ALegID:    "aleg-e2e",
		IsNew:     true,
	})
	if err != nil {
		t.Fatalf("FromSessionStart: %v", err)
	}
	if _, err := recorder.Record(context.Background(), sessEv); err != nil {
		t.Fatalf("Record session: %v", err)
	}

	// Record an attempt.
	attemptEv, err := normalizer.FromAttempt(controlplane.AttemptSourceRecord{
		OccurredAt:   time.Date(2026, 7, 4, 0, 3, 0, 0, time.UTC),
		TraceID:      "trace-e2e",
		SessionID:    "sess-e2e",
		ALegID:       "aleg-e2e",
		BLegID:       "bleg-e2e",
		AttemptSeq:   1,
		BackendID:    "openai",
		Model:        "gpt-4o",
		RouteOutcome: "primary",
		Surfaced:     cp.AttemptSurfacedSurfaced,
		Outcome:      cp.AttemptOutcomeSucceeded,
		Scope:        &view,
	})
	if err != nil {
		t.Fatalf("FromAttempt: %v", err)
	}
	if _, err := recorder.Record(context.Background(), attemptEv); err != nil {
		t.Fatalf("Record attempt: %v", err)
	}

	// Record usage.
	usageEv, err := normalizer.FromUsage(usage.Event{
		TraceID:       "trace-e2e",
		SessionID:     "sess-e2e",
		ALegID:        "aleg-e2e",
		BLegID:        "bleg-e2e",
		AttemptSeq:    1,
		BackendID:     "openai",
		Model:         "gpt-4o",
		Scope:         view,
		InputTokens:   100,
		OutputTokens:  50,
		TotalTokens:   150,
		CostNanoUnits: 1000,
		Currency:      "USD",
		RecordedAt:    time.Date(2026, 7, 4, 0, 4, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("FromUsage: %v", err)
	}
	if _, err := recorder.Record(context.Background(), usageEv); err != nil {
		t.Fatalf("Record usage: %v", err)
	}

	// Record a policy decision.
	policyEv, err := normalizer.FromPolicyDecision(policydecision.Record{
		TraceID:    "trace-e2e",
		ALegID:     "aleg-e2e",
		BLegID:     "bleg-e2e",
		AttemptSeq: 1,
		Stage:      "pre_backend",
		Provider:   policydecision.ProviderRef{ID: "opa", Stage: "pre_backend"},
		Outcome:    policydecision.OutcomeAllow,
		Effect:     policydecision.EffectNone,
		ReasonCode: "default_allow",
		Scope:      view,
	})
	if err != nil {
		t.Fatalf("FromPolicyDecision: %v", err)
	}
	if _, err := recorder.Record(context.Background(), policyEv); err != nil {
		t.Fatalf("Record policy: %v", err)
	}

	// Query sessions through the bounded query service.
	qs := controlplane.NewQueryService(store, status, controlplane.QueryServiceConfig{
		Enabled:         true,
		DefaultPageSize: 50,
		MaxPageSize:     200,
	})
	sessionsPage, err := qs.Sessions(context.Background(), cp.SessionQuery{
		Common: cp.CommonFilters{Scope: cp.ScopeFilters{PrincipalID: scope.Known("principal-e2e")}},
	})
	if err != nil {
		t.Fatalf("Sessions query: %v", err)
	}
	if len(sessionsPage.Items) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessionsPage.Items))
	}
	if sessionsPage.Items[0].SessionID != "sess-e2e" {
		t.Fatalf("session id = %q, want sess-e2e", sessionsPage.Items[0].SessionID)
	}
	if !sessionsPage.Items[0].Scope.PrincipalID.Equal(scope.Known("principal-e2e")) {
		t.Fatalf("session scope principal lost: %#v", sessionsPage.Items[0].Scope.PrincipalID)
	}
	if sessionsPage.Items[0].AttemptCount != 1 {
		t.Fatalf("attempt count = %d, want 1", sessionsPage.Items[0].AttemptCount)
	}

	// Query attempts and verify surfaced state is distinguishable.
	attemptsPage, err := qs.Attempts(context.Background(), cp.AttemptQuery{
		Common: cp.CommonFilters{SessionID: "sess-e2e"},
	})
	if err != nil {
		t.Fatalf("Attempts query: %v", err)
	}
	if len(attemptsPage.Items) != 1 || attemptsPage.Items[0].Surfaced != cp.AttemptSurfacedSurfaced {
		t.Fatalf("attempt surfaced state lost: %+v", attemptsPage.Items)
	}

	// Query policy/audit evidence.
	policyPage, err := qs.PolicyAudit(context.Background(), cp.EvidenceQuery{
		Common: cp.CommonFilters{TraceID: "trace-e2e"},
	})
	if err != nil {
		t.Fatalf("PolicyAudit query: %v", err)
	}
	if len(policyPage.Items) != 1 || policyPage.Items[0].Outcome != "allow" {
		t.Fatalf("policy outcome lost: %+v", policyPage.Items)
	}

	// Apply retention with a cutoff after the recorded events: nothing expires.
	ctrl := controlplane.NewRetentionController(store, status, controlplane.RetentionControllerConfig{
		Profile: controlplane.RetentionProfileStandard,
		Window:  24 * time.Hour,
		Clock:   fixedClockE2E{t: time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)},
	})
	res, err := ctrl.Apply(context.Background(), time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC), cp.VisibilityDefault)
	if err != nil {
		t.Fatalf("Retention Apply: %v", err)
	}
	// All recorded events occurred after 2026-07-04 00:00, so none expire at that cutoff.
	// The store may mark 0 records here.
	_ = res

	// Apply retention with a cutoff after all events: they become expired.
	res2, err := ctrl.Apply(context.Background(), time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC), cp.VisibilityDefault)
	if err != nil {
		t.Fatalf("Retention Apply 2: %v", err)
	}
	if res2.Marked == 0 {
		t.Fatalf("expected records to be marked expired after cutoff beyond event times")
	}

	// After retention, the events query should report expired evidence state.
	eventsPage, err := qs.Events(context.Background(), cp.EventQuery{
		Common: cp.CommonFilters{TraceID: "trace-e2e"},
	})
	if err != nil {
		t.Fatalf("Events query after retention: %v", err)
	}
	for _, ev := range eventsPage.Items {
		if ev.EvidenceState != cp.EvidenceExpired {
			t.Fatalf("expected expired evidence state after retention, got %q for category %q", ev.EvidenceState, ev.Category)
		}
	}
}

type fixedClockE2E struct{ t time.Time }

func (f fixedClockE2E) Now() time.Time { return f.t }
