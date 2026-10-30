package runtime

// Spec: b-leg-path-virtualization post-certification review, defect 1 (P1) — the
// post-declaration replay residual at the tool-call assembly chokepoint.
// Requirements 4.1, 4.4, 4.5, 4.6, 8.3.
//
// WHAT THIS FILE PROVES
//
// The assembler's single fallback for an unusable finalizer result used to be
// "replay the original fragments". Two chokepoints narrow it: an UNDECIDED
// mandatory completeness requirement refuses closed, and a call with no declared
// requirement keeps the replay unconditionally. Between them sat the hole this
// file closes - once the declaring finalizer had been invoked and had produced a
// result, a LATER finalizer's unusable result still replayed the ORIGINAL,
// alias-bearing fragments, so the reserved namespace reached tool policies and the
// client with no error at all.
//
// The decision this file pins is PRESERVE, not refuse and not replay: the
// document the declaring finalizer was shown and accepted is retained and is what
// the call releases, while the later failure still surfaces through the error
// return. Every unusable shape is driven (a Go error, a panic, an invalid rewrite
// envelope, an unknown action), because each reaches the same fallback through a
// different branch and a fix on only one of them would leave the others open.
//
// The invariants around the decision are pinned in the same file, so a later
// change cannot buy this property by widening the refusal:
//
//   - an UNDECIDED requirement still refuses closed with the bounded
//     ReasonMandatoryBufferingIncomplete, whatever the later finalizer does;
//   - a call with NO declared requirement replays the originals byte-identically
//     and error-free;
//   - the declaring finalizer's OWN failure also refuses closed: it produced no
//     decision, so nothing was satisfied and nothing may be preserved (blocker 3).
//
// IT IS NOT AN ORDERING RULE. Every order here is an absolute fixture constant,
// the assembler still iterates the list MaterializeSorted produced, and the
// property holds for a later finalizer at ANY order above the declaring one
// because the decision is made from the retained document rather than from a
// position in the chain.
//
// NO CONTENT FREEDOM: every failure message below carries counts, bounded labels,
// and byte lengths only. The alias, the real root, and the argument document are
// never formatted.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

const (
	// postDeclaringOrder is the absolute order of the declaring expansion stand-in
	// this file drives. It is a constant rather than an offset from any shipped
	// order so the fixture cannot silently depend on a feature's number.
	postDeclaringOrder = 20

	// postLaterOrder is strictly above postDeclaringOrder, so the declaring
	// finalizer is invoked FIRST and the unusable result arrives afterwards.
	postLaterOrder = postDeclaringOrder + 1

	// postEarlierOrder is strictly below postDeclaringOrder, so the same unusable
	// result arrives while the requirement is still undecided. It is the control
	// for the two halves of the decision.
	postEarlierOrder = postDeclaringOrder - 15

	// postArgsBytes is the assembled argument size every case drives. It is well
	// under the shared finalization cap, so nothing here is about the LIMIT clause.
	postArgsBytes = 8 * 1024
)

// errPostLaterFailure is the LATER finalizer's own error. It is a compile-time
// literal with no path, alias, workspace tag, or argument content in it, and the
// cases assert on its IDENTITY (errors.Is), never on rendered text.
var errPostLaterFailure = errors.New("later finalizer failure")

// panickingOrdinaryFin is an ordinary, non-declaring finalizer that panics. The
// assembler isolates the panic through safety.CallValue, so a panic reaches the
// same fallback as a returned Go error while being a distinct production shape.
type panickingOrdinaryFin struct {
	order int
	calls int
}

func (*panickingOrdinaryFin) ID() string { return "panicking-ordinary" }

func (f *panickingOrdinaryFin) Order() int { return f.order }

func (f *panickingOrdinaryFin) Finalize(
	_ context.Context,
	_ toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	f.calls++
	panic("later finalizer panic")
}

// postSurface classifies what the surfaced failure must be for one unusable-result
// shape. It is an explicit vocabulary rather than a nullable error so that "the
// assembler had no error to surface" and "the finalizer's error must survive" cannot
// be confused, and so a shape cannot silently stop asserting anything.
type postSurface int

const (
	// postSurfaceLaterError means the later finalizer returned a Go error and that
	// exact error must reach the caller.
	postSurfaceLaterError postSurface = iota
	// postSurfaceIsolatedPanic means the later finalizer panicked and the isolated
	// panic the assembler recovered must reach the caller.
	postSurfaceIsolatedPanic
	// postSurfaceNoError means the later finalizer returned a well-formed Result the
	// assembler cannot use - an invalid rewrite envelope or an unknown action - and
	// therefore produced no error at all. The assembler preserves the document and
	// keeps the pre-existing error-free outcome for those two shapes rather than
	// inventing a failure it did not receive.
	postSurfaceNoError
)

