package observers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// TestCompatibility_SecureSessionReadsArePurePassThrough proves task 6.3: the
// secure-session decorator's read methods (LoadByID, LoadByALegID, Audit,
// Transcript, Summary, ListAttemptEvidence, NextAuditSeq, NextTranscriptSeq,
// CheckReadiness) never record and return exactly what the delegate returns,
// so existing secure-session list/detail/transcript/audit/by-A-leg diagnostics
// remain unchanged when control-plane recording is enabled (requirements 8.1,
// 8.5, 8.6, 10.6, 10.7).
func TestCompatibility_SecureSessionReadsArePurePassThrough(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	delegate := &fakeSecureSessionStore{}
	dec := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   delegate,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	ctx := context.Background()

	// Read methods return delegate values/errors verbatim.
	if got, err := dec.Audit(ctx, "s", domain.ReadOptions{}); err != nil || len(got) != 0 {
		t.Fatalf("Audit pass-through: got %v err %v", got, err)
	}
	if got, err := dec.Transcript(ctx, "s", domain.ReadOptions{}); err != nil || len(got) != 0 {
		t.Fatalf("Transcript pass-through: got %v err %v", got, err)
	}
	if got, err := dec.ListAttemptEvidence(ctx, "s", domain.ReadOptions{}); err != nil || len(got) != 0 {
		t.Fatalf("ListAttemptEvidence pass-through: got %v err %v", got, err)
	}
	if got, err := dec.Summary(ctx, domain.SummaryQuery{}); err != nil || len(got) != 0 {
		t.Fatalf("Summary pass-through: got %v err %v", got, err)
	}
	if _, err := dec.LoadByID(ctx, "s"); err != nil {
		t.Fatalf("LoadByID pass-through: %v", err)
	}
	if _, err := dec.LoadByALegID(ctx, "aleg-1"); err != nil {
		t.Fatalf("LoadByALegID pass-through: %v", err)
	}
	if _, err := dec.LoadByResumeFingerprint(ctx, domain.TokenFingerprint{}); err != nil {
		t.Fatalf("LoadByResumeFingerprint pass-through: %v", err)
	}
	if seq, err := dec.NextAuditSeq(ctx, "s"); err != nil || seq != 1 {
		t.Fatalf("NextAuditSeq pass-through: got %d err %v", seq, err)
	}
	if seq, err := dec.NextTranscriptSeq(ctx, "s"); err != nil || seq != 1 {
		t.Fatalf("NextTranscriptSeq pass-through: got %d err %v", seq, err)
	}
	if err := dec.CheckReadiness(ctx, domain.PolicyMetadata{}); err != nil {
		t.Fatalf("CheckReadiness pass-through: %v", err)
	}

	// No events were recorded for read-only operations.
	if len(h.events()) != 0 {
		t.Fatalf("read-only operations must not record, got %d events", len(h.events()))
	}
}

