package observers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func sampleAuthDecision() sdkauth.AuthDecisionEvent {
	return sdkauth.AuthDecisionEvent{
		Time:        fixedTime,
		TraceID:     "trace-auth-1",
		Frontend:    "openai-responses",
		Outcome:     sdkauth.OutcomeAllow,
		ReasonCode:  "ok",
		HandlerKind: sdkauth.HandlerLocalAPIKey,
		Scope:       new(knownScope()),
	}
}

func sampleSessionStart() sdkauth.SessionStartEvent {
	return sdkauth.SessionStartEvent{
		Time:      fixedTime,
		TraceID:   "trace-auth-1",
		Frontend:  "openai-responses",
		SessionID: "sess-1",
		ALegID:    "aleg-1",
		IsNew:     true,
		Certainty: sdkauth.SessionCertaintyKnown,
	}
}

func TestAuthSinkAdapter_FansOutToDelegateAndRecorder(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	sink := &captureAuthSink{}
	adapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   sink,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})

	if err := adapter.OnAuthDecision(context.Background(), sampleAuthDecision()); err != nil {
		t.Fatalf("OnAuthDecision: %v", err)
	}
	if err := adapter.OnSessionStart(context.Background(), sampleSessionStart()); err != nil {
		t.Fatalf("OnSessionStart: %v", err)
	}

	if sink.authCalls != 1 || sink.sessionCall != 1 {
		t.Fatalf("delegate must receive one auth + one session call, got auth=%d session=%d",
			sink.authCalls, sink.sessionCall)
	}
	evs := h.events()
	if len(evs) != 2 {
		t.Fatalf("expected 2 recorded events, got %d", len(evs))
	}
	if evs[0].Category != cp.CategoryAuth || evs[0].Auth() == nil || evs[0].Auth().Outcome != "allow" {
		t.Fatalf("auth event not recorded correctly: %#v", evs[0])
	}
	if evs[0].Correlation.TraceID != "trace-auth-1" {
		t.Fatalf("auth event correlation lost: %#v", evs[0].Correlation)
	}
	if evs[1].Category != cp.CategorySession || evs[1].Session() == nil || evs[1].Session().SessionID != "sess-1" {
		t.Fatalf("session event not recorded correctly: %#v", evs[1])
	}
	// Deterministic source keys (design "Source Event Mapping").
	if evs[0].SourceEventKey != "auth:trace-auth-1:allow:ok" {
		t.Fatalf("auth source key = %q, want auth:trace-auth-1:allow:ok", evs[0].SourceEventKey)
	}
	if evs[1].SourceEventKey != "session-start:trace-auth-1:sess-1:aleg-1" {
		t.Fatalf("session source key = %q, want session-start:trace-auth-1:sess-1:aleg-1", evs[1].SourceEventKey)
	}
}

func TestAuthSinkAdapter_DisabledRecorderPreservesDelegateBehavior(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	sink := &captureAuthSink{authErr: nil}
	adapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   sink,
		Normalizer: h.normal,
		Recorder:   h.disabledRecorder(),
	})
	if err := adapter.OnAuthDecision(context.Background(), sampleAuthDecision()); err != nil {
		t.Fatalf("disabled recorder must not surface error: %v", err)
	}
	if sink.authCalls != 1 {
		t.Fatalf("delegate must still receive auth call when recording disabled, got %d", sink.authCalls)
	}
	if len(h.events()) != 0 {
		t.Fatalf("disabled recorder must not record events, got %d", len(h.events()))
	}
}

func TestAuthSinkAdapter_BestEffortRecordingFailurePreservesAuthOutcome(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	// Force store append failures by closing... instead use a failing store via
	// a recorder backed by a store that returns errors. Build a failing recorder.
	rec := newFailingRecorder(t, h.status, cp.RecordingBestEffort, nil)
	sink := &captureAuthSink{}
	adapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   sink,
		Normalizer: h.normal,
		Recorder:   rec,
	})
	if err := adapter.OnAuthDecision(context.Background(), sampleAuthDecision()); err != nil {
		t.Fatalf("best-effort recording failure must not surface to caller: %v", err)
	}
	if sink.authCalls != 1 {
		t.Fatalf("delegate must still be called on best-effort recording failure, got %d", sink.authCalls)
	}
	if got := h.status.Snapshot().State; got != cp.CapabilityDegraded {
		t.Fatalf("best-effort recording failure must degrade status, got %q", got)
	}
}

func TestAuthSinkAdapter_RequiredPreWorkFailClosedReturnsErrorBeforeDelegate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingRequiredPreWork, []cp.Category{cp.CategoryAuth})
	rec := newFailingRecorder(t, h.status, cp.RecordingRequiredPreWork, []cp.Category{cp.CategoryAuth})
	sink := &captureAuthSink{}
	adapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   sink,
		Normalizer: h.normal,
		Recorder:   rec,
		FailClosed: true,
	})
	err := adapter.OnAuthDecision(context.Background(), sampleAuthDecision())
	if err == nil {
		t.Fatalf("required pre-work fail-closed must return error before upstream")
	}
	if !errors.Is(err, controlplane.ErrDegraded) && !errors.Is(err, controlplane.ErrUnavailable) {
		t.Fatalf("expected degraded/unavailable classification, got %v", err)
	}
	if sink.authCalls != 0 {
		t.Fatalf("fail-closed must skip delegate so upstream auth delivery does not proceed, got %d", sink.authCalls)
	}
}

func TestAuthSinkAdapter_RequiredPreWorkButBestEffortDeliveryDoesNotFailClosed(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingRequiredPreWork, []cp.Category{cp.CategoryAuth})
	rec := newFailingRecorder(t, h.status, cp.RecordingRequiredPreWork, []cp.Category{cp.CategoryAuth})
	sink := &captureAuthSink{}
	adapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   sink,
		Normalizer: h.normal,
		Recorder:   rec,
		FailClosed: false, // existing auth event delivery policy is best-effort
	})
	if err := adapter.OnAuthDecision(context.Background(), sampleAuthDecision()); err != nil {
		t.Fatalf("best-effort auth delivery must not fail closed on recording failure: %v", err)
	}
	if sink.authCalls != 1 {
		t.Fatalf("delegate must still be called when auth delivery is best-effort, got %d", sink.authCalls)
	}
}

func TestAuthSinkAdapter_DelegateErrorPropagates(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	sink := &captureAuthSink{authErr: errors.New("sink down")}
	adapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   sink,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	err := adapter.OnAuthDecision(context.Background(), sampleAuthDecision())
	if err == nil || err.Error() != "sink down" {
		t.Fatalf("delegate error must propagate unchanged, got %v", err)
	}
	// Recording still happened (fan-out is independent of delegate outcome).
	if len(h.events()) != 1 {
		t.Fatalf("recording must happen regardless of delegate error, got %d events", len(h.events()))
	}
}

func TestAuthSinkAdapter_NilDelegateRecordsOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	adapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   nil,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	if err := adapter.OnAuthDecision(context.Background(), sampleAuthDecision()); err != nil {
		t.Fatalf("nil delegate must not surface error: %v", err)
	}
	if len(h.events()) != 1 {
		t.Fatalf("nil delegate must still record, got %d", len(h.events()))
	}
}
