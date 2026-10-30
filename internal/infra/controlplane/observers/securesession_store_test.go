package observers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func sampleCreateRecord() domain.CreateRecord {
	return domain.CreateRecord{
		SessionID: "sess-1",
		ALegID:    "aleg-1",
		Owner:     domain.PrincipalRef{ID: "principal-1"},
		CreatedAt: fixedTime,
	}
}

func newSecureSessionDecorator(t *testing.T, h *harness, fake *fakeSecureSessionStore) *observers.SecureSessionStoreDecorator {
	t.Helper()
	return observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   fake,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
}

func TestSecureSession_CreateRecordsSessionEventAfterDelegate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{}
	dec := newSecureSessionDecorator(t, h, fake)

	rec, err := dec.Create(context.Background(), sampleCreateRecord())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.SessionID != "sess-1" {
		t.Fatalf("delegate result must be preserved, got %q", rec.SessionID)
	}
	if fake.createCalls != 1 {
		t.Fatalf("delegate Create must be called once, got %d", fake.createCalls)
	}
	evs := h.events()
	if len(evs) != 1 || evs[0].Category != cp.CategorySession || evs[0].Session() == nil {
		t.Fatalf("session event not recorded: %#v", evs)
	}
	if evs[0].SourceEventKey != "secure-create:sess-1" {
		t.Fatalf("source key = %q, want secure-create:sess-1", evs[0].SourceEventKey)
	}
	if evs[0].Session().SessionID != "sess-1" || evs[0].Session().ALegID != "aleg-1" {
		t.Fatalf("session correlation lost: %#v", evs[0].Session())
	}
}

func TestSecureSession_CreateDelegateFailureSurfacesAndDoesNotRecord(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{createErr: errors.New("create failed")}
	dec := newSecureSessionDecorator(t, h, fake)

	if _, err := dec.Create(context.Background(), sampleCreateRecord()); err == nil {
		t.Fatalf("delegate failure must surface")
	}
	if len(h.events()) != 0 {
		t.Fatalf("recording must not happen on delegate failure, got %d", len(h.events()))
	}
}

func TestSecureSession_TouchActivityRecordsSessionUpdate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{}
	dec := newSecureSessionDecorator(t, h, fake)

	at := fixedTime.Add(time.Minute)
	if err := dec.TouchActivity(context.Background(), "sess-1", at, domain.ActivityClientRequest); err != nil {
		t.Fatalf("TouchActivity: %v", err)
	}
	if fake.touchCalls != 1 {
		t.Fatalf("delegate TouchActivity must be called once, got %d", fake.touchCalls)
	}
	evs := h.events()
	if len(evs) != 1 || evs[0].Session() == nil || evs[0].Session().Action != cp.SessionActionUpdated {
		t.Fatalf("session update not recorded: %#v", evs)
	}
	want := "secure-touch:sess-1:client_request:" + at.Format(time.RFC3339Nano)
	if evs[0].SourceEventKey != want {
		t.Fatalf("source key = %q, want %q", evs[0].SourceEventKey, want)
	}
}

func TestSecureSession_AppendAttemptTraceRecordsAttempt(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{}
	dec := newSecureSessionDecorator(t, h, fake)

	trace := domain.AttemptTrace{
		SessionID: "sess-1", TurnID: "turn-1", ALegID: "aleg-1", BLegID: "bleg-1", AttemptSeq: 3,
		ResolvedBackend: "openai", ResolvedModel: "gpt-4o", RouteSource: "selector", StartedAt: fixedTime,
	}
	if err := dec.AppendAttemptTrace(context.Background(), trace); err != nil {
		t.Fatalf("AppendAttemptTrace: %v", err)
	}
	evs := h.events()
	if len(evs) != 1 || evs[0].Attempt() == nil {
		t.Fatalf("attempt event not recorded: %#v", evs)
	}
	if evs[0].SourceEventKey != "secure-attempt-trace:sess-1:bleg-1:3" {
		t.Fatalf("source key = %q", evs[0].SourceEventKey)
	}
	if evs[0].Attempt().BackendID != "openai" || evs[0].Attempt().Model != "gpt-4o" {
		t.Fatalf("backend/model lost: %#v", evs[0].Attempt())
	}
}

