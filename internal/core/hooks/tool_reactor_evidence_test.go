package hooks_test

import (
	"context"
	"errors"
	"testing"

	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// TestApplyToolReactors_EmitsPerReactorEvidence asserts the runner invokes the
// context-carried evidence func once per reactor with the reactor's provider id
// and its decision. It fails if per-reactor projection is removed from the runner.
func TestApplyToolReactors_EmitsPerReactorEvidence(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	type evidence struct {
		provider      string
		decision      sdk.ToolDecision
		err           error
		validationErr error
	}
	var seen []evidence
	fn := corehooks.ToolReactorEvidenceFunc(func(_ context.Context, providerID string, dec sdk.ToolDecision, err error, validationErr error) {
		seen = append(seen, evidence{provider: providerID, decision: dec, err: err, validationErr: validationErr})
	})
	ctx := corehooks.WithToolReactorEvidence(context.Background(), fn)
	b := corehooks.New(corehooks.Config{
		ToolReactors: []sdk.ToolReactor{
			&stubTool{id: "r-pass", order: 1, fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
				return sdk.ToolPass, aipapi.ToolEvent{}, nil
			}},
			&stubTool{id: "r-rewrite", order: 2, fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
				return sdk.ToolRewrite, aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "y"}, nil
			}},
		},
	})
	out := b.ApplyToolReactors(ctx, te, sdk.ToolMeta{})
	if !out.Emit {
		t.Fatalf("expected emit, got %#v", out)
	}
	if len(seen) != 2 {
		t.Fatalf("expected one evidence call per reactor (2), got %d: %+v", len(seen), seen)
	}
	if seen[0].provider != "r-pass" || seen[0].decision != sdk.ToolPass || seen[0].err != nil || seen[0].validationErr != nil {
		t.Fatalf("r-pass evidence: %+v", seen[0])
	}
	if seen[1].provider != "r-rewrite" || seen[1].decision != sdk.ToolRewrite || seen[1].err != nil || seen[1].validationErr != nil {
		t.Fatalf("r-rewrite evidence: %+v", seen[1])
	}
}

// TestApplyToolReactors_EvidenceRecordsFailure asserts a failing reactor's
// evidence callback receives the reactor's provider id and its error.
func TestApplyToolReactors_EvidenceRecordsFailure(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	type evidence struct {
		provider string
		err      error
	}
	var seen []evidence
	fn := corehooks.ToolReactorEvidenceFunc(func(_ context.Context, providerID string, _ sdk.ToolDecision, err error, _ error) {
		seen = append(seen, evidence{provider: providerID, err: err})
	})
	ctx := corehooks.WithToolReactorEvidence(context.Background(), fn)
	b := corehooks.New(corehooks.Config{
		ToolReactors: []sdk.ToolReactor{
			&stubTool{id: "r-fail", order: 1, fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
				return sdk.ToolRewrite, aipapi.ToolEvent{}, errors.New("boom")
			}},
		},
	})
	_ = b.ApplyToolReactors(ctx, te, sdk.ToolMeta{})
	if len(seen) != 1 {
		t.Fatalf("expected 1 evidence call, got %d: %+v", len(seen), seen)
	}
	if seen[0].provider != "r-fail" || seen[0].err == nil {
		t.Fatalf("r-fail evidence: %+v", seen[0])
	}
}

