package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// TestNewSubmitEvidenceFunc_RejectAnnotateFailure asserts the submit evidence
// func projects reject/annotate/failure outcomes into shared records through the
// emitter, and skips no-op outcomes. It fails if the func does not emit per-hook.
func TestNewSubmitEvidenceFunc_RejectAnnotateFailure(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	fn := extensions.NewSubmitEvidenceFunc(extensions.DecisionEvidenceFromContext(ctx))

	fn(ctx, "sub-reject", true, nil, nil)
	rej, ok := obs.findByProvider("sub-reject")
	if !ok {
		t.Fatalf("expected sub-reject record, got %+v", obs.snapshot())
	}
	if rej.Outcome != policydecision.OutcomeDeny || rej.Effect != policydecision.EffectNone {
		t.Fatalf("reject: want deny/none, got %s/%s", rej.Outcome, rej.Effect)
	}
	if rej.Stage != feature.StageIDSubmit {
		t.Fatalf("reject stage: got %q want %q", rej.Stage, feature.StageIDSubmit)
	}
	if rej.ReasonCode != extensions.ReasonSubmitRejected {
		t.Fatalf("reject reason: got %q want %q", rej.ReasonCode, extensions.ReasonSubmitRejected)
	}
	if rej.BackendAttempted {
		t.Fatalf("submit reject must record no backend attempt")
	}

	fn(ctx, "sub-annotate", false, map[string]string{"team": "platform"}, nil)
	ann, ok := obs.findByProvider("sub-annotate")
	if !ok {
		t.Fatalf("expected sub-annotate record, got %+v", obs.snapshot())
	}
	if ann.Outcome != policydecision.OutcomeAllow || ann.Effect != policydecision.EffectAnnotate {
		t.Fatalf("annotate: want allow/annotate, got %s/%s", ann.Outcome, ann.Effect)
	}
	if ann.Annotations["team"] != "platform" {
		t.Fatalf("annotate annotations not projected: %+v", ann.Annotations)
	}

	fn(ctx, "sub-fail", false, nil, errors.New("boom"))
	fail, ok := obs.findByProvider("sub-fail")
	if !ok {
		t.Fatalf("expected sub-fail record, got %+v", obs.snapshot())
	}
	if fail.Outcome != policydecision.OutcomeError || fail.Effect != policydecision.EffectNone {
		t.Fatalf("failure: want error/none, got %s/%s", fail.Outcome, fail.Effect)
	}
	if fail.ReasonCode != extensions.ReasonSubmitFailure {
		t.Fatalf("failure reason: got %q want %q", fail.ReasonCode, extensions.ReasonSubmitFailure)
	}
}

// TestNewSubmitEvidenceFunc_NoOpSkipEmitsNothing asserts a no-op submit outcome
// (no reject, no error, no annotations) emits no record.
func TestNewSubmitEvidenceFunc_NoOpSkipEmitsNothing(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	fn := extensions.NewSubmitEvidenceFunc(extensions.DecisionEvidenceFromContext(ctx))
	fn(ctx, "sub-noop", false, nil, nil)
	if len(obs.snapshot()) != 0 {
		t.Fatalf("no-op submit must emit nothing, got %+v", obs.snapshot())
	}
}

// TestNewSubmitEvidenceFunc_NilEmitterEmitsNothing asserts a nil emitter seam
// yields a func that emits nothing (non-interference default).
func TestNewSubmitEvidenceFunc_NilEmitterEmitsNothing(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	fn := extensions.NewSubmitEvidenceFunc(&extensions.DecisionEvidence{Emitter: nil, Views: sampleViews()})
	fn(context.Background(), "sub-reject", true, nil, nil)
	if len(obs.snapshot()) != 0 {
		t.Fatalf("nil emitter must emit nothing, got %+v", obs.snapshot())
	}
}
