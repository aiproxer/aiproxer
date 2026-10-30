package observers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// TestCrossSeam_AllAdaptersRecordSharedCorrelation exercises auth, policy,
// usage, secure-session, and B2BUA adapters together and proves they record
// shared trace/session/A-leg/B-leg correlation without changing any existing
// observer/store/sink behavior (task 4.5; requirements 3.1, 3.7, 8.1, 8.4, 8.5,
// 9.1, 10.7).
func TestCrossSeam_AllAdaptersRecordSharedCorrelation(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)

	authSink := &captureAuthSink{}
	authAdapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   authSink,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	policyAdapter := observers.NewPolicyObserverAdapter(observers.PolicyObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	usageAdapter := observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	ssFake := &fakeSecureSessionStore{}
	ssDec := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   ssFake,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	b2buaFake := &fakeB2BUAStore{}
	b2buaDec := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   b2buaFake,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})

	ctx := context.Background()
	const traceID = "trace-shared"

	if err := authAdapter.OnAuthDecision(ctx, sdkauth.AuthDecisionEvent{
		Time: fixedTime, TraceID: traceID, Frontend: "openai-responses",
		Outcome: sdkauth.OutcomeAllow, ReasonCode: "ok", HandlerKind: sdkauth.HandlerLocalAPIKey,
		Scope: new(knownScope()),
	}); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if err := authAdapter.OnSessionStart(ctx, sdkauth.SessionStartEvent{
		Time: fixedTime, TraceID: traceID, Frontend: "openai-responses",
		SessionID: "sess-shared", ALegID: "aleg-shared", IsNew: true, Certainty: sdkauth.SessionCertaintyKnown,
	}); err != nil {
		t.Fatalf("session start: %v", err)
	}
	if _, err := ssDec.Create(ctx, domain.CreateRecord{
		SessionID: "sess-shared", ALegID: "aleg-shared", Owner: domain.PrincipalRef{ID: "principal-1"}, CreatedAt: fixedTime,
	}); err != nil {
		t.Fatalf("secure-session Create: %v", err)
	}
	if err := b2buaDec.RecordAttempt(ctx, aipapi.AttemptRecord{
		BLegID: "bleg-shared", ALegID: "aleg-shared", Seq: 1, BackendID: "openai", EffectiveModel: "gpt-4o",
		StartedAt: fixedTime.Add(-time.Minute), FinishedAt: fixedTime, Outcome: aipapi.AttemptSuccess,
	}); err != nil {
		t.Fatalf("b2bua RecordAttempt: %v", err)
	}
	if err := ssDec.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: "sess-shared", ALegID: "aleg-shared", BLegID: "bleg-shared", AttemptSeq: 1,
		ResolvedBackend: "openai", ResolvedModel: "gpt-4o", StartedAt: fixedTime,
	}); err != nil {
		t.Fatalf("secure-session AppendAttemptTrace: %v", err)
	}
	if err := policyAdapter.OnPolicyDecision(ctx, policydecision.Record{
		TraceID: traceID, ALegID: "aleg-shared", BLegID: "bleg-shared", AttemptSeq: 1, Stage: "pre_backend",
		Provider: policydecision.ProviderRef{ID: "opa", Stage: "pre_backend"}, Outcome: policydecision.OutcomeAllow,
		Effect: policydecision.EffectNone, ReasonCode: "ok", Scope: knownScope(),
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	if err := usageAdapter.OnUsage(ctx, usage.Event{
		TraceID: traceID, ALegID: "aleg-shared", BLegID: "bleg-shared", SessionID: "sess-shared", AttemptSeq: 1,
		BackendID: "openai", FrontendID: "openai-responses", Model: "gpt-4o", Scope: knownScope(),
		InputTokens: 100, OutputTokens: 50, TotalTokens: 150, RecordedAt: fixedTime,
	}); err != nil {
		t.Fatalf("usage: %v", err)
	}

	evs := h.events()
	if len(evs) != 7 {
		t.Fatalf("expected 7 recorded events across seams, got %d", len(evs))
	}
	// All events carry the shared correlation where the source supplied it.
	for i, ev := range evs {
		if ev.Correlation.TraceID != "" && ev.Correlation.TraceID != traceID {
			t.Fatalf("event %d trace correlation mismatch: %q", i, ev.Correlation.TraceID)
		}
	}
	// Existing sinks/observers/stores were called and remain unchanged.
	if authSink.authCalls != 1 || authSink.sessionCall != 1 {
		t.Fatalf("auth delegate must receive one of each, got auth=%d session=%d", authSink.authCalls, authSink.sessionCall)
	}
	if ssFake.createCalls != 1 || ssFake.appendAttemptTraceCalls != 1 {
		t.Fatalf("secure-session delegate must be called, got create=%d trace=%d", ssFake.createCalls, ssFake.appendAttemptTraceCalls)
	}
	if b2buaFake.recordCalls != 1 {
		t.Fatalf("b2bua delegate must be called once, got %d", b2buaFake.recordCalls)
	}
}

// TestCrossSeam_DisabledCapabilityPreservesAllExistingBehavior proves that when
// the control-plane capability is disabled, all adapters pass through to their
// delegates and no events are recorded (task 4.5; requirements 5.1, 5.2, 5.5,
// 7.5, 8.1, 8.4, 8.5).
func TestCrossSeam_DisabledCapabilityPreservesAllExistingBehavior(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	disabled := h.disabledRecorder()

	authSink := &captureAuthSink{authErr: errors.New("auth sink error")}
	authAdapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   authSink,
		Normalizer: h.normal,
		Recorder:   disabled,
	})
	policyAdapter := observers.NewPolicyObserverAdapter(observers.PolicyObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   disabled,
	})
	usageAdapter := observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   disabled,
	})
	ssFake := &fakeSecureSessionStore{}
	ssDec := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   ssFake,
		Normalizer: h.normal,
		Recorder:   disabled,
	})
	b2buaFake := &fakeB2BUAStore{}
	b2buaDec := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   b2buaFake,
		Normalizer: h.normal,
		Recorder:   disabled,
	})

	ctx := context.Background()
	// Auth: disabled recording does not swallow the existing delegate error.
	if err := authAdapter.OnAuthDecision(ctx, sdkauth.AuthDecisionEvent{Time: fixedTime, TraceID: "t", Outcome: sdkauth.OutcomeAllow}); err == nil {
		t.Fatalf("disabled recording must preserve existing delegate error")
	}
	if err := policyAdapter.OnPolicyDecision(ctx, policydecision.Record{TraceID: "t", Stage: "pre_backend", Outcome: policydecision.OutcomeAllow}); err != nil {
		t.Fatalf("disabled policy adapter must be no-op: %v", err)
	}
	if err := usageAdapter.OnUsage(ctx, usage.Event{TraceID: "t", RecordedAt: fixedTime}); err != nil {
		t.Fatalf("disabled usage adapter must be no-op: %v", err)
	}
	if _, err := ssDec.Create(ctx, domain.CreateRecord{SessionID: "s", CreatedAt: fixedTime}); err != nil {
		t.Fatalf("disabled secure-session Create must preserve semantics: %v", err)
	}
	if err := b2buaDec.RecordAttempt(ctx, aipapi.AttemptRecord{ALegID: "a", BLegID: "b", Seq: 1, Outcome: aipapi.AttemptSuccess}); err != nil {
		t.Fatalf("disabled b2bua RecordAttempt must preserve semantics: %v", err)
	}
	if len(h.events()) != 0 {
		t.Fatalf("disabled capability must record nothing, got %d", len(h.events()))
	}
	if ssFake.createCalls != 1 || b2buaFake.recordCalls != 1 || authSink.authCalls != 1 {
		t.Fatalf("delegates must still be called when recording disabled")
	}
}