func TestSecureSession_UpdateAttemptOutcomeMapsSurfaceState(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{}
	dec := newSecureSessionDecorator(t, h, fake)

	out := domain.AttemptOutcome{
		SessionID: "sess-1", BLegID: "bleg-1", Success: true, SurfaceState: domain.SurfaceSurfaced,
		EndedAt: fixedTime,
	}
	if err := dec.UpdateAttemptOutcome(context.Background(), out); err != nil {
		t.Fatalf("UpdateAttemptOutcome: %v", err)
	}
	evs := h.events()
	if len(evs) != 1 || evs[0].Attempt() == nil {
		t.Fatalf("attempt outcome not recorded: %#v", evs)
	}
	if evs[0].Attempt().Surfaced != cp.AttemptSurfacedSurfaced || evs[0].Attempt().Outcome != cp.AttemptOutcomeSucceeded {
		t.Fatalf("surface state mapping lost: %#v", evs[0].Attempt())
	}
	if evs[0].SourceEventKey != "secure-attempt-outcome:sess-1:bleg-1" {
		t.Fatalf("source key = %q", evs[0].SourceEventKey)
	}
}

func TestSecureSession_AddUsageDropsRawJSONAndRecords(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{}
	dec := newSecureSessionDecorator(t, h, fake)

	delta := domain.UsageDelta{
		SessionID: "sess-1", TurnID: "turn-1", BLegID: "bleg-1",
		InputTokens: 100, OutputTokens: 50, TotalTokens: 150,
		CostNanoUnits: 1000, Currency: "USD", CostSource: "accounting",
		RawUsageJSON:     `{"secret":"should-not-leak"}`,
		ProxyCompletedAt: fixedTime,
	}
	if err := dec.AddUsage(context.Background(), delta); err != nil {
		t.Fatalf("AddUsage: %v", err)
	}
	evs := h.events()
	if len(evs) != 1 || evs[0].Usage() == nil {
		t.Fatalf("usage event not recorded: %#v", evs)
	}
	if evs[0].Usage().InputTokens != 100 || evs[0].Usage().TotalTokens != 150 {
		t.Fatalf("usage dimensions lost: %#v", evs[0].Usage())
	}
	for _, bad := range []string{"secret", `{"secret":`, "RawUsageJSON"} {
		if contains(string(mustMarshal(t, evs[0])), bad) {
			t.Fatalf("recorded secure-session usage must not carry raw usage JSON; found %q", bad)
		}
	}
	want := "secure-usage:sess-1:turn-1:bleg-1:" + fixedTime.Format(time.RFC3339Nano)
	if evs[0].SourceEventKey != want {
		t.Fatalf("source key = %q, want %q", evs[0].SourceEventKey, want)
	}
}

func TestSecureSession_AddUsageFallsBackToHashWhenTimeMissing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{}
	dec := newSecureSessionDecorator(t, h, fake)

	delta := domain.UsageDelta{SessionID: "sess-1", TurnID: "turn-1", BLegID: "bleg-1", InputTokens: 10}
	if err := dec.AddUsage(context.Background(), delta); err != nil {
		t.Fatalf("AddUsage: %v", err)
	}
	evs := h.events()
	if len(evs) != 1 {
		t.Fatalf("usage event not recorded")
	}
	if evs[0].SourceEventKey == "secure-usage:sess-1:turn-1:bleg-1:" {
		t.Fatalf("source key must include a hash/time suffix, got %q", evs[0].SourceEventKey)
	}
	// Deterministic: same delta produces the same key, so the store dedupes the
	// second projection (one event remains, not two).
	if err := dec.AddUsage(context.Background(), delta); err != nil {
		t.Fatalf("AddUsage 2: %v", err)
	}
	if len(h.events()) != 1 {
		t.Fatalf("deterministic source key must dedupe repeated projection, got %d events", len(h.events()))
	}
	for _, bad := range []string{"secret", "raw"} {
		if contains(string(mustMarshal(t, evs[0])), bad) {
			t.Fatalf("hash key must not leak raw content: %q", bad)
		}
	}
}

