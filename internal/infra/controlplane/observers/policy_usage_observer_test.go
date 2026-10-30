package observers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

func samplePolicyRecord() policydecision.Record {
	return policydecision.Record{
		TraceID:    "trace-p",
		ALegID:     "aleg-p",
		BLegID:     "bleg-p",
		AttemptSeq: 1,
		Stage:      "pre_backend",
		Provider:   policydecision.ProviderRef{ID: "opa", Stage: "pre_backend"},
		Outcome:    policydecision.OutcomeDeny,
		Effect:     policydecision.EffectSwallow,
		ReasonCode: "policy_violation",
		Visibility: policydecision.EvidenceDefault,
		Scope:      knownScope(),
	}
}

func sampleUsageEvent() usage.Event {
	return usage.Event{
		TraceID:       "trace-u",
		ALegID:        "aleg-u",
		BLegID:        "bleg-u",
		SessionID:     "sess-u",
		AttemptSeq:    1,
		BackendID:     "openai",
		FrontendID:    "openai-responses",
		Model:         "gpt-4o",
		Scope:         knownScope(),
		InputTokens:   100,
		OutputTokens:  50,
		TotalTokens:   150,
		CostNanoUnits: 1000,
		Currency:      "USD",
		CostSource:    "accounting",
		RawUsageJSON:  `{"secret":"should-not-leak"}`,
		RecordedAt:    fixedTime,
	}
}

func TestPolicyObserverAdapter_RecordsDecisionAndRemainsFailOpen(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	adapter := observers.NewPolicyObserverAdapter(observers.PolicyObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	if err := adapter.OnPolicyDecision(context.Background(), samplePolicyRecord()); err != nil {
		t.Fatalf("policy observer must be fail-open, got %v", err)
	}
	evs := h.events()
	if len(evs) != 1 || evs[0].Policy() == nil || evs[0].Policy().Outcome != "deny" {
		t.Fatalf("policy decision not recorded: %#v", evs)
	}
	// Outcome is preserved unchanged.
	if evs[0].Policy().Effect != "swallow" || evs[0].Policy().ReasonCode != "policy_violation" {
		t.Fatalf("policy effect/reason lost: %#v", evs[0].Policy())
	}
	if evs[0].SourceEventKey != "policy:trace-p:pre_backend:opa:aleg-p:bleg-p:1:policy_violation" {
		t.Fatalf("policy source key = %q", evs[0].SourceEventKey)
	}
}

func TestPolicyObserverAdapter_RecordingFailureNeverSurfacesAndDegrades(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	rec := newFailingRecorder(t, h.status, cp.RecordingBestEffort, nil)
	adapter := observers.NewPolicyObserverAdapter(observers.PolicyObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   rec,
	})
	if err := adapter.OnPolicyDecision(context.Background(), samplePolicyRecord()); err != nil {
		t.Fatalf("policy observer must be fail-open even on recording failure, got %v", err)
	}
	if got := h.status.Snapshot().State; got != cp.CapabilityDegraded {
		t.Fatalf("recording failure must degrade status, got %q", got)
	}
}

func TestPolicyObserverAdapter_DisabledRecorderIsNoOp(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	adapter := observers.NewPolicyObserverAdapter(observers.PolicyObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   h.disabledRecorder(),
	})
	if err := adapter.OnPolicyDecision(context.Background(), samplePolicyRecord()); err != nil {
		t.Fatalf("disabled recorder must not surface error: %v", err)
	}
	if len(h.events()) != 0 {
		t.Fatalf("disabled recorder must not record, got %d", len(h.events()))
	}
}

func TestPolicyObserverAdapter_PreservesChainWhenOtherObserverErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	adapter := observers.NewPolicyObserverAdapter(observers.PolicyObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	chain := policydecision.NewChainObserver(
		adapter,
		&errorPolicyObserver{err: errors.New("other observer failed")},
	)
	// ChainObserver ignores child errors (fail-open); control-plane adapter must
	// not change that.
	_ = chain.OnPolicyDecision(context.Background(), samplePolicyRecord())
	if len(h.events()) != 1 {
		t.Fatalf("control-plane adapter must record independently of other observer errors, got %d", len(h.events()))
	}
}

type errorPolicyObserver struct{ err error }

func (o *errorPolicyObserver) OnPolicyDecision(context.Context, policydecision.Record) error {
	return o.err
}

func TestUsageObserverAdapter_RecordsSafeUsageAndDropsRawJSON(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	adapter := observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	if err := adapter.OnUsage(context.Background(), sampleUsageEvent()); err != nil {
		t.Fatalf("usage observer must be fail-open, got %v", err)
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
			t.Fatalf("recorded usage must not carry raw usage JSON; found %q", bad)
		}
	}
	if evs[0].SourceEventKey != "usage:trace-u:sess-u:bleg-u:1:accounting" {
		t.Fatalf("usage source key = %q", evs[0].SourceEventKey)
	}
}

func TestUsageObserverAdapter_RecordingFailureNeverSurfacesAndDegrades(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	rec := newFailingRecorder(t, h.status, cp.RecordingBestEffort, nil)
	adapter := observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   rec,
	})
	if err := adapter.OnUsage(context.Background(), sampleUsageEvent()); err != nil {
		t.Fatalf("usage observer must be fail-open even on recording failure, got %v", err)
	}
	if got := h.status.Snapshot().State; got != cp.CapabilityDegraded {
		t.Fatalf("recording failure must degrade status, got %q", got)
	}
}

func TestUsageObserverAdapter_NeverIntroducesChainError(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	rec := newFailingRecorder(t, h.status, cp.RecordingBestEffort, nil)
	adapter := observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   rec,
	})
	// usage.ChainObserver returns the first child error; the control-plane
	// adapter must never be that child.
	chain := usage.ChainObservers(adapter)
	if err := chain.OnUsage(context.Background(), sampleUsageEvent()); err != nil {
		t.Fatalf("control-plane usage adapter must never introduce chain error, got %v", err)
	}
}
