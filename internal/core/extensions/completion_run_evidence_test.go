package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

// malformedPassGate returns a pass-original outcome that carries an Err, which
// fails completion.Outcome.Validate (pass original must not set err). It is used
// to verify validation failures emit malformed evidence rather than being
// misclassified as a provider failure or a successful pass.
type malformedPassGate struct{}

func (malformedPassGate) ID() string                        { return "pd-malformed-pass" }
func (malformedPassGate) Order() int                        { return 0 }
func (malformedPassGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (malformedPassGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.Outcome{Kind: completion.OutcomePassOriginal, Err: errors.New("illegal err on pass")}, nil
}

// handlerFailGate returns a handler error (provider failure) with a zero outcome
// kind, used to verify provider failures still emit ReasonCompletionFailure.
type handlerFailGate struct{}

func (handlerFailGate) ID() string                        { return "pd-handler-fail" }
func (handlerFailGate) Order() int                        { return 0 }
func (handlerFailGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (handlerFailGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.Outcome{Kind: completion.OutcomeKind(0)}, errors.New("handler boom")
}

// postOutputReplaceGate returns a replace outcome; when output is already
// committed the runtime ignores the replacement and the projector records
// skip/none with the post-output reason.
type postOutputReplaceGate struct{}

func (postOutputReplaceGate) ID() string                        { return "pd-post-output-replace" }
func (postOutputReplaceGate) Order() int                        { return 0 }
func (postOutputReplaceGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (postOutputReplaceGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.ReplaceOutcome([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
}

func completionGateServices() completion.Services {
	return completion.Services{State: state.DisabledStore{}}
}

// TestApplyCompletionGateChain_PreOutputPassPreservesOutputCommittedFalse
// asserts that a pre-output pass gate emits a record with OutputCommitted=false.
// This fails if the runner forces OutputCommitted=true on every record.
func TestApplyCompletionGateChain_PreOutputPassPreservesOutputCommittedFalse(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	orig := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}, {Kind: aipapi.EventResponseFinished}}
	result, err := extensions.ApplyCompletionGateChain(ctx, []completion.Gate{runnerPassGate{}},
		completion.Meta{}, orig, false, completionGateServices(), nil)
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	if len(result.Events) != 2 {
		t.Fatalf("pre-output pass must preserve buffer: got %d events", len(result.Events))
	}
	rec, ok := obs.findByProvider("pd-pass-gate")
	if !ok {
		t.Fatalf("expected pd-pass-gate record, got %+v", obs.snapshot())
	}
	if rec.OutputCommitted {
		t.Fatalf("pre-output pass must record OutputCommitted=false, got true")
	}
	if rec.Outcome != policydecision.OutcomeAllow || rec.Effect != policydecision.EffectNone {
		t.Fatalf("pass: want allow/none, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.ReasonCode != extensions.ReasonCompletionPass {
		t.Fatalf("pass reason: got %q want %q", rec.ReasonCode, extensions.ReasonCompletionPass)
	}
}

// TestApplyCompletionGateChain_ValidationFailureEmitsMalformed asserts that an
// outcome failing Validate (pass-original with Err set) emits malformed evidence
// (ReasonCompletionMalformed), not a provider failure or a successful pass. This
// fails if the runner classifies validation failures by outcome kind/fields.
func TestApplyCompletionGateChain_ValidationFailureEmitsMalformed(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	orig := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}, {Kind: aipapi.EventResponseFinished}}
	// FailOpen: validation failure is logged and skipped; chain continues.
	result, err := extensions.ApplyCompletionGateChain(ctx, []completion.Gate{malformedPassGate{}},
		completion.Meta{}, orig, false, completionGateServices(), nil)
	if err != nil {
		t.Fatalf("fail-open validation failure must not surface error, got %v", err)
	}
	if len(result.Events) != 2 {
		t.Fatalf("fail-open validation failure must preserve buffer: got %d events", len(result.Events))
	}
	rec, ok := obs.findByProvider("pd-malformed-pass")
	if !ok {
		t.Fatalf("expected malformed record, got %+v", obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeError || rec.Effect != policydecision.EffectNone {
		t.Fatalf("malformed: want error/none, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.ReasonCode != extensions.ReasonCompletionMalformed {
		t.Fatalf("malformed reason: got %q want %q", rec.ReasonCode, extensions.ReasonCompletionMalformed)
	}
	if rec.ClientCategory != extensions.CategoryMalformed {
		t.Fatalf("malformed category: got %q want %q", rec.ClientCategory, extensions.CategoryMalformed)
	}
	if rec.OutputCommitted {
		t.Fatalf("pre-output malformed must record OutputCommitted=false, got true")
	}
	if rec.Stage != feature.StageIDCompletionGating {
		t.Fatalf("stage: got %q want %q", rec.Stage, feature.StageIDCompletionGating)
	}
	if err := extensions.ValidateDecisionRecord(rec); err != nil {
		t.Fatalf("malformed record not legal: %v", err)
	}
}

// TestApplyCompletionGateChain_HandlerErrorEmitsFailure asserts a handler-returned
// error (provider failure) emits ReasonCompletionFailure, not malformed.
func TestApplyCompletionGateChain_HandlerErrorEmitsFailure(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	orig := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}, {Kind: aipapi.EventResponseFinished}}
	result, err := extensions.ApplyCompletionGateChain(ctx, []completion.Gate{handlerFailGate{}},
		completion.Meta{}, orig, false, completionGateServices(), nil)
	if err != nil {
		t.Fatalf("fail-open handler error must not surface error, got %v", err)
	}
	if len(result.Events) != 2 {
		t.Fatalf("fail-open handler error must preserve buffer: got %d events", len(result.Events))
	}
	rec, ok := obs.findByProvider("pd-handler-fail")
	if !ok {
		t.Fatalf("expected failure record, got %+v", obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeError || rec.Effect != policydecision.EffectNone {
		t.Fatalf("failure: want error/none, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.ReasonCode != extensions.ReasonCompletionFailure {
		t.Fatalf("failure reason: got %q want %q", rec.ReasonCode, extensions.ReasonCompletionFailure)
	}
	if rec.ClientCategory != extensions.CategoryFailure {
		t.Fatalf("failure category: got %q want %q", rec.ClientCategory, extensions.CategoryFailure)
	}
}

// TestApplyCompletionGateChain_PanicEvidenceRecordsFailClosed asserts the panic
// special case mirrors the runtime's fail-closed treatment in emitted evidence.
func TestApplyCompletionGateChain_PanicEvidenceRecordsFailClosed(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	orig := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}, {Kind: aipapi.EventResponseFinished}}
	_, err := extensions.ApplyCompletionGateChain(ctx, []completion.Gate{panicGate{}},
		completion.Meta{}, orig, false, completionGateServices(), nil)
	if err == nil {
		t.Fatal("expected panic to surface as fail-closed policy error")
	}
	rec, ok := obs.findByProvider("pd-panic-gate")
	if !ok {
		t.Fatalf("expected panic record, got %+v", obs.snapshot())
	}
	if rec.FailureBehavior != policydecision.FailureBehaviorFailClosed {
		t.Fatalf("panic failure behavior: got %q want fail-closed", rec.FailureBehavior)
	}
	if rec.ReasonCode != extensions.ReasonCompletionFailure {
		t.Fatalf("panic reason: got %q want %q", rec.ReasonCode, extensions.ReasonCompletionFailure)
	}
}

// TestApplyCompletionGateChain_PostOutputReplaceProjectsSkipNone asserts that a
// replace after outputCommitted=true still projects as skip/none with the
// post-output reason, and records OutputCommitted=true.
func TestApplyCompletionGateChain_PostOutputReplaceProjectsSkipNone(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	orig := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}, {Kind: aipapi.EventResponseFinished}}
	result, err := extensions.ApplyCompletionGateChain(ctx, []completion.Gate{postOutputReplaceGate{}},
		completion.Meta{}, orig, true, completionGateServices(), nil)
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	// outputCommitted=true: replacement ignored, original buffer preserved.
	if len(result.Events) != 2 {
		t.Fatalf("post-output replace must preserve buffer: got %d events", len(result.Events))
	}
	rec, ok := obs.findByProvider("pd-post-output-replace")
	if !ok {
		t.Fatalf("expected post-output replace record, got %+v", obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeSkip || rec.Effect != policydecision.EffectNone {
		t.Fatalf("post-output replace: want skip/none, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.ReasonCode != extensions.ReasonCompletionIgnored {
		t.Fatalf("post-output reason: got %q want %q", rec.ReasonCode, extensions.ReasonCompletionIgnored)
	}
	if !rec.OutputCommitted {
		t.Fatalf("post-output replace must record OutputCommitted=true, got false")
	}
	if err := extensions.ValidateDecisionRecord(rec); err != nil {
		t.Fatalf("post-output record not legal: %v", err)
	}
}