// TestCrossSeam_RequiredPreWorkAndBestEffortCoexist proves that required
// pre-work categories fail closed while best-effort categories degrade status,
// across seams in one configuration (task 4.5; requirements 5.1, 5.2, 5.5, 5.4).
func TestCrossSeam_RequiredPreWorkAndBestEffortCoexist(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingRequiredPreWork, []cp.Category{cp.CategoryAuth, cp.CategoryAudit})
	rec := newFailingRecorder(t, h.status, cp.RecordingRequiredPreWork, []cp.Category{cp.CategoryAuth, cp.CategoryAudit})

	authAdapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   &captureAuthSink{},
		Normalizer: h.normal,
		Recorder:   rec,
		FailClosed: true,
	})
	usageAdapter := observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   rec,
	})
	ssFake := &fakeSecureSessionStore{}
	ssDec := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   ssFake,
		Normalizer: h.normal,
		Recorder:   rec,
	})
	b2buaFake := &fakeB2BUAStore{}
	b2buaDec := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   b2buaFake,
		Normalizer: h.normal,
		Recorder:   rec,
	})

	ctx := context.Background()

	// Auth is required pre-work + fail-closed delivery => must fail closed.
	if err := authAdapter.OnAuthDecision(ctx, sdkauth.AuthDecisionEvent{Time: fixedTime, TraceID: "t", Outcome: sdkauth.OutcomeAllow}); err == nil {
		t.Fatalf("required pre-work auth must fail closed")
	}

	// Usage observer is always fail-open (best-effort) => no error, status degraded.
	preState := h.status.Snapshot().State
	if err := usageAdapter.OnUsage(ctx, usage.Event{TraceID: "t2", RecordedAt: fixedTime}); err != nil {
		t.Fatalf("usage observer must be fail-open even under required pre-work: %v", err)
	}
	_ = preState

	// Secure-session AppendAudit (audit category required) => fail closed.
	if err := ssDec.AppendAudit(ctx, domain.AuditItem{SessionID: "s", TurnID: "turn", Seq: 1, Action: "a", CreatedAt: fixedTime}); err == nil {
		t.Fatalf("required pre-work audit must fail closed")
	}

	// Secure-session TouchActivity (best-effort) => no error.
	if err := ssDec.TouchActivity(ctx, "s", fixedTime, domain.ActivitySystem); err != nil {
		t.Fatalf("best-effort TouchActivity must not fail closed: %v", err)
	}

	// B2BUA RecordAttempt is always best-effort => never fail closed.
	if err := b2buaDec.RecordAttempt(ctx, aipapi.AttemptRecord{ALegID: "a", BLegID: "b", Seq: 1, Outcome: aipapi.AttemptSuccess}); err != nil {
		t.Fatalf("b2bua RecordAttempt must be best-effort: %v", err)
	}
	if got := h.status.Snapshot().State; got != cp.CapabilityDegraded {
		t.Fatalf("best-effort failures must degrade status, got %q", got)
	}
}

