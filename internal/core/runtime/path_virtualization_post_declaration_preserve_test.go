package runtime_test

// Spec: b-leg-path-virtualization post-certification review, defect 1 (P1) —
// the post-declaration replay residual, proved over the REAL composition.
// Requirements 4.1, 4.3, 4.4, 4.5, 4.6, 8.3.
//
// WHY THIS FILE EXISTS ALONGSIDE THE ASSEMBLER-LEVEL REGRESSION
//
// tool_call_post_declaration_preserve_test.go drives the chokepoint directly with
// a stand-in declaring pass. This file drives the REAL shipped expansion
// finalizer, the REAL outbound passes, and the REAL tool policy and tool reactor
// planes, over a model-emitted document whose selected path member carries the
// frozen reserved namespace.
//
// That distinction is the whole point of the defect. The residual was never
// reachable through a finalizer sorting BELOW the declaring pass - that direction
// is the assembler-level chokepoint, already proven. It was reachable through a
// finalizer sorting ABOVE it, and the only way to observe that at production
// fidelity is a run whose declaring participant is the shipped one and whose later
// participant is an ordinary finalizer an operator could genuinely register.
//
// WHAT IT ASSERTS
//
//   - selected paths contain no unresolved alias; unselected fields remain intact;
//   - the preserved call reaches tool policies, reactors and the client before
//     the later error, rather than being discarded by terminal cleanup;
//   - the declaring pass really ran and really published the expansion, so there
//     was a mandatory-safe result to preserve and the case is not vacuous;
//   - the turn still fails, and with the LATER finalizer's own error rather than
//     with the assembler's own typed refusal.
//
// A POSITIVE CONTROL runs the identical fixture with no later failing finalizer and
// requires one clean expanded tool call, so preservation is distinguishable
// from a stream that was never scanned. A PRE-DECLARATION CONTROL runs the same
// failing finalizer BELOW the shipped order and requires the assembler's own
// bounded incomplete-requirement refusal, so the two halves of the decision cannot
// be confused with one another.
//
// IT IS NOT AN ORDERING RULE. Nothing here changes a finalizer's declared order or
// the assembler's iteration; the later participant is placed above the declaring one
// only so the residual's precondition holds. A core ordering rule remains rejected.
//
// CONTENT FREEDOM: no failure message in this file contains a path, an alias, a
// workspace tag, an argument byte, or a tool-call identifier. Messages carry
// requirement numbers, byte counts, occurrence counts, bounded reason labels, and
// typed-error classification results only.