// postLaterUnusableShapes are the four ways a LATER finalizer can return
// something the assembler cannot use, with the classification each one's surfaced
// failure must carry. Every shape is a NON-declaring finalizer: none of them stands
// in for the declaring one.
func postLaterUnusableShapes() []struct {
	name     string
	ordinary func(order int) toolcall.Finalizer
	surface  postSurface
} {
	return []struct {
		name     string
		ordinary func(order int) toolcall.Finalizer
		surface  postSurface
	}{
		{
			name: "later_finalizer_error",
			ordinary: func(order int) toolcall.Finalizer {
				return &laterErroringOrdinaryFin{order: order}
			},
			surface: postSurfaceLaterError,
		},
		{
			name: "later_finalizer_panic",
			ordinary: func(order int) toolcall.Finalizer {
				return &panickingOrdinaryFin{order: order}
			},
			surface: postSurfaceIsolatedPanic,
		},
		{
			name: "later_invalid_rewrite_envelope",
			ordinary: func(order int) toolcall.Finalizer {
				return &unusableOrdinaryFin{order: order, res: toolcall.Result{
					Action:     toolcall.ActionRewrite,
					ToolName:   mandatoryToolName,
					ArgsJSON:   []byte(`{"path":`),
					ReasonCode: toolcall.ReasonValidPassThrough,
				}}
			},
			surface: postSurfaceNoError,
		},
		{
			name: "later_unknown_action",
			ordinary: func(order int) toolcall.Finalizer {
				return &unusableOrdinaryFin{order: order, res: toolcall.Result{
					Action:     toolcall.Action(999),
					ReasonCode: toolcall.ReasonValidPassThrough,
				}}
			},
			surface: postSurfaceNoError,
		},
	}
}

// laterErroringOrdinaryFin is an ordinary, non-declaring finalizer that returns a
// Go error carrying a package-level sentinel, so a test can prove the surfaced
// failure is THIS finalizer's own error rather than an assembler refusal wearing
// its place.
type laterErroringOrdinaryFin struct {
	order int
	calls int
}

func (*laterErroringOrdinaryFin) ID() string { return "later-erroring-ordinary" }

func (f *laterErroringOrdinaryFin) Order() int { return f.order }

func (f *laterErroringOrdinaryFin) Finalize(
	_ context.Context,
	_ toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	f.calls++
	return toolcall.Result{}, errPostLaterFailure
}

// TestToolCallAssembler_ALaterUnusableResultPreservesTheMandatorySafeResult is the
// RED for the post-declaration replay residual.
//
// Before the fix this released the ORIGINAL alias-bearing fragments and returned
// no error at all. After it, the released document is the one the declaring
// expansion pass produced, and the later failure still surfaces as an error.
func TestToolCallAssembler_ALaterUnusableResultPreservesTheMandatorySafeResult(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()
	args := mandatoryArgsJSON(postArgsBytes)
	expanded := mandatoryExpandedArgsJSON(postArgsBytes)
	if expanded == args {
		t.Fatal("fixture: the declaring pass must change the document, or nothing is preserved")
	}

	for _, shape := range postLaterUnusableShapes() {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()

			ordinary := shape.ordinary(postLaterOrder)
			if _, declared := ordinary.(toolcall.BufferingRequirement); declared {
				t.Fatal("fixture must be a finalizer without a declaration")
			}
			mand := newMandatoryExpansionFin()
			mand.order = postDeclaringOrder
			a := newToolCallAssembler([]toolcall.Finalizer{mand, ordinary}, 0, catalog)
			if a == nil {
				t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
			}
			if !a.mandatory.mandatoryBoundDeclared {
				t.Fatal("fixture: the declaring finalizer must arm the mandatory requirement")
			}

			released, err := streamMandatoryToolCall(t, a, "post-declaration", args)

			// Fixture guards. The declaring pass really ran on the alias-bearing
			// document and really published the expansion, so there IS a
			// mandatory-safe result to preserve and the case proves something.
			if mand.calls != 1 {
				t.Fatalf("fixture: the declaring finalizer invocations=%d want 1", mand.calls)
			}
			if !strings.Contains(string(mand.seen), mandatoryVirtualRoot) {
				t.Fatal("fixture: the declaring pass must have received an alias-bearing document")
			}

			// The decision: the preserved document is released, never the originals.
			if strings.Contains(released, mandatoryVirtualRoot) {
				t.Fatalf("requirements.md 4.1/4.4 - the reserved namespace reached the client: released=%d bytes",
					len(released))
			}
			if released != expanded {
				t.Fatalf("requirements.md 4.1 - released %d bytes, want the %d bytes the declaring pass produced",
					len(released), len(expanded))
			}
			if released == args {
				t.Fatalf("requirements.md 4.4 - the original %d alias-bearing bytes were replayed", len(released))
			}

			// The later failure still surfaces exactly as the later finalizer produced
			// it, and it is never the assembler's own refusal wearing its place.
			switch shape.surface {
			case postSurfaceLaterError:
				if !errors.Is(err, errPostLaterFailure) {
					t.Fatalf("requirements.md 4.6 - the surfaced failure must be the later finalizer's own error: got %T",
						err)
				}
			case postSurfaceIsolatedPanic:
				var panicErr *safety.PanicError
				if !errors.As(err, &panicErr) || panicErr == nil {
					t.Fatalf("requirements.md 4.6 - the surfaced failure must be the isolated panic: got %T", err)
				}
			case postSurfaceNoError:
				// An unusable RESULT is not a failure the finalizer reported, so the
				// assembler must not invent one. The document is still preserved.
				if err != nil {
					t.Fatalf("requirements.md 4.6 - an unusable result carries no error to surface: got %T", err)
				}
			default:
				t.Fatalf("fixture: unknown surface classification %d", int(shape.surface))
			}
			if IsMandatoryBufferingError(err) {
				t.Fatalf("requirements.md 4.6 - a SATISFIED requirement must not be reported as undecided: reason=%q",
					err.Error())
			}
		})
	}
}