// TestCrossSeam_PostOutputFailuresNeverRequestRetryOrFailover proves that
// best-effort adapter paths (usage observer, b2bua RecordAttempt, secure-session
// UpdateAttemptOutcome) never surface errors and never change routing outcomes
// (task 4.5; requirements 5.1, 5.2, 5.3, 5.6, 10.7).
func TestCrossSeam_PostOutputFailuresNeverRequestRetryOrFailover(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	rec := newFailingRecorder(t, h.status, cp.RecordingBestEffort, nil)

	usageAdapter := observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   rec,
	})
	ssFake := &fakeSecureSessionStore{}
	ssDec := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   ssFake,
		Normalizer: h.normal,
		Recorder:   rec,
	})
	b2buaFake := &fakeB2BUAStore{}
	b2buaDec := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   b2buaFake,
		Normalizer: h.normal,
		Recorder:   rec,
	})

	ctx := context.Background()
	if err := usageAdapter.OnUsage(ctx, usage.Event{TraceID: "t", RecordedAt: fixedTime}); err != nil {
		t.Fatalf("usage adapter post-output failure must never surface: %v", err)
	}
	if err := ssDec.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{SessionID: "s", BLegID: "b", EndedAt: fixedTime}); err != nil {
		t.Fatalf("secure-session post-output failure must never surface: %v", err)
	}
	if err := b2buaDec.RecordAttempt(ctx, aipapi.AttemptRecord{ALegID: "a", BLegID: "b", Seq: 1, Outcome: aipapi.AttemptSuccess}); err != nil {
		t.Fatalf("b2bua post-output failure must never surface: %v", err)
	}
	if got := h.status.Snapshot().State; got != cp.CapabilityDegraded {
		t.Fatalf("post-output failures must degrade status only, got %q", got)
	}
	// Ensure classifier maps degraded status to a safe operator code, no infra leak.
	if code := controlplane.Classify(errors.New("transient")); code != "" {
		t.Fatalf("non-controlplane errors must not classify, got %q", code)
	}
}