// TestCompatibility_SecureSessionMutationsPreserveDelegateData proves that
// enabling control-plane recording does not change what the delegate store
// records: the delegate receives the same create/trace/audit/outcome/usage
// calls with the same data, and its return values surface unchanged
// (requirements 8.1, 8.5, 10.6, 10.7).
func TestCompatibility_SecureSessionMutationsPreserveDelegateData(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	delegate := &fakeSecureSessionStore{}
	dec := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   delegate,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	ctx := context.Background()

	createRec, err := dec.Create(ctx, domain.CreateRecord{
		SessionID: "sess-c", ALegID: "aleg-c", Owner: domain.PrincipalRef{ID: "principal-c"}, CreatedAt: fixedTime,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if createRec.SessionID != "sess-c" || delegate.createCalls != 1 {
		t.Fatalf("delegate Create must be called once with preserved result: %#v calls=%d", createRec, delegate.createCalls)
	}
	if delegate.appendAttemptTraceCalls != 0 {
		t.Fatalf("unexpected trace recorded before AppendAttemptTrace: calls=%d", delegate.appendAttemptTraceCalls)
	}
	trace := domain.AttemptTrace{
		SessionID: "sess-c", ALegID: "aleg-c", BLegID: "bleg-c", AttemptSeq: 1,
		ResolvedBackend: "openai", ResolvedModel: "gpt-4o", StartedAt: fixedTime,
	}
	if err := dec.AppendAttemptTrace(ctx, trace); err != nil {
		t.Fatalf("AppendAttemptTrace: %v", err)
	}
	if delegate.appendAttemptTraceCalls != 1 {
		t.Fatalf("delegate AppendAttemptTrace must be called once, got %d", delegate.appendAttemptTraceCalls)
	}
	if delegate.lastAttemptTrace.SessionID != trace.SessionID || delegate.lastAttemptTrace.BLegID != trace.BLegID || delegate.lastAttemptTrace.AttemptSeq != trace.AttemptSeq || delegate.lastAttemptTrace.ResolvedBackend != trace.ResolvedBackend {
		t.Fatalf("delegate must receive identical trace: got=%#v", delegate.lastAttemptTrace)
	}
	if err := dec.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{SessionID: "sess-c", BLegID: "bleg-c", EndedAt: fixedTime}); err != nil {
		t.Fatalf("UpdateAttemptOutcome: %v", err)
	}
	if delegate.updateAttemptOutcomeCalls != 1 {
		t.Fatalf("delegate UpdateAttemptOutcome must be called once, got %d", delegate.updateAttemptOutcomeCalls)
	}
}

// TestCompatibility_AuthSinkDelegateReceivesIdenticalEvents proves the auth
// sink adapter forwards the exact source event to the existing delegate, so
// existing auth event delivery behavior is unchanged when control-plane
// recording is enabled (requirements 8.4, 8.5, 10.7).
func TestCompatibility_AuthSinkDelegateReceivesIdenticalEvents(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	delegate := &captureAuthSink{}
	adapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   delegate,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	ctx := context.Background()
	authEv := sdkauth.AuthDecisionEvent{
		Time: fixedTime, TraceID: "trace-compat", Frontend: "openai-responses",
		Outcome: sdkauth.OutcomeAllow, ReasonCode: "ok", HandlerKind: sdkauth.HandlerLocalAPIKey,
	}
	if err := adapter.OnAuthDecision(ctx, authEv); err != nil {
		t.Fatalf("OnAuthDecision: %v", err)
	}
	if delegate.authCalls != 1 || delegate.lastAuth.TraceID != authEv.TraceID || delegate.lastAuth.Outcome != authEv.Outcome || delegate.lastAuth.ReasonCode != authEv.ReasonCode {
		t.Fatalf("delegate must receive identical auth event: calls=%d got=%#v", delegate.authCalls, delegate.lastAuth)
	}
	sessEv := sdkauth.SessionStartEvent{
		Time: fixedTime, TraceID: "trace-compat", Frontend: "openai-responses",
		SessionID: "sess-compat", ALegID: "aleg-compat", IsNew: true,
	}
	if err := adapter.OnSessionStart(ctx, sessEv); err != nil {
		t.Fatalf("OnSessionStart: %v", err)
	}
	if delegate.sessionCall != 1 || delegate.lastSession.SessionID != sessEv.SessionID || delegate.lastSession.ALegID != sessEv.ALegID {
		t.Fatalf("delegate must receive identical session event: calls=%d got=%#v", delegate.sessionCall, delegate.lastSession)
	}
}

// TestCompatibility_AuthSinkDisabledRecordingPreservesDelegateError proves that
// when the control-plane capability is disabled, the auth adapter preserves the
// existing delegate error verbatim (requirement 8.4, 8.5).
func TestCompatibility_AuthSinkDisabledRecordingPreservesDelegateError(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	delegateErr := errors.New("existing auth sink failure")
	delegate := &captureAuthSink{authErr: delegateErr}
	adapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   delegate,
		Normalizer: h.normal,
		Recorder:   h.disabledRecorder(),
	})
	if err := adapter.OnAuthDecision(context.Background(), sdkauth.AuthDecisionEvent{Time: fixedTime, TraceID: "t", Outcome: sdkauth.OutcomeAllow}); err != delegateErr {
		t.Fatalf("disabled recording must preserve existing delegate error, got %v want %v", err, delegateErr)
	}
}

// TestCompatibility_B2BUADecoratorPreservesDelegateOutcomes proves the B2BUA
// decorator preserves attempt lineage semantics: the delegate receives the
// attempt record unchanged and its error surfaces verbatim (requirements 8.3,
// 8.5, 10.7).
func TestCompatibility_B2BUADecoratorPreservesDelegateOutcomes(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	delegate := &fakeB2BUAStore{recordAttemptErr: errors.New("b2bua delegate failure")}
	dec := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   delegate,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	rec := aipapiAttemptRecord()
	if err := dec.RecordAttempt(context.Background(), rec); err == nil {
		t.Fatalf("delegate failure must surface verbatim")
	}
	if delegate.recordCalls != 1 || delegate.lastAttempt.ALegID != rec.ALegID || delegate.lastAttempt.BLegID != rec.BLegID || delegate.lastAttempt.BackendID != rec.BackendID {
		t.Fatalf("delegate must receive identical attempt record: calls=%d got=%#v", delegate.recordCalls, delegate.lastAttempt)
	}
}

func aipapiAttemptRecord() aipapi.AttemptRecord {
	return aipapi.AttemptRecord{ALegID: "aleg-x", BLegID: "bleg-x", Seq: 1, BackendID: "openai", EffectiveModel: "gpt-4o"}
}
