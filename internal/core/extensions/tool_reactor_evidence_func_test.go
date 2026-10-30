package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// TestNewToolReactorEvidenceFunc_InvalidRewriteEmitsMalformed asserts that an
// invalid rewrite/replace (runner validation failed) projects as
// OutcomeError/EffectNone with ReasonToolReactorMalformed, not as a successful
// allow/mutate or allow/replace. This fails if the evidence func ignores the
// validationErr argument and projects the raw ToolRewrite decision.
func TestNewToolReactorEvidenceFunc_InvalidRewriteEmitsMalformed(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	fn := extensions.NewToolReactorEvidenceFunc(extensions.DecisionEvidenceFromContext(ctx))
	fn(ctx, "r-bad-rewrite", sdkhooks.ToolRewrite, nil, errors.New("missing tool call id"))
	rec, ok := obs.findByProvider("r-bad-rewrite")
	if !ok {
		t.Fatalf("expected malformed record, got %+v", obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeError || rec.Effect != policydecision.EffectNone {
		t.Fatalf("invalid rewrite: want error/none, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.ReasonCode != extensions.ReasonToolReactorMalformed {
		t.Fatalf("invalid rewrite reason: got %q want %q", rec.ReasonCode, extensions.ReasonToolReactorMalformed)
	}
	if rec.ClientCategory != extensions.CategoryMalformed {
		t.Fatalf("invalid rewrite category: got %q want %q", rec.ClientCategory, extensions.CategoryMalformed)
	}
	if rec.Stage != feature.StageIDToolEventReaction {
		t.Fatalf("stage: got %q want %q", rec.Stage, feature.StageIDToolEventReaction)
	}
	if !rec.BackendAttempted {
		t.Fatalf("tool reactor must record backend attempted (stream stage)")
	}
	if err := extensions.ValidateDecisionRecord(rec); err != nil {
		t.Fatalf("malformed record not legal: %v", err)
	}
}

// TestNewToolReactorEvidenceFunc_ProviderFailureEmitsFailure asserts a reactor
// returned error projects as OutcomeError/EffectNone with ReasonToolReactorFailure.
func TestNewToolReactorEvidenceFunc_ProviderFailureEmitsFailure(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	fn := extensions.NewToolReactorEvidenceFunc(extensions.DecisionEvidenceFromContext(ctx))
	fn(ctx, "r-fail", sdkhooks.ToolRewrite, errors.New("boom"), nil)
	rec, ok := obs.findByProvider("r-fail")
	if !ok {
		t.Fatalf("expected failure record, got %+v", obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeError || rec.Effect != policydecision.EffectNone {
		t.Fatalf("provider failure: want error/none, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.ReasonCode != extensions.ReasonToolReactorFailure {
		t.Fatalf("failure reason: got %q want %q", rec.ReasonCode, extensions.ReasonToolReactorFailure)
	}
	if rec.ClientCategory != extensions.CategoryFailure {
		t.Fatalf("failure category: got %q want %q", rec.ClientCategory, extensions.CategoryFailure)
	}
}

// TestNewToolReactorEvidenceFunc_ValidRewriteEmitsAllowMutate asserts a valid
// rewrite (no error, no validation error) still projects as allow/mutate.
func TestNewToolReactorEvidenceFunc_ValidRewriteEmitsAllowMutate(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	fn := extensions.NewToolReactorEvidenceFunc(extensions.DecisionEvidenceFromContext(ctx))
	fn(ctx, "r-rewrite", sdkhooks.ToolRewrite, nil, nil)
	rec, ok := obs.findByProvider("r-rewrite")
	if !ok {
		t.Fatalf("expected rewrite record, got %+v", obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeAllow || rec.Effect != policydecision.EffectMutate {
		t.Fatalf("valid rewrite: want allow/mutate, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.ReasonCode != extensions.ReasonToolReactorRewrite {
		t.Fatalf("rewrite reason: got %q want %q", rec.ReasonCode, extensions.ReasonToolReactorRewrite)
	}
}
