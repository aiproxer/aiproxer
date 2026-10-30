package hooks_test

import (
	"context"
	"errors"
	"testing"

	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// submitEvidenceCall captures one invocation of the submit evidence seam.
type submitEvidenceCall struct {
	provider    string
	rejected    bool
	annotations map[string]string
	err         error
}

// recorderSubmitEvidence returns a SubmitEvidenceFunc that records each call.
func recorderSubmitEvidence(seen *[]submitEvidenceCall) corehooks.SubmitEvidenceFunc {
	return func(_ context.Context, providerID string, rejected bool, annotations map[string]string, err error) {
		*seen = append(*seen, submitEvidenceCall{
			provider:    providerID,
			rejected:    rejected,
			annotations: annotations,
			err:         err,
		})
	}
}

// TestRunSubmit_EmitsPerHookEvidence asserts the runner invokes the context-carried
// evidence func once per hook with the hook's provider id, reject flag, added
// annotations, and returned error. It fails if per-hook projection is missing.
func TestRunSubmit_EmitsPerHookEvidence(t *testing.T) {
	t.Parallel()
	var seen []submitEvidenceCall
	fn := recorderSubmitEvidence(&seen)
	ctx := corehooks.WithSubmitEvidence(context.Background(), fn)
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{
		&stubSubmit{id: "sub-annotate", order: 1, handle: func(_ context.Context, _ *aipapi.Call, meta *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			meta.Annotations["team"] = "platform"
			return sdk.SubmitDecision{}, nil
		}},
		&stubSubmit{id: "sub-reject", order: 2, handle: func(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			return sdk.SubmitDecision{Reject: true, Reason: "nope"}, nil
		}},
	}})
	err := bus.RunSubmit(ctx, testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}})
	if !sdk.IsSubmitReject(err) {
		t.Fatalf("expected submit reject preserved, got %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected one evidence call per hook (2), got %d: %+v", len(seen), seen)
	}
	if seen[0].provider != "sub-annotate" || seen[0].rejected || seen[0].err != nil {
		t.Fatalf("sub-annotate evidence: %+v", seen[0])
	}
	if seen[0].annotations["team"] != "platform" {
		t.Fatalf("sub-annotate annotations not projected: %+v", seen[0].annotations)
	}
	if seen[1].provider != "sub-reject" || !seen[1].rejected || seen[1].err != nil {
		t.Fatalf("sub-reject evidence: %+v", seen[1])
	}
}

// TestRunSubmit_EmitsFailureEvidenceOnFailOpen asserts a failing fail-open hook
// still emits failure evidence (err carried) and the chain continues, mirroring
// the tool reactor evidence behavior. The second hook is a no-op and produces no
// evidence (no representable semantics), proving fail-open continuation.
func TestRunSubmit_EmitsFailureEvidenceOnFailOpen(t *testing.T) {
	t.Parallel()
	var ranSecond bool
	var seen []submitEvidenceCall
	fn := recorderSubmitEvidence(&seen)
	ctx := corehooks.WithSubmitEvidence(context.Background(), fn)
	boom := errors.New("boom")
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{
		&stubSubmit{id: "sub-fail-open", order: 1, explicitMode: true, mode: sdk.FailOpen, handle: func(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			return sdk.SubmitDecision{}, boom
		}},
		&stubSubmit{id: "sub-after", order: 2, fn: func() { ranSecond = true }},
	}})
	if err := bus.RunSubmit(ctx, testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}}); err != nil {
		t.Fatalf("fail-open must not surface error, got %v", err)
	}
	if !ranSecond {
		t.Fatal("expected second hook to run after fail-open error")
	}
	if len(seen) != 1 {
		t.Fatalf("expected evidence only for the failing hook (1), got %d: %+v", len(seen), seen)
	}
	if seen[0].provider != "sub-fail-open" || seen[0].err == nil {
		t.Fatalf("sub-fail-open failure evidence: %+v", seen[0])
	}
}

// TestRunSubmit_EmitsFailureEvidenceOnFailClosed asserts a fail-closed hook
// failure emits failure evidence before the runner returns the wrapped error.
func TestRunSubmit_EmitsFailureEvidenceOnFailClosed(t *testing.T) {
	t.Parallel()
	var seen []submitEvidenceCall
	fn := recorderSubmitEvidence(&seen)
	ctx := corehooks.WithSubmitEvidence(context.Background(), fn)
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{
		&stubSubmit{id: "sub-fail-closed", order: 1, explicitMode: true, mode: sdk.FailClosed, handle: func(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			return sdk.SubmitDecision{}, errors.New("fatal")
		}},
	}})
	err := bus.RunSubmit(ctx, testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}})
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if len(seen) != 1 {
		t.Fatalf("expected 1 evidence call, got %d: %+v", len(seen), seen)
	}
	if seen[0].provider != "sub-fail-closed" || seen[0].err == nil {
		t.Fatalf("sub-fail-closed failure evidence: %+v", seen[0])
	}
}