func TestSecureSession_AppendAuditRecordsAuditEvent(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{}
	dec := newSecureSessionDecorator(t, h, fake)

	item := domain.AuditItem{SessionID: "sess-1", TurnID: "turn-1", Seq: 7, Action: "transcript.view", Result: "ok", CreatedAt: fixedTime}
	if err := dec.AppendAudit(context.Background(), item); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	evs := h.events()
	if len(evs) != 1 || evs[0].Audit() == nil || evs[0].Audit().Action != "transcript.view" {
		t.Fatalf("audit event not recorded: %#v", evs)
	}
	if evs[0].SourceEventKey != "secure-audit:sess-1:turn-1:transcript.view:7" {
		t.Fatalf("source key = %q", evs[0].SourceEventKey)
	}
}

func TestSecureSession_BestEffortRecordingFailurePreservesDelegateOutcome(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	rec := newFailingRecorder(t, h.status, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{}
	dec := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   fake,
		Normalizer: h.normal,
		Recorder:   rec,
	})

	if err := dec.TouchActivity(context.Background(), "sess-1", fixedTime, domain.ActivitySystem); err != nil {
		t.Fatalf("best-effort recording failure must not surface: %v", err)
	}
	if err := dec.AddUsage(context.Background(), domain.UsageDelta{SessionID: "sess-1", BLegID: "bleg-1", ProxyCompletedAt: fixedTime}); err != nil {
		t.Fatalf("best-effort recording failure must not surface: %v", err)
	}
	if err := dec.UpdateAttemptOutcome(context.Background(), domain.AttemptOutcome{SessionID: "sess-1", BLegID: "bleg-1", EndedAt: fixedTime}); err != nil {
		t.Fatalf("best-effort recording failure must not surface: %v", err)
	}
	if got := h.status.Snapshot().State; got != cp.CapabilityDegraded {
		t.Fatalf("recording failure must degrade status, got %q", got)
	}
}

func TestSecureSession_CreateRequiredPreWorkFailClosedReturnsError(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingRequiredPreWork, []cp.Category{cp.CategorySession})
	rec := newFailingRecorder(t, h.status, cp.RecordingRequiredPreWork, []cp.Category{cp.CategorySession})
	fake := &fakeSecureSessionStore{}
	dec := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   fake,
		Normalizer: h.normal,
		Recorder:   rec,
	})
	if _, err := dec.Create(context.Background(), sampleCreateRecord()); err == nil {
		t.Fatalf("required pre-work Create must fail closed on recording failure")
	} else if !errors.Is(err, controlplane.ErrDegraded) && !errors.Is(err, controlplane.ErrUnavailable) {
		t.Fatalf("expected degraded/unavailable classification, got %v", err)
	}
	// Delegate already succeeded before recording (authoritative id known).
	if fake.createCalls != 1 {
		t.Fatalf("delegate Create must still be called once, got %d", fake.createCalls)
	}
}

func TestSecureSession_AppendAuditRequiredPreWorkFailClosedReturnsError(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingRequiredPreWork, []cp.Category{cp.CategoryAudit})
	rec := newFailingRecorder(t, h.status, cp.RecordingRequiredPreWork, []cp.Category{cp.CategoryAudit})
	fake := &fakeSecureSessionStore{}
	dec := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   fake,
		Normalizer: h.normal,
		Recorder:   rec,
	})
	err := dec.AppendAudit(context.Background(), domain.AuditItem{SessionID: "sess-1", TurnID: "turn-1", Seq: 1, Action: "a", CreatedAt: fixedTime})
	if err == nil {
		t.Fatalf("required pre-work AppendAudit must fail closed on recording failure")
	}
	if fake.appendAuditCalls != 1 {
		t.Fatalf("delegate AppendAudit must be called once, got %d", fake.appendAuditCalls)
	}
}