// TestToolCallAssembler_PreserveDecisionBoundaries pins the three boundaries the
// preserve decision must not move.
func TestToolCallAssembler_PreserveDecisionBoundaries(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()
	args := mandatoryArgsJSON(postArgsBytes)

	t.Run("an_undecided_requirement_still_refuses_closed", func(t *testing.T) {
		t.Parallel()
		for _, shape := range postLaterUnusableShapes() {
			t.Run(shape.name, func(t *testing.T) {
				t.Parallel()
				ordinary := shape.ordinary(postEarlierOrder)
				mand := newMandatoryExpansionFin()
				mand.order = postDeclaringOrder
				a := newToolCallAssembler([]toolcall.Finalizer{mand, ordinary}, 0, catalog)
				if a == nil {
					t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
				}

				released, err := streamMandatoryToolCall(t, a, "undecided", args)
				if released != "" {
					t.Fatalf("requirements.md 4.5 - an undecided requirement must release nothing: released=%d bytes",
						len(released))
				}
				if strings.Contains(released, mandatoryVirtualRoot) {
					t.Fatal("requirements.md 4.5 - the reserved namespace was released")
				}
				var mbe *MandatoryBufferingError
				if !errors.As(err, &mbe) || mbe == nil {
					t.Fatalf("requirements.md 4.5/4.6 - want a typed MandatoryBufferingError, got %T", err)
				}
				if mbe.Reason != ReasonMandatoryBufferingIncomplete {
					t.Fatalf("requirements.md 4.6 - reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingIncomplete)
				}
				if mand.calls != 0 {
					t.Fatalf("fixture: the declaring finalizer must not have decided, got %d invocations", mand.calls)
				}
			})
		}
	})

	t.Run("a_call_with_no_declared_requirement_stays_byte_identical", func(t *testing.T) {
		t.Parallel()
		for _, shape := range postLaterUnusableShapes() {
			t.Run(shape.name, func(t *testing.T) {
				t.Parallel()
				ordinary := shape.ordinary(postLaterOrder)
				a := newToolCallAssembler([]toolcall.Finalizer{ordinary}, 0, catalog)
				if a == nil {
					t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
				}
				if a.mandatory.mandatoryBoundDeclared {
					t.Fatal("fixture: a finalizer without a declaration must not arm the requirement")
				}

				released, err := streamMandatoryToolCall(t, a, "no-declaration", args)
				if err != nil {
					t.Fatalf("no declared requirement means no assembler refusal or surfaced failure: %T", err)
				}
				if released != args {
					t.Fatalf("released %d bytes, want the %d original bytes replayed unchanged",
						len(released), len(args))
				}
			})
		}
	})

	t.Run("the_declaring_finalizer_s_own_failure_refuses_closed", func(t *testing.T) {
		t.Parallel()
		// CHANGED from "is unchanged", which codified the defect the second
		// adversarial review raised as blocker 3: the pending flag was cleared as
		// soon as the DECLARING finalizer was invoked, so its own error produced
		// neither a pending requirement nor a document to preserve, and the
		// fallback replayed the ORIGINAL alias-bearing fragments. A requirement
		// becomes satisfied only on a usable decision (requirements.md 8.3, 4.4),
		// and the preserve answer below is only reachable once something was
		// actually decided.
		ordinary := &legacyOptOutFin{}
		mand := &failingMandatoryFin{order: postDeclaringOrder}
		a := newToolCallAssembler([]toolcall.Finalizer{ordinary, mand}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}

		released, err := streamMandatoryToolCall(t, a, "declaring-own-failure", args)
		if released != "" {
			t.Fatalf("requirements.md 8.3 - released %d original argument bytes", len(released))
		}
		if strings.Contains(released, mandatoryVirtualRoot) {
			t.Fatal("requirements.md 8.3 - the reserved namespace reached the client")
		}
		var mbe *MandatoryBufferingError
		if !errors.As(err, &mbe) || mbe == nil {
			t.Fatalf("requirements.md 8.3 - want a typed MandatoryBufferingError, got %T", err)
		}
		if mbe.Reason != ReasonMandatoryBufferingIncomplete {
			t.Fatalf("requirements.md 8.3 - reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingIncomplete)
		}
		if mbe.FinalizerID != mand.ID() {
			t.Fatalf("requirements.md 8.3 - the refusal must name the declarer that never decided: got %q want %q",
				mbe.FinalizerID, mand.ID())
		}
		if mand.calls != 1 {
			t.Fatalf("fixture: the declaring finalizer must have been invoked: got %d invocations", mand.calls)
		}
	})
}
