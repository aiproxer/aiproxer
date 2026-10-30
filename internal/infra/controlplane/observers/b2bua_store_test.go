package observers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func sampleAttemptRecord(outcome aipapi.AttemptOutcome) aipapi.AttemptRecord {
	return aipapi.AttemptRecord{
		BLegID:         "bleg-1",
		ALegID:         "aleg-1",
		Seq:            2,
		BackendID:      "openai",
		EffectiveModel: "gpt-4o",
		StartedAt:      fixedTime.Add(-time.Minute),
		FinishedAt:     fixedTime,
		Outcome:        outcome,
		Reason:         "ok",
	}
}

func newB2BUADecorator(t *testing.T, h *harness, fake *fakeB2BUAStore) *observers.B2BUAStoreDecorator {
	t.Helper()
	return observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   fake,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
}

func TestB2BUA_RecordAttemptRecordsLineageAfterDelegate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeB2BUAStore{}
	dec := newB2BUADecorator(t, h, fake)

	if err := dec.RecordAttempt(context.Background(), sampleAttemptRecord(aipapi.AttemptSuccess)); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if fake.recordCalls != 1 {
		t.Fatalf("delegate RecordAttempt must be called once, got %d", fake.recordCalls)
	}
	evs := h.events()
	if len(evs) != 1 || evs[0].Attempt() == nil {
		t.Fatalf("attempt event not recorded: %#v", evs)
	}
	if evs[0].SourceEventKey != "b2bua-attempt:aleg-1:bleg-1:2" {
		t.Fatalf("source key = %q, want b2bua-attempt:aleg-1:bleg-1:2", evs[0].SourceEventKey)
	}
	if evs[0].Attempt().ALegID != "aleg-1" || evs[0].Attempt().BLegID != "bleg-1" || evs[0].Attempt().AttemptSeq != 2 {
		t.Fatalf("lineage lost: %#v", evs[0].Attempt())
	}
	if evs[0].Attempt().BackendID != "openai" || evs[0].Attempt().Model != "gpt-4o" {
		t.Fatalf("backend/model lost: %#v", evs[0].Attempt())
	}
}

func TestB2BUA_RecordAttemptMapsSurfacedVsSwallowed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		outcome   aipapi.AttemptOutcome
		surfaced  cp.AttemptSurfaced
		outResult cp.AttemptOutcome
	}{
		{"success", aipapi.AttemptSuccess, cp.AttemptSurfacedSurfaced, cp.AttemptOutcomeSucceeded},
		{"surfaced_failure", aipapi.AttemptSurfacedFailure, cp.AttemptSurfacedSurfaced, cp.AttemptOutcomeFailed},
		{"swallowed_failure", aipapi.AttemptSwallowedFailure, cp.AttemptSurfacedSwallowed, cp.AttemptOutcomeFailed},
		{"cancelled", aipapi.AttemptCancelled, cp.AttemptSurfacedSwallowed, cp.AttemptOutcomeCancelled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, cp.RecordingBestEffort, nil)
			fake := &fakeB2BUAStore{}
			dec := newB2BUADecorator(t, h, fake)
			if err := dec.RecordAttempt(context.Background(), sampleAttemptRecord(c.outcome)); err != nil {
				t.Fatalf("RecordAttempt: %v", err)
			}
			evs := h.events()
			if len(evs) != 1 || evs[0].Attempt().Surfaced != c.surfaced || evs[0].Attempt().Outcome != c.outResult {
				t.Fatalf("mapping %s = surfaced %q outcome %q, want %q %q",
					c.name, evs[0].Attempt().Surfaced, evs[0].Attempt().Outcome, c.surfaced, c.outResult)
			}
		})
	}
}

func TestB2BUA_RecordAttemptDelegateFailureSurfacesAndDoesNotRecord(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeB2BUAStore{recordAttemptErr: errors.New("b2bua down")}
	dec := newB2BUADecorator(t, h, fake)
	if err := dec.RecordAttempt(context.Background(), sampleAttemptRecord(aipapi.AttemptSuccess)); err == nil {
		t.Fatalf("delegate failure must surface")
	}
	if len(h.events()) != 0 {
		t.Fatalf("recording must not happen on delegate failure, got %d", len(h.events()))
	}
}

func TestB2BUA_RecordAttemptRecordingFailureNeverChangesRouting(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	rec := newFailingRecorder(t, h.status, cp.RecordingBestEffort, nil)
	fake := &fakeB2BUAStore{}
	dec := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   fake,
		Normalizer: h.normal,
		Recorder:   rec,
	})
	if err := dec.RecordAttempt(context.Background(), sampleAttemptRecord(aipapi.AttemptSuccess)); err != nil {
		t.Fatalf("best-effort recording failure must never change routing outcome: %v", err)
	}
	if fake.recordCalls != 1 {
		t.Fatalf("delegate must still be called on recording failure, got %d", fake.recordCalls)
	}
	if got := h.status.Snapshot().State; got != cp.CapabilityDegraded {
		t.Fatalf("recording failure must degrade status, got %q", got)
	}
}

func TestB2BUA_DisabledRecorderPreservesContinuitySemantics(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeB2BUAStore{}
	dec := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   fake,
		Normalizer: h.normal,
		Recorder:   h.disabledRecorder(),
	})
	if err := dec.RecordAttempt(context.Background(), sampleAttemptRecord(aipapi.AttemptSuccess)); err != nil {
		t.Fatalf("disabled recorder must not surface error: %v", err)
	}
	if fake.recordCalls != 1 {
		t.Fatalf("delegate must still be called when recording disabled, got %d", fake.recordCalls)
	}
	if len(h.events()) != 0 {
		t.Fatalf("disabled recorder must not record, got %d", len(h.events()))
	}
}

func TestB2BUA_AllocationAndReadMethodsArePassThroughAndNeverRecord(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	fake := &fakeB2BUAStore{}
	dec := newB2BUADecorator(t, h, fake)

	if _, err := dec.CreateALeg(context.Background(), "key-1"); err != nil {
		t.Fatalf("CreateALeg: %v", err)
	}
	if _, err := dec.NextBLeg(context.Background(), "aleg-1"); err != nil {
		t.Fatalf("NextBLeg: %v", err)
	}
	if err := dec.SetWeightedFirstConsumed(context.Background(), "aleg-1", true); err != nil {
		t.Fatalf("SetWeightedFirstConsumed: %v", err)
	}
	if _, err := dec.LoadAttempts(context.Background(), "aleg-1"); err != nil {
		t.Fatalf("LoadAttempts: %v", err)
	}
	if len(h.events()) != 0 {
		t.Fatalf("allocation/read methods must never record, got %d", len(h.events()))
	}
}