func TestSecureSession_DisabledRecorderPreservesAllDelegateSemantics(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{}
	dec := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   fake,
		Normalizer: h.normal,
		Recorder:   h.disabledRecorder(),
	})
	if _, err := dec.Create(context.Background(), sampleCreateRecord()); err != nil {
		t.Fatalf("Create with disabled recorder must not surface error: %v", err)
	}
	if err := dec.AppendAudit(context.Background(), domain.AuditItem{SessionID: "sess-1", TurnID: "turn-1", Seq: 1, Action: "a", CreatedAt: fixedTime}); err != nil {
		t.Fatalf("AppendAudit with disabled recorder must not surface error: %v", err)
	}
	if len(h.events()) != 0 {
		t.Fatalf("disabled recorder must not record, got %d", len(h.events()))
	}
}

func TestSecureSession_BestEffortPathsIgnoreDisabledRecorder(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{}
	dec := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   fake,
		Normalizer: h.normal,
		Recorder:   h.disabledRecorder(),
	})
	if err := dec.TouchActivity(context.Background(), "sess-1", fixedTime, domain.ActivityClientRequest); err != nil {
		t.Fatalf("TouchActivity with disabled recorder must not surface: %v", err)
	}
	if err := dec.AppendAttemptTrace(context.Background(), domain.AttemptTrace{SessionID: "sess-1", BLegID: "bleg-1", AttemptSeq: 1, StartedAt: fixedTime}); err != nil {
		t.Fatalf("AppendAttemptTrace with disabled recorder must not surface: %v", err)
	}
	if err := dec.UpdateAttemptOutcome(context.Background(), domain.AttemptOutcome{SessionID: "sess-1", BLegID: "bleg-1", EndedAt: fixedTime}); err != nil {
		t.Fatalf("UpdateAttemptOutcome with disabled recorder must not surface: %v", err)
	}
	if err := dec.AddUsage(context.Background(), domain.UsageDelta{SessionID: "sess-1", BLegID: "bleg-1", ProxyCompletedAt: fixedTime}); err != nil {
		t.Fatalf("AddUsage with disabled recorder must not surface: %v", err)
	}
	if fake.touchCalls != 1 || fake.appendAttemptTraceCalls != 1 || fake.updateAttemptOutcomeCalls != 1 || fake.addUsageCalls != 1 {
		t.Fatalf("delegate must still be called when recorder disabled: touch=%d trace=%d outcome=%d usage=%d",
			fake.touchCalls, fake.appendAttemptTraceCalls, fake.updateAttemptOutcomeCalls, fake.addUsageCalls)
	}
	if len(h.events()) != 0 {
		t.Fatalf("disabled recorder must not record best-effort events, got %d", len(h.events()))
	}
}

func TestSecureSession_ReadMethodsArePassThroughAndNeverRecord(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeSecureSessionStore{}
	dec := newSecureSessionDecorator(t, h, fake)

	if _, err := dec.LoadByID(context.Background(), "sess-1"); err != nil {
		t.Fatalf("LoadByID: %v", err)
	}
	if _, err := dec.LoadByALegID(context.Background(), "aleg-1"); err != nil {
		t.Fatalf("LoadByALegID: %v", err)
	}
	if _, err := dec.Summary(context.Background(), domain.SummaryQuery{}); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if _, err := dec.Audit(context.Background(), "sess-1", domain.ReadOptions{}); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if _, err := dec.Transcript(context.Background(), "sess-1", domain.ReadOptions{}); err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if _, err := dec.ListAttemptEvidence(context.Background(), "sess-1", domain.ReadOptions{}); err != nil {
		t.Fatalf("ListAttemptEvidence: %v", err)
	}
	if _, err := dec.NextAuditSeq(context.Background(), "sess-1"); err != nil {
		t.Fatalf("NextAuditSeq: %v", err)
	}
	if _, err := dec.NextTranscriptSeq(context.Background(), "sess-1"); err != nil {
		t.Fatalf("NextTranscriptSeq: %v", err)
	}
	if err := dec.AppendTranscript(context.Background(), domain.TranscriptItem{SessionID: "sess-1"}); err != nil {
		t.Fatalf("AppendTranscript: %v", err)
	}
	if err := dec.CheckReadiness(context.Background(), domain.PolicyMetadata{}); err != nil {
		t.Fatalf("CheckReadiness: %v", err)
	}
	if len(h.events()) != 0 {
		t.Fatalf("read methods must never record, got %d", len(h.events()))
	}
}