// TestRunSubmit_NoEvidenceWithoutFunc asserts the runner does not panic and emits
// nothing when no evidence func is attached (non-interference default).
func TestRunSubmit_NoEvidenceWithoutFunc(t *testing.T) {
	t.Parallel()
	var seen []submitEvidenceCall
	// Intentionally no WithSubmitEvidence on ctx.
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{
		&stubSubmit{id: "sub-noop", order: 1, fn: func() {}},
	}})
	if err := bus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}}); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("expected no evidence without func, got %+v", seen)
	}
}

// TestRunSubmit_NoEvidenceForNoOpHook asserts a no-op hook (no reject, no error,
// no added annotations) produces no evidence call, matching ProjectSubmitOutcome's
// ok=false skip semantics.
func TestRunSubmit_NoEvidenceForNoOpHook(t *testing.T) {
	t.Parallel()
	var seen []submitEvidenceCall
	fn := recorderSubmitEvidence(&seen)
	ctx := corehooks.WithSubmitEvidence(context.Background(), fn)
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{
		&stubSubmit{id: "sub-noop", order: 1, fn: func() {}},
	}})
	if err := bus.RunSubmit(ctx, testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}}); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("no-op hook must not produce evidence, got %+v", seen)
	}
}

// TestRunSubmit_AnnotationsDiffExcludesDeletions asserts the evidence diff for a
// submit hook captures only annotations it added or changed relative to the before
// snapshot, never deletions of keys removed by the hook. Hook A adds two
// annotations; hook B deletes one and changes/adds another. Hook B's evidence must
// contain only the changed/added key, proving deletions are not projected (they
// have no value to represent) and the runner does not panic on a shrinking map.
func TestRunSubmit_AnnotationsDiffExcludesDeletions(t *testing.T) {
	t.Parallel()
	var seen []submitEvidenceCall
	fn := recorderSubmitEvidence(&seen)
	ctx := corehooks.WithSubmitEvidence(context.Background(), fn)
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{
		&stubSubmit{id: "hook-a", order: 1, handle: func(_ context.Context, _ *aipapi.Call, meta *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			meta.Annotations["keep"] = "from-a"
			meta.Annotations["drop"] = "from-a"
			return sdk.SubmitDecision{}, nil
		}},
		&stubSubmit{id: "hook-b", order: 2, handle: func(_ context.Context, _ *aipapi.Call, meta *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			delete(meta.Annotations, "drop")
			meta.Annotations["keep"] = "from-b"
			meta.Annotations["added"] = "from-b"
			return sdk.SubmitDecision{}, nil
		}},
	}})
	meta := &sdk.SubmitMeta{Annotations: map[string]string{}}
	if err := bus.RunSubmit(ctx, testCall(), meta); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected one evidence call per hook (2), got %d: %+v", len(seen), seen)
	}
	if seen[0].provider != "hook-a" {
		t.Fatalf("hook-a evidence: %+v", seen[0])
	}
	if got := seen[0].annotations["keep"]; got != "from-a" {
		t.Fatalf("hook-a keep annotation: got %q want from-a", got)
	}
	if got := seen[0].annotations["drop"]; got != "from-a" {
		t.Fatalf("hook-a drop annotation: got %q want from-a", got)
	}
	if seen[1].provider != "hook-b" {
		t.Fatalf("hook-b evidence: %+v", seen[1])
	}
	bAnn := seen[1].annotations
	if _, ok := bAnn["drop"]; ok {
		t.Fatalf("hook-b evidence must not include deleted key drop, got %+v", bAnn)
	}
	if got := bAnn["keep"]; got != "from-b" {
		t.Fatalf("hook-b changed key keep: got %q want from-b", got)
	}
	if got := bAnn["added"]; got != "from-b" {
		t.Fatalf("hook-b added key added: got %q want from-b", got)
	}
	if len(bAnn) != 2 {
		t.Fatalf("hook-b evidence must contain only changed/added keys, got %+v", bAnn)
	}
}