import (
	"bytes"
	"context"
	"errors"
	"testing"

	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// containsReservedMarker is the literal-byte leak oracle applied to one value the
// declaring pass was shown. It is deliberately the same marker literal the stream
// scanner uses and NOT the production recognizer, so a value the recognizer would
// fail to see still trips the fixture guard.
func containsReservedMarker(seen []byte) bool {
	return bytes.Contains(seen, []byte(expReservedMarker))
}

// postLaterFailingOrder is the ABSOLUTE order of the unrelated optional finalizer
// used by the post-declaration case.
//
// It is an absolute constant so the ordering property under test is genuinely
// load-bearing: the case reaches the residual only because the shipped expansion
// order sits BELOW it. Lowering the shipped order above it would silently convert
// this case into the pre-declaration chokepoint case, which the control below
// covers separately and which asserts a different outcome - so the two can never be
// satisfied by the same behaviour.
const postLaterFailingOrder = 100

// postEarlierFailingOrder is the ABSOLUTE order of the same finalizer for the
// pre-declaration control, which must sort below the shipped expansion order so the
// requirement is still undecided when it fails.
const postEarlierFailingOrder = 1

// errPostLaterFinalizer is the later finalizer's own error: a compile-time literal
// with no path, alias, workspace tag, or argument content in it. Cases assert on its
// IDENTITY through errors.Is and never on rendered text.
var errPostLaterFinalizer = errors.New("post-declaration finalizer failure")

// postLaterFailingFinalizer is the unrelated OPTIONAL finalizer of both cases. It
// publishes no buffering requirement, never touches the document, and always fails.
type postLaterFailingFinalizer struct {
	order int
}

func (*postLaterFailingFinalizer) ID() string { return "post-declaration-failing-finalizer" }

func (f *postLaterFailingFinalizer) Order() int { return f.order }

func (f *postLaterFailingFinalizer) Finalize(
	context.Context,
	toolcall.CompletedCall,
	aipapi.ToolDef,
	[]aipapi.ToolDef,
	toolcall.Meta,
) (toolcall.Result, error) {
	return toolcall.Result{}, errPostLaterFinalizer
}

// postPreservedCase is the shared body of the post-declaration assertion over one
// run's whole client-facing event stream.
//
// It is deliberately about the STREAM rather than a return value, because the
// residual was invisible to every per-finalizer assertion: the shipped pass reported
// a correct expansion, and the leak happened afterwards.
func postPreservedCase(t *testing.T, label string, result expRunResult) {
	t.Helper()

	scan := expScanClientEvents(result.events, "", "")

	// The marker oracle must have read every event before any zero is trusted.
	if scan.unrenderedEvents != 0 {
		t.Fatalf("%s: fixture: the marker oracle must be able to read every client event: events=%d unrendered=%d",
			label, scan.events, scan.unrenderedEvents)
	}
	// Non-vacuity: the shipped pass really ran on the alias-bearing document and
	// really published the expansion, so there WAS a mandatory-safe result.
	if result.expCalls != 1 {
		t.Fatalf("%s: fixture: the declaring pass invocation count: got %d want 1", label, result.expCalls)
	}
	if result.expResult.Action != toolcall.ActionRewrite {
		t.Fatalf("%s: fixture: the declaring pass must publish the expansion for a mandatory-safe result to exist: action=%d",
			label, int(result.expResult.Action))
	}
	if !containsReservedMarker(result.expSeen) {
		t.Fatalf("%s: fixture: the declaring pass must have received an alias-bearing document: seen=%d bytes",
			label, len(result.expSeen))
	}

	// The unselected payload field deliberately quotes the namespace. It must
	// remain unchanged, just as in the positive control; only selected paths
	// must be free of unresolved aliases.
	// And the production recognizer must find no reserved alias in any selected path
	// field of any released argument document.
	if scan.reservedSelected != 0 {
		t.Fatalf("requirements.md 4.1/4.4 - %s: a selected path field still carried the reserved namespace: documents=%d selected_fields=%d reserved_selected=%d",
			label, scan.documents, scan.selectedFields, scan.reservedSelected)
	}
	// A later optional failure releases the already-safe lifecycle, then fails the
	// turn. A pre-declaration failure still releases nothing (separate control).
	if scan.toolEvents != 3 || scan.argEvents != 1 || scan.realSelected != 1 {
		t.Fatalf("requirements.md 4.1 - %s: safe lifecycle missing before later error: tool_events=%d argument_events=%d argument_bytes=%d",
			label, scan.toolEvents, scan.argEvents, scan.argBytes)
	}
	if result.released == "" || result.lifecycle != 3 {
		t.Fatalf("requirements.md 4.1 - %s: preserved arguments missing: released=%d bytes lifecycle_events=%d",
			label, len(result.released), result.lifecycle)
	}
	// requirements.md 4.3: expansion must reach existing tool policies as the REAL
	// path, even when an unrelated later finalizer failed.
	if result.policyCalls < 1 || result.reactorCalls < 1 {
		t.Fatalf("requirements.md 4.3 - %s: preserved call missed policy/reactor planes: policy_calls=%d reactor_calls=%d",
			label, result.policyCalls, result.reactorCalls)
	}

	// THE FAILURE STILL SURFACES, and it is the LATER finalizer's own error.
	if result.recvErr == nil {
		t.Fatalf("requirements.md 4.6 - %s: the later finalizer's failure must still fail the turn: events=%d",
			label, scan.events)
	}
	if !errors.Is(result.recvErr, errPostLaterFinalizer) {
		t.Fatalf("requirements.md 4.6 - %s: the surfaced failure must be the later finalizer's own error: got %T",
			label, result.recvErr)
	}
}

// TestStreamToolCall_APostDeclarationFailurePreservesTheMandatorySafeResult is the
// production-fidelity RED for defect 1.
func TestStreamToolCall_APostDeclarationFailurePreservesTheMandatorySafeResult(t *testing.T) {
	t.Parallel()

	resolvable := expFixture{root: hookRegAliasOf(t), suffix: expModelSuffix}

	// Both controls' ordering preconditions are load-bearing, so they are asserted
	// rather than assumed. Lowering the shipped expansion order below
	// postLaterFailingOrder would make the main case unreachable; raising it above
	// postEarlierFailingOrder would make the control unreachable.
	if expansion.FinalizerOrder >= postLaterFailingOrder {
		t.Fatalf("fixture: the shipped expansion order must sort BELOW the post-declaration failing finalizer: expansion=%d later=%d",
			expansion.FinalizerOrder, postLaterFailingOrder)
	}
	if expansion.FinalizerOrder <= postEarlierFailingOrder {
		t.Fatalf("fixture: the shipped expansion order must sort ABOVE the pre-declaration failing finalizer: expansion=%d earlier=%d",
			expansion.FinalizerOrder, postEarlierFailingOrder)
	}

	// POSITIVE CONTROL. The identical fixture with the resolvable alias and NO later
	// failing finalizer releases exactly one expanded tool call. Every zero asserted
	// in the case below is therefore a real negative and not an artifact of a stream
	// that was never scanned.
	//
	// Note that markerEvents is NOT zero here and must not be: the fixture's
	// payload-concept member deliberately spells the reserved namespace and
	// requirements.md 4.9 forbids expansion from touching it, so the released
	// document legitimately still carries the marker in a NON-selected field. That is
	// exactly the discrimination the negative below turns around.
	t.Run("positive_control_the_resolvable_alias_releases_one_clean_selected_field", func(t *testing.T) {
		t.Parallel()
		control := expRun(t, expScenario{
			label:             "post_declaration_positive_control",
			aliasRoot:         resolvable.root,
			expansionDecides:  true,
			observersExpected: true,
		})
		controlScan := expScanClientEvents(control.events, "", "")
		if control.recvErr != nil {
			t.Fatalf("fixture: the resolvable alias must not fail the turn: %T", control.recvErr)
		}
		if controlScan.unrenderedEvents != 0 {
			t.Fatalf("fixture: the marker oracle must be able to read every client event: events=%d unrendered=%d",
				controlScan.events, controlScan.unrenderedEvents)
		}
		if controlScan.documents != 1 || controlScan.selectedFields != 1 {
			t.Fatalf("fixture: the positive control must release exactly one readable selected path field: documents=%d selected_fields=%d",
				controlScan.documents, controlScan.selectedFields)
		}
		if controlScan.reservedSelected != 0 {
			t.Fatalf("fixture: the positive control must release no reserved namespace in a selected path field: reserved_selected=%d",
				controlScan.reservedSelected)
		}
		if controlScan.realSelected != 1 {
			t.Fatalf("requirements.md 4.1 - the positive control must release the authoritative real root in the selected path field: real_selected=%d",
				controlScan.realSelected)
		}
		if controlScan.toolEvents != 3 || controlScan.argEvents != 1 {
			t.Fatalf("fixture: the positive control must release the synthesized canonical lifecycle: tool_events=%d argument_events=%d",
				controlScan.toolEvents, controlScan.argEvents)
		}
		if control.policyCalls < 1 || control.reactorCalls < 1 {
			t.Fatalf("requirements.md 4.3 - the positive control must reach both step-12 observer planes: policy_calls=%d reactor_calls=%d",
				control.policyCalls, control.reactorCalls)
		}
	})

	// THE CASE. The shipped declaring pass runs first and publishes the expansion;
	// an unrelated optional finalizer above it then fails.
	t.Run("post_declaration_unrelated_failure", func(t *testing.T) {
		t.Parallel()
		result := expRun(t, expScenario{
			label:             "post_declaration_unrelated_failure",
			aliasRoot:         resolvable.root,
			expansionDecides:  true,
			extraFinalizers:   []toolcall.Finalizer{&postLaterFailingFinalizer{order: postLaterFailingOrder}},
			observersExpected: true,
		})
		postPreservedCase(t, "post_declaration_unrelated_failure", result)
	})

	// PRE-DECLARATION CONTROL. The same failing finalizer BELOW the shipped order
	// fails while the requirement is still undecided, so the assembler must refuse
	// closed with its own bounded reason and never reach the shipped pass. This is
	// what keeps the two halves of the decision from being one blanket rule.
	t.Run("pre_declaration_unrelated_failure_still_refuses_closed", func(t *testing.T) {
		t.Parallel()
		result := expRun(t, expScenario{
			label:             "pre_declaration_unrelated_failure",
			aliasRoot:         resolvable.root,
			expansionDecides:  true,
			extraFinalizers:   []toolcall.Finalizer{&postLaterFailingFinalizer{order: postEarlierFailingOrder}},
			observersExpected: false,
		})
		if result.recvErr == nil {
			t.Fatal("requirements.md 4.5/4.6 - an undecided requirement must fail the turn")
		}
		var refusal *coreruntime.MandatoryBufferingError
		if !errors.As(result.recvErr, &refusal) || refusal == nil {
			t.Fatalf("requirements.md 4.5/4.6 - want a typed MandatoryBufferingError, got %T", result.recvErr)
		}
		if refusal.Reason != coreruntime.ReasonMandatoryBufferingIncomplete {
			t.Fatalf("requirements.md 4.6 - the assembler's bounded reason: got %q want %q",
				refusal.Reason, coreruntime.ReasonMandatoryBufferingIncomplete)
		}
		if result.expCalls != 0 {
			t.Fatalf("fixture: the declaring pass must not have decided: invocations=%d", result.expCalls)
		}
		scan := expScanClientEvents(result.events, "", "")
		if scan.markerEvents != 0 || scan.reservedSelected != 0 {
			t.Fatalf("requirements.md 4.5 - the reserved namespace reached the client: events=%d marker_carrying_events=%d reserved_selected=%d",
				scan.events, scan.markerEvents, scan.reservedSelected)
		}
		if scan.toolEvents != 0 || scan.argEvents != 0 || scan.argBytes != 0 {
			t.Fatalf("requirements.md 4.5 - a refused call still released tool lifecycle or arguments: tool_events=%d argument_events=%d argument_bytes=%d",
				scan.toolEvents, scan.argEvents, scan.argBytes)
		}
		if result.policyCalls != 0 || result.reactorCalls != 0 {
			t.Fatalf("requirements.md 4.3/4.5 - a refused call must not reach the tool policy or reactor plane: policy_calls=%d reactor_calls=%d",
				result.policyCalls, result.reactorCalls)
		}
	})
}