// TestApplyToolReactors_InvalidRewriteEvidenceIsMalformed asserts that when a
// reactor returns ToolRewrite with structurally invalid output (empty tool call
// id), the runner rejects the mutation (fail-open continues with the original
// event) AND reports the validation error to the evidence callback rather than
// fabricating a successful allow/mutate. This fails if evidence is emitted
// before validation with the raw ToolRewrite decision.
func TestApplyToolReactors_InvalidRewriteEvidenceIsMalformed(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	type evidence struct {
		decision      sdk.ToolDecision
		err           error
		validationErr error
	}
	var seen []evidence
	fn := corehooks.ToolReactorEvidenceFunc(func(_ context.Context, _ string, dec sdk.ToolDecision, err error, validationErr error) {
		seen = append(seen, evidence{decision: dec, err: err, validationErr: validationErr})
	})
	ctx := corehooks.WithToolReactorEvidence(context.Background(), fn)
	b := corehooks.New(corehooks.Config{
		ToolReactors: []sdk.ToolReactor{
			&stubTool{id: "r-bad-rewrite", order: 1, fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
				// Missing ToolCallID: ValidateToolEventAfterPolicy rejects this.
				return sdk.ToolRewrite, aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ArgsDelta: "y"}, nil
			}},
		},
	})
	out := b.ApplyToolReactors(ctx, te, sdk.ToolMeta{})
	if !out.Emit {
		t.Fatalf("fail-open must keep emitting the original event, got %#v", out)
	}
	if out.Event.ToolCallID != "c1" {
		t.Fatalf("invalid rewrite must not replace the current event, got %#v", out.Event)
	}
	if len(seen) != 1 {
		t.Fatalf("expected 1 evidence call, got %d: %+v", len(seen), seen)
	}
	if seen[0].err != nil {
		t.Fatalf("invalid rewrite is not a provider failure, got err=%v", seen[0].err)
	}
	if seen[0].validationErr == nil {
		t.Fatalf("invalid rewrite must surface a validation error to evidence, got %+v", seen[0])
	}
	if seen[0].decision != sdk.ToolRewrite {
		t.Fatalf("decision passed to evidence must remain the reactor's ToolRewrite, got %v", seen[0].decision)
	}
}

// TestApplyToolReactors_InvalidReplaceFailClosedSurfacesError asserts that with
// fail-closed policy an invalid ToolReplace surfaces the validation error to the
// caller and to the evidence callback (as a malformed validation error, not a
// successful allow/replace).
func TestApplyToolReactors_InvalidReplaceFailClosedSurfacesError(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	var validationErr error
	fn := corehooks.ToolReactorEvidenceFunc(func(_ context.Context, _ string, _ sdk.ToolDecision, err error, vErr error) {
		if vErr != nil {
			validationErr = vErr
		}
	})
	ctx := corehooks.WithToolReactorEvidence(context.Background(), fn)
	b := corehooks.New(corehooks.Config{
		ToolReactorErrorPolicy: sdk.ToolReactorErrorsFailClosed,
		ToolReactors: []sdk.ToolReactor{
			&stubTool{id: "r-bad-replace", order: 1, fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
				return sdk.ToolReplace, aipapi.ToolEvent{Kind: aipapi.ToolEventFinished, ArgsDelta: "y"}, nil
			}},
		},
	})
	out := b.ApplyToolReactors(ctx, te, sdk.ToolMeta{})
	if out.Err == nil {
		t.Fatalf("fail-closed invalid replace must surface error, got %#v", out)
	}
	if validationErr == nil {
		t.Fatalf("evidence must receive the validation error, got %v", validationErr)
	}
}

// TestApplyToolReactors_SwallowEmitsSwallowEvidence asserts a ToolSwallow
// decision emits swallow evidence before the runner returns Emit=false.
func TestApplyToolReactors_SwallowEmitsSwallowEvidence(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	var seen sdk.ToolDecision
	fn := corehooks.ToolReactorEvidenceFunc(func(_ context.Context, _ string, dec sdk.ToolDecision, _ error, _ error) {
		seen = dec
	})
	ctx := corehooks.WithToolReactorEvidence(context.Background(), fn)
	b := corehooks.New(corehooks.Config{
		ToolReactors: []sdk.ToolReactor{
			&stubTool{id: "r-swallow", order: 1, fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
				return sdk.ToolSwallow, aipapi.ToolEvent{}, nil
			}},
		},
	})
	out := b.ApplyToolReactors(ctx, te, sdk.ToolMeta{})
	if out.Emit {
		t.Fatalf("swallow must return Emit=false, got %#v", out)
	}
	if seen != sdk.ToolSwallow {
		t.Fatalf("evidence decision: got %v want ToolSwallow", seen)
	}
}

// TestApplyToolReactors_NoEvidenceWithoutFunc asserts the runner does not panic
// and emits nothing when no evidence func is attached (non-interference default).
func TestApplyToolReactors_NoEvidenceWithoutFunc(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	b := corehooks.New(corehooks.Config{
		ToolReactors: []sdk.ToolReactor{
			&stubTool{id: "r-pass", order: 1, fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
				return sdk.ToolPass, aipapi.ToolEvent{}, nil
			}},
		},
	})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit {
		t.Fatalf("expected emit without evidence func, got %#v", out)
	}
}
