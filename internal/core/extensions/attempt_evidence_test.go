package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// TestNewAttemptEvidenceFunc_FailureEmitsErrorEvidence asserts the attempt
// evidence func projects a failed attempt into an error/none record on the
// attempt-lifecycle stage with BackendAttempted=true. It fails if the func does
// not emit per-attempt failure evidence.
func TestNewAttemptEvidenceFunc_FailureEmitsErrorEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	fn := extensions.NewAttemptEvidenceFunc(extensions.DecisionEvidenceFromContext(ctx))

	fn(ctx, "openai", errors.New("upstream boom"))
	rec, ok := obs.findByProvider("openai")
	if !ok {
		t.Fatalf("expected openai attempt-failure record, got %+v", obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeError || rec.Effect != policydecision.EffectNone {
		t.Fatalf("attempt failure: want error/none, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.Stage != feature.StageIDAttemptLifecycle {
		t.Fatalf("attempt stage: got %q want %q", rec.Stage, feature.StageIDAttemptLifecycle)
	}
	if rec.ReasonCode != extensions.ReasonAttemptFailure {
		t.Fatalf("attempt reason: got %q want %q", rec.ReasonCode, extensions.ReasonAttemptFailure)
	}
	if !rec.BackendAttempted {
		t.Fatalf("attempt observation must record backend attempted")
	}
	if err := extensions.ValidateDecisionRecord(rec); err != nil {
		t.Fatalf("attempt record not legal: %v", err)
	}
}

// TestNewAttemptEvidenceFunc_SuccessEmitsNothing asserts a successful attempt
// (nil err) emits no record, matching ProjectAttemptObservation's ok=false skip.
func TestNewAttemptEvidenceFunc_SuccessEmitsNothing(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	fn := extensions.NewAttemptEvidenceFunc(extensions.DecisionEvidenceFromContext(ctx))
	fn(ctx, "openai", nil)
	if len(obs.snapshot()) != 0 {
		t.Fatalf("successful attempt must emit nothing, got %+v", obs.snapshot())
	}
}

// TestNewAttemptEvidenceFunc_NilEmitterEmitsNothing asserts a nil emitter seam
// yields a func that emits nothing (non-interference default).
func TestNewAttemptEvidenceFunc_NilEmitterEmitsNothing(t *testing.T) {
	t.Parallel()
	fn := extensions.NewAttemptEvidenceFunc(&extensions.DecisionEvidence{Emitter: nil, Views: sampleViews()})
	// A nil emitter seam must not panic and emits nothing by default.
	fn(context.Background(), "openai", errors.New("boom"))
}

// TestWithAttemptEvidence_RoundTrip asserts the context seam attaches and
// retrieves the func, and returns nil when absent.
func TestWithAttemptEvidence_RoundTrip(t *testing.T) {
	t.Parallel()
	if got := extensions.AttemptEvidenceFromContext(context.Background()); got != nil {
		t.Fatalf("absent seam: want nil, got %v", got)
	}
	fn := extensions.AttemptEvidenceFunc(func(_ context.Context, _ string, _ error) {})
	ctx := extensions.WithAttemptEvidence(context.Background(), fn)
	if got := extensions.AttemptEvidenceFromContext(ctx); got == nil {
		t.Fatal("attached seam: want non-nil func")
	}
	if extensions.WithAttemptEvidence(context.Background(), nil) != context.Background() {
		t.Fatal("nil func must leave ctx unchanged")
	}
}
