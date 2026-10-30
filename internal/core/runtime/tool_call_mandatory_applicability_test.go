package runtime

// Spec: b-leg-path-virtualization second adversarial review, blockers 1, 2, 3
// and 5 (P1/P1/P1/P2). Requirements 4.5, 4.6, 4.7, 8.3, 8.4 and design.md section
// 6 ("if an APPLICABLE call exceeds that mandatory bound, reject before any
// alias-bearing argument reaches the client").
//
// ONE ROOT CAUSE, FOUR SYMPTOMS
//
// Mandatory buffering was compressed, at composition time, into ONE
// assembler-wide bound plus ONE boolean, inside a contract that is generic and
// public. That single pair of values could not express four things the contract
// has to say:
//
//   - WHICH CALLS a declaration governs (blocker 1). Path expansion is selective:
//     `expansion.Finalizer.decide` can answer "no selectors" and pass, so a call
//     the feature would never touch was still refused, and a small unrelated call
//     whose earlier OPTIONAL finalizer failed became
//     `mandatory_buffering_incomplete`.
//   - WHICH LIMIT each declarer published (blocker 2). Aggregation took the
//     MAXIMUM, so a declarer's own smaller bound was unenforceable once any
//     larger shared cap existed; the real expansion finalizer records
//     `Report.ArgsOverDeclaredBound` for telemetry and never refuses.
//   - WHEN a requirement became SATISFIED (blocker 3). The pending flag was
//     cleared BEFORE the declaring finalizer ran, so the declaring finalizer's
//     OWN error or panic fell through to the replay fallback and released the
//     original alias-bearing fragments.
//   - HOW MANY requirements a call carried (blocker 5). One boolean cannot
//     compose two mandatory finalizers: the first cleared it and the second was
//     silently bypassed.
//
// This file drives all four through the assembler as it is composed of the real
// shipped SDK contract, and each case names the requirement it defends.
//
// NO CONTENT FREEDOM: every message below carries counts, bounded labels, and
// byte lengths only. The reserved alias, the real root, the workspace tag, and
// the argument document are never formatted into an assertion message.

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// scopedExpansionFin is a finalizer that declares a mandatory completeness
// requirement AND scopes it to the tool names it was configured with, which is
// how a selective feature such as path virtualization says "this bound governs
// the tools whose argument locations an operator selected, not every tool on
// the chain".
//
// It deliberately does NOT enforce its own declared bound. That mirrors the real
// shipped expansion pass exactly, which records `Report.ArgsOverDeclaredBound`
// and never yields ActionReject, and it is what makes this file a test of the
// CONSUMER's enforcement rather than of a cooperative finalizer: if the
// consumer does not enforce the declarer's own limit, nothing here refuses.
type scopedExpansionFin struct {
	id       string
	order    int
	spec     toolcall.BufferingSpec
	selected map[string]struct{}
	calls    int
	seen     []byte
	// failOnCall makes Finalize return a Go error on the given 1-based invocation
	// count; zero disables it.
	failOnCall int
	// panicOnCall makes Finalize panic on the given 1-based invocation count; zero
	// disables it. It is a distinct production shape from a returned error: the
	// consumer isolates it through safety.CallValue.
	panicOnCall int
	// mutate, when non-nil, is invoked from ToolCallBufferingApplies with the tool
	// definition and catalog the consumer handed over. It exists so a HOSTILE
	// declarer can be driven through the real isolation boundary: the consumer must
	// hand over a detached copy, so a declarer that scribbles in place can corrupt
	// only what it was given and never the assembler's own catalog.
	mutate func(tool aipapi.ToolDef, catalog []aipapi.ToolDef)
}

var (
	_ toolcall.Finalizer              = (*scopedExpansionFin)(nil)
	_ toolcall.BufferingRequirement   = (*scopedExpansionFin)(nil)
	_ toolcall.BufferingApplicability = (*scopedExpansionFin)(nil)
)

func (f *scopedExpansionFin) ID() string { return f.id }

func (f *scopedExpansionFin) Order() int { return f.order }

func (f *scopedExpansionFin) ToolCallBufferingRequirement() toolcall.BufferingSpec {
	return f.spec
}

// ToolCallBufferingApplies implements toolcall.BufferingApplicability by exact
// tool name, which is the generic answer: a finalizer knows which of the tools on
// the chain it actually inspects.
func (f *scopedExpansionFin) ToolCallBufferingApplies(toolName string, tool aipapi.ToolDef, catalog []aipapi.ToolDef) bool {
	if f.mutate != nil {
		f.mutate(tool, catalog)
	}
	_, ok := f.selected[toolName]
	return ok
}

func (f *scopedExpansionFin) Finalize(
	_ context.Context,
	call toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	f.calls++
	f.seen = append(f.seen[:0], call.ArgsJSON...)
	if f.panicOnCall == f.calls {
		panic("scoped declaring finalizer panic")
	}
	if f.failOnCall == f.calls {
		return toolcall.Result{}, errDeclaringFailure
	}
	expanded := strings.Replace(string(call.ArgsJSON), mandatoryVirtualRoot, mandatoryRealRoot+"/", 1)
	if expanded == string(call.ArgsJSON) {
		return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
	}
	return toolcall.Result{
		Action:     toolcall.ActionRewrite,
		ToolName:   call.ToolName,
		ArgsJSON:   []byte(expanded),
		ReasonCode: toolcall.ReasonValidPassThrough,
	}, nil
}

// errDeclaringFailure is the declaring finalizer's own error. It is a
// compile-time literal with no path, alias, workspace tag, or argument content
// in it, and the cases below assert on its IDENTITY, never on rendered text.
var errDeclaringFailure = errors.New("declaring finalizer failure")

// newScopedFin builds a scoped declarer over the shared alias-bearing fixture.
func newScopedFin(t *testing.T, id string, order int, spec toolcall.BufferingSpec, selected ...string) *scopedExpansionFin {
	t.Helper()
	names := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		names[name] = struct{}{}
	}
	return &scopedExpansionFin{id: id, order: order, spec: spec, selected: names}
}

// unselectedToolName is a tool the scoped declarers in this file were NOT
// configured for. It is in the catalog, so the assembler holds and finalizes the
// call exactly as it does for any declared tool; only the declarative scope says
// the requirement does not govern it.
const unselectedToolName = "list_directory_entries"

func scopedCatalog() []aipapi.ToolDef {
	return []aipapi.ToolDef{
		{Name: mandatoryToolName, Parameters: []byte(`{"type":"object"}`)},
		{Name: unselectedToolName, Parameters: []byte(`{"type":"object"}`)},
	}
}

// streamToolCallNamed drives one completed call for an arbitrary tool name
// through the assembler as started / args-delta* / finished and returns the
// argument bytes the client would observe plus the assembler's error. It is
// streamMandatoryToolCall with the tool name lifted, because applicability is
// derived from the tool name and a fixture that cannot name a non-selected tool
// cannot exercise blocker 1 at all.
func streamToolCallNamed(t *testing.T, a *toolCallAssembler, id, toolName, argsJSON string) (string, error) {
	t.Helper()
	ctx := context.Background()
	meta := toolcall.Meta{}
	var released strings.Builder

	if held, ingErr := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: toolName,
	}, meta, nil); ingErr != nil || !held {
		t.Fatalf("started: held=%v err=%v", held, ingErr)
	}
	for _, fragment := range splitMandatoryArgsFragments(argsJSON) {
		held, ingErr := a.ingest(ctx, aipapi.Event{
			Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id,
			ToolName: toolName, Delta: fragment,
		}, meta, nil)
		if ingErr != nil {
			t.Fatalf("args delta: %v", ingErr)
		}
		if !held {
			released.WriteString(fragment)
		}
	}
	_, err := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallFinished, ToolCallID: id, ToolName: toolName,
	}, meta, nil)
	for {
		ev, ok := a.popDrain()
		if !ok {
			if err == nil {
				err = a.popDrainError()
			}
			return released.String(), err
		}
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			released.WriteString(ev.Delta)
		}
	}
}

// TestToolCallAssembler_ANonSelectedToolKeepsLegacyBehaviorAboveTheDeclaredBound
// is blocker 1's RED, and requirement 4.7.
//
// A scoped declarer exists on the chain, so the composition has a 1 MiB declared
// mandatory bound. The call is for a tool no declarer was configured for and is
// 1 MiB + 8 KiB, which is past that declared bound. Neither the assembler nor any
// finalizer has any business refusing it: the legacy answer is to release the
// original argument fragments.
//
// Before the fix the single global boolean made the assembler refuse the call
// with `mandatory_buffering_overflow` before any finalizer ran, purely because
// the feature was installed.
func TestToolCallAssembler_ANonSelectedToolKeepsLegacyBehaviorAboveTheDeclaredBound(t *testing.T) {
	t.Parallel()

	catalog := scopedCatalog()
	fin := newScopedFin(t, "scoped-expansion", 20, toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}, mandatoryToolName)

	// Fixture guards: the boundary being crossed is the DECLARED one, and the
	// shared legacy bound is far below it, so only the mandatory declaration can
	// account for a refusal.
	const oversize = toolcall.DefaultMandatoryMaxArgsBytes + 8*1024
	args := mandatoryArgsJSON(oversize)
	if len(args) != oversize {
		t.Fatalf("fixture sizing: got %d want %d", len(args), oversize)
	}
	if len(args) <= defaultToolCallFinalizationMaxArgsBytes {
		t.Fatalf("fixture must exceed the shared legacy bound: %d <= %d",
			len(args), defaultToolCallFinalizationMaxArgsBytes)
	}

	a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
	if a == nil {
		t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
	}
	if !a.mandatory.mandatoryBoundDeclared {
		t.Fatal("fixture: the declaration must be present, or the case proves nothing")
	}

	released, err := streamToolCallNamed(t, a, "unselected-over", unselectedToolName, args)
	// Requirements 4.7 / design.md 6: the pre-existing behavior is preserved
	// byte for byte and error free.
	if err != nil {
		t.Fatalf("requirements.md 4.7 - a tool no declaration governs must not fail the call: %T", err)
	}
	if IsMandatoryBufferingError(err) {
		t.Fatalf("requirements.md 4.7 - a mandatory refusal was manufactured for a non-selected tool: %q", err.Error())
	}
	if released != args {
		t.Fatalf("requirements.md 4.7 - released %d bytes, want the %d original bytes replayed unchanged",
			len(released), len(args))
	}
	if fin.calls != 0 {
		t.Fatalf("fixture: a non-selected tool must not reach the declarer, got %d invocations", fin.calls)
	}

	// The non-vacuity control in the SAME composition: the very same declarer and
	// the very same assembler do govern the selected tool, so a fix that simply
	// ignored every declaration would fail here instead of passing the case above
	// by making mandatory buffering disappear.
	selected := newScopedFin(t, "scoped-expansion", 20, toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}, mandatoryToolName)
	a2 := newToolCallAssembler([]toolcall.Finalizer{selected}, 0, catalog)
	small := mandatoryArgsJSON(defaultToolCallFinalizationMaxArgsBytes + 4096)
	if len(small) > toolcall.DefaultMandatoryMaxArgsBytes {
		t.Fatalf("control fixture must stay inside the declared bound: %d", len(small))
	}
	released2, err2 := streamToolCallNamed(t, a2, "selected-over-shared", mandatoryToolName, small)
	if err2 != nil {
		t.Fatalf("control: the selected tool must still reach its declarer: %v", err2)
	}
	if selected.calls != 1 {
		t.Fatalf("control: the declarer must have run on the complete document, got %d invocations", selected.calls)
	}
	if strings.Contains(released2, mandatoryVirtualRoot) {
		t.Fatalf("control: requirements.md 4.1 - released %d bytes with the reserved namespace still present",
			len(released2))
	}
}

// TestToolCallAssembler_ANonSelectedToolKeepsTheReplayWhenAnUnrelatedFinalizerFails
// is the second half of blocker 1: requirement 4.6's failure clause must not
// reach a call no declaration governs either.
//
// Before the fix, `finalizeCall` seeded EVERY call with
// `mandatoryPending = a.mandatory.mandatoryBoundDeclared`, so a small unrelated
// call whose earlier OPTIONAL finalizer failed was reported as
// `mandatory_buffering_incomplete` even though no declarer had ever claimed it.
func TestToolCallAssembler_ANonSelectedToolKeepsTheReplayWhenAnUnrelatedFinalizerFails(t *testing.T) {
	t.Parallel()

	const failingOrder = 10
	ordinary := &laterErroringOrdinaryFin{order: failingOrder}
	fin := newScopedFin(t, "scoped-expansion", 20, toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}, mandatoryToolName)
	a := newToolCallAssembler([]toolcall.Finalizer{ordinary, fin}, 0, scopedCatalog())
	if a == nil {
		t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
	}

	args := mandatoryArgsJSON(8 * 1024)
	released, err := streamToolCallNamed(t, a, "unrelated-failure", unselectedToolName, args)

	if IsMandatoryBufferingError(err) {
		t.Fatalf("requirements.md 4.6/4.7 - a call no declaration governs must not be reported as an undecided mandatory requirement: %q",
			err.Error())
	}
	// The pre-existing fallback for a call no declaration governs discards a
	// failure it cannot attach to any released document, and it does so here too:
	// requirement 4.7 asks for byte-identical existing behavior, not for a new
	// error. What must NOT happen is the assembler inventing its own refusal.
	if err != nil {
		t.Fatalf("requirements.md 4.7 - the pre-existing replay produced no error, got %T", err)
	}
	if released != args {
		t.Fatalf("requirements.md 4.7 - released %d bytes, want the %d original bytes replayed unchanged",
			len(released), len(args))
	}
	if fin.calls != 0 {
		t.Fatalf("fixture: a non-selected tool must not reach the declarer, got %d invocations", fin.calls)
	}
}

// TestToolCallAssembler_EachDeclarerEnforcesItsOwnDeclaredBound is blocker 2's
// RED, and requirement 4.5.
//
// The numbers are the ones the blocker names. The declaration is
// `mandatory_max_args_bytes: 65536` beside a shared cap of 128 KiB, and the call
// is 96 KiB: comfortably inside the cap the assembler actually buffers to, and
// comfortably outside the bound the declarer published. The shipped expansion
// pass records `Report.ArgsOverDeclaredBound` for telemetry and never refuses, so
// nothing but the consumer can enforce that limit.
//
// The second case is the same defect with two declarers instead of a shared cap:
// 64 KiB and 1 MiB aggregate to 1 MiB, which silently destroys the smaller
// contract, and the call that proves it is 96 KiB.
func TestToolCallAssembler_EachDeclarerEnforcesItsOwnDeclaredBound(t *testing.T) {
	t.Parallel()

	catalog := scopedCatalog()
	// Exactly the blocker's configuration: a 64 KiB declared bound beside a 128 KiB
	// shared cap, and a call of 96 KiB.
	const (
		declaredBound = toolcall.MinMandatoryMaxArgsBytes // 65536
		sharedCap     = 128 * 1024
		callBytes     = 96 * 1024
	)
	args := mandatoryArgsJSON(callBytes)
	if len(args) != callBytes {
		t.Fatalf("fixture sizing: got %d want %d", len(args), callBytes)
	}
	if len(args) <= declaredBound || len(args) > sharedCap {
		t.Fatalf("fixture must sit between the declarer's own bound and the shared cap: %d not in (%d, %d]",
			len(args), declaredBound, sharedCap)
	}

	assertOwnBoundRefusal := func(t *testing.T, released string, err error, a *toolCallAssembler, wantFinalizerID string, gotCalls int) {
		t.Helper()
		if released != "" {
			t.Fatalf("requirements.md 4.5 - a call past the declarer's own bound must release nothing: released=%d bytes",
				len(released))
		}
		var mbe *MandatoryBufferingError
		if !errors.As(err, &mbe) || mbe == nil {
			t.Fatalf("requirements.md 4.5 - want a typed MandatoryBufferingError, got %T", err)
		}
		if mbe.Reason != ReasonMandatoryBufferingOverflow {
			t.Fatalf("requirements.md 4.5 - reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingOverflow)
		}
		if mbe.FinalizerID != wantFinalizerID {
			t.Fatalf("requirements.md 4.5 - the refusal must name the declarer whose bound was exceeded: got %q want %q",
				mbe.FinalizerID, wantFinalizerID)
		}
		if mbe.MaxArgsBytes != declaredBound {
			t.Fatalf("requirements.md 4.5 - the refusal must report the declarer's OWN bound: got %d want %d",
				mbe.MaxArgsBytes, declaredBound)
		}
		if gotCalls != 0 {
			t.Fatalf("requirements.md 4.5 - the declarer must not be handed a document past its own bound: got %d invocations",
				gotCalls)
		}
		// The refusal must release the per-call state so the assembler is reusable.
		if _, ok := a.refusing["own-bound"]; ok {
			t.Fatal("a refused tool call must be removed from the refusing set")
		}
		if _, ok := a.active["own-bound"]; ok {
			t.Fatal("a refused tool call must be removed from the active set")
		}
	}

	t.Run("beside_a_larger_shared_cap", func(t *testing.T) {
		t.Parallel()
		fin := newScopedFin(t, "small-bound", 20, toolcall.BufferingSpec{
			MaxArgsBytes: declaredBound,
			Overflow:     toolcall.OverflowReject,
		}, mandatoryToolName)
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, sharedCap, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		// The pre-existing shared bound keeps its operator-raised value; only the
		// physical ceiling is affected by the declaration.
		if a.maxArgsBytes != sharedCap {
			t.Fatalf("shared cap: got %d want %d", a.maxArgsBytes, sharedCap)
		}
		released, err := streamToolCallNamed(t, a, "own-bound", mandatoryToolName, args)
		assertOwnBoundRefusal(t, released, err, a, fin.ID(), fin.calls)
	})

	t.Run("beside_a_larger_second_declaration", func(t *testing.T) {
		t.Parallel()
		small := newScopedFin(t, "small-bound", 20, toolcall.BufferingSpec{
			MaxArgsBytes: declaredBound,
			Overflow:     toolcall.OverflowReject,
		}, mandatoryToolName)
		large := newScopedFin(t, "large-bound", 21, toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
			Overflow:     toolcall.OverflowReject,
		}, mandatoryToolName)
		a := newToolCallAssembler([]toolcall.Finalizer{small, large}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		released, err := streamToolCallNamed(t, a, "own-bound", mandatoryToolName, args)
		// The smaller contract must survive aggregation: the refusal names the
		// 64 KiB declarer, and the larger one must never have run either, because
		// the call was already refused at the first declarer.
		assertOwnBoundRefusal(t, released, err, a, small.ID(), small.calls)
		if large.calls != 0 {
			t.Fatalf("requirements.md 4.5 - a call refused at the first declarer must not reach the second: got %d invocations",
				large.calls)
		}
	})
}

// TestToolCallAssembler_TwoMandatoryFinalizersKeepTheSecondRequirementPending is
// blocker 5's RED.
//
// The composition is mandatory A, an unrelated OPTIONAL finalizer that fails,
// then mandatory B. A's success used to clear one shared boolean, the optional
// failure then reached the replay fallback with no pending requirement left, and
// the call released A's document - so B's mandatory completeness requirement was
// silently bypassed even though B never ran.
//
// The non-vacuity control is the same pair with the unrelated finalizer absent:
// both declarers decide and the call is released expanded.
func TestToolCallAssembler_TwoMandatoryFinalizersKeepTheSecondRequirementPending(t *testing.T) {
	t.Parallel()

	const (
		firstOrder      = 10
		unrelatedOrder  = 11
		secondOrder     = 12
		firstFinalizer  = "mandatory-first"
		secondFinalizer = "mandatory-second"
	)
	spec := toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}
	catalog := scopedCatalog()
	args := mandatoryArgsJSON(8 * 1024)

	t.Run("unrelated_failure_between_two_mandatory_finalizers_refuses_closed", func(t *testing.T) {
		t.Parallel()
		ordinary := &laterErroringOrdinaryFin{order: unrelatedOrder}
		first := newScopedFin(t, firstFinalizer, firstOrder, spec, mandatoryToolName)
		second := newScopedFin(t, secondFinalizer, secondOrder, spec, mandatoryToolName)
		a := newToolCallAssembler([]toolcall.Finalizer{first, ordinary, second}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}

		released, err := streamToolCallNamed(t, a, "two-mandatory", mandatoryToolName, args)

		if released != "" {
			t.Fatalf("requirements.md 4.6 - an undecided mandatory requirement must release nothing: released=%d bytes",
				len(released))
		}
		if strings.Contains(released, mandatoryVirtualRoot) {
			t.Fatal("requirements.md 8.3 - the reserved namespace reached the client")
		}
		var mbe *MandatoryBufferingError
		if !errors.As(err, &mbe) || mbe == nil {
			t.Fatalf("requirements.md 4.6 - want a typed MandatoryBufferingError, got %T", err)
		}
		if mbe.Reason != ReasonMandatoryBufferingIncomplete {
			t.Fatalf("requirements.md 4.6 - reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingIncomplete)
		}
		// The refusal must name the declarer that never decided, which is the
		// observable proof that the SECOND requirement - not the first - is the
		// one still pending.
		if mbe.FinalizerID != secondFinalizer {
			t.Fatalf("requirements.md 4.6 - the refusal must name the still-pending declarer: got %q want %q",
				mbe.FinalizerID, secondFinalizer)
		}
		if err.Error() != "tool call finalization: tool call assembler: mandatory buffering requirement not honored ("+
			ReasonMandatoryBufferingIncomplete+")" {
			t.Fatalf("refusal message must be the bounded, content-free classification: %q", err.Error())
		}
		if first.calls != 1 {
			t.Fatalf("fixture: the first declarer must have decided, got %d invocations", first.calls)
		}
		if second.calls != 0 {
			t.Fatalf("fixture: the second declarer must not have decided, got %d invocations", second.calls)
		}
		if ordinary.calls != 1 {
			t.Fatalf("fixture: the unrelated finalizer must have run, got %d invocations", ordinary.calls)
		}
	})

	t.Run("without_the_unrelated_failure_both_declare_and_the_call_is_released", func(t *testing.T) {
		t.Parallel()
		first := newScopedFin(t, firstFinalizer, firstOrder, spec, mandatoryToolName)
		second := newScopedFin(t, secondFinalizer, secondOrder, spec, mandatoryToolName)
		a := newToolCallAssembler([]toolcall.Finalizer{first, second}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}

		released, err := streamToolCallNamed(t, a, "two-mandatory-ok", mandatoryToolName, args)
		if err != nil {
			t.Fatalf("both requirements were decided, so the call must not be refused: %v", err)
		}
		if first.calls != 1 || second.calls != 1 {
			t.Fatalf("fixture: both declarers must have decided, got %d and %d invocations", first.calls, second.calls)
		}
		if released != mandatoryExpandedArgsJSON(8*1024) {
			t.Fatalf("requirements.md 4.1 - released %d bytes with reserved namespace present=%t, want the expanded document",
				len(released), strings.Contains(released, mandatoryVirtualRoot))
		}
	})
}

// TestToolCallAssembler_ADeclaringFinalizersOwnFailureRefusesClosed is blocker
// 3's RED, and requirement 8.3.
//
// `finalizeCall` cleared the pending flag BEFORE invoking the declarer, so when
// the DECLARING finalizer itself failed or panicked the flag was already false
// AND no mandatory-safe document existed to release. The fallback then replayed
// the ORIGINAL alias-bearing fragments. A requirement becomes satisfied only
// after that finalizer returns a usable Pass, a usable Rewrite, or an explicit
// Reject; `safety.CallValue` exists precisely because an extension panic must
// cross the boundary as a failure rather than as a released document.
//
// It replaces the characterization that asserted the replay, which codified the
// defect.
func TestToolCallAssembler_ADeclaringFinalizersOwnFailureRefusesClosed(t *testing.T) {
	t.Parallel()

	const declaringOrder = 10
	spec := toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}
	catalog := scopedCatalog()
	args := mandatoryArgsJSON(8 * 1024)

	for _, shape := range []struct {
		name        string
		failOnCall  int
		panicOnCall int
	}{
		{name: "declaring_finalizer_error", failOnCall: 1},
		{name: "declaring_finalizer_panic", panicOnCall: 1},
	} {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()
			fin := newScopedFin(t, "declaring", declaringOrder, spec, mandatoryToolName)
			fin.failOnCall = shape.failOnCall
			fin.panicOnCall = shape.panicOnCall
			a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
			if a == nil {
				t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
			}

			released, err := streamToolCallNamed(t, a, "declaring-own-failure", mandatoryToolName, args)

			// Requirements 8.3 / 4.4: the original alias-bearing fragments must
			// never reach tool policies or the client.
			if released != "" {
				t.Fatalf("requirements.md 8.3 - released %d original argument bytes", len(released))
			}
			if strings.Contains(released, mandatoryVirtualRoot) || strings.Contains(released, mandatoryRealRoot) {
				t.Fatal("requirements.md 8.3 - a path reached the client on a call whose declarer never decided")
			}
			var mbe *MandatoryBufferingError
			if !errors.As(err, &mbe) || mbe == nil {
				t.Fatalf("requirements.md 8.3 - want a typed MandatoryBufferingError, got %T", err)
			}
			if mbe.Reason != ReasonMandatoryBufferingIncomplete {
				t.Fatalf("requirements.md 8.3 - reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingIncomplete)
			}
			if mbe.FinalizerID != fin.ID() {
				t.Fatalf("requirements.md 8.3 - the refusal must name the declarer that never decided: got %q want %q",
					mbe.FinalizerID, fin.ID())
			}
			if mbe.MaxArgsBytes != 0 {
				t.Fatalf("no bound was exceeded, so MaxArgsBytes must stay zero: got %d", mbe.MaxArgsBytes)
			}
			// The declarer's own failure is REPLACED by the typed refusal, not
			// surfaced alongside it: nothing is released, so there is no released
			// document for the later-failure answer to attach to. The refusal is
			// therefore the whole answer, and it must classify through the sentinel.
			if !errors.Is(err, ErrMandatoryBuffering) || !IsMandatoryBufferingError(err) {
				t.Fatalf("requirements.md 8.3 - the refusal must classify through the sentinel, got %v", err)
			}
			if errors.Is(err, errDeclaringFailure) {
				t.Fatalf("requirements.md 8.3 - the declarer's own error must not be mistaken for the refusal")
			}
			if fin.calls != 1 {
				t.Fatalf("fixture: the declarer must have been invoked, got %d invocations", fin.calls)
			}
		})
	}
}

// TestToolCallAssembler_ApplicabilityIsDerivedPerCallFromToolName pins the
// derivation itself, so the per-call requirement set is observable rather than
// inferred from a release.
//
// The SAME assembler is asked the same question for a selected and a
// non-selected tool in one test, which is what makes the case non-vacuous: an
// implementation that answered "applies to everything" would pass the selected
// half and fail the non-selected one.
func TestToolCallAssembler_ApplicabilityIsDerivedPerCallFromToolName(t *testing.T) {
	t.Parallel()

	catalog := scopedCatalog()
	fin := newScopedFin(t, "scoped-expansion", 20, toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}, mandatoryToolName)
	a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
	if a == nil {
		t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
	}
	if fin.ToolCallBufferingApplies(mandatoryToolName, catalog[0], catalog) != true {
		t.Fatal("fixture: the configured tool must be selected")
	}
	if fin.ToolCallBufferingApplies(unselectedToolName, catalog[1], catalog) != false {
		t.Fatal("fixture: the unconfigured tool must not be selected")
	}

	meta := toolcall.Meta{}
	readRequirements := func(t *testing.T, id, toolName string) (*callRequirements, bool) {
		t.Helper()
		if held, err := a.ingest(context.Background(), aipapi.Event{
			Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: toolName,
		}, meta, nil); err != nil || !held {
			t.Fatalf("started %s: held=%v err=%v", id, held, err)
		}
		buf, ok := a.active[id]
		if !ok {
			t.Fatalf("started %s: no active buffer", id)
		}
		return buf.requirements, true
	}

	selected, ok := readRequirements(t, "derive-selected", mandatoryToolName)
	if !ok || selected == nil {
		t.Fatalf("a selected tool must carry an applicable mandatory requirement")
	}
	if got := selected.pendingCount(); got != 1 {
		t.Fatalf("selected tool pending requirement count = %d, want 1", got)
	}
	if selected.limitBytes != toolcall.DefaultMandatoryMaxArgsBytes {
		t.Fatalf("selected tool buffering ceiling: got %d want %d",
			selected.limitBytes, toolcall.DefaultMandatoryMaxArgsBytes)
	}
	if !selected.refusesPastLimit() {
		t.Fatal("a selected tool whose declarer asks for OverflowReject must refuse past its ceiling")
	}

	unselected, ok := readRequirements(t, "derive-unselected", unselectedToolName)
	if !ok {
		t.Fatal("the assembler must expose the derived requirement set for inspection")
	}
	if unselected != nil {
		t.Fatalf("a non-selected tool must carry NO applicable requirement, got %d", len(unselected.items))
	}
}

// TestToolCallAssembler_APassThroughDeclarerIsNotInvokedPastItsOwnBound pins the
// third overflow policy's own meaning.
//
// `OverflowPassThrough` promises the pre-existing behavior past the declared
// bound: the declarer is not invoked. When that declarer's own bound is BELOW
// the ceiling the assembler actually buffers to, the consumer is the only thing
// that can honor the promise - and it must also discharge the requirement rather
// than leave it pending forever, which would refuse every such call instead.
func TestToolCallAssembler_APassThroughDeclarerIsNotInvokedPastItsOwnBound(t *testing.T) {
	t.Parallel()

	const (
		declaredBound = toolcall.MinMandatoryMaxArgsBytes
		sharedCap     = 128 * 1024
		callBytes     = 96 * 1024
	)
	catalog := scopedCatalog()
	spec := toolcall.BufferingSpec{
		MaxArgsBytes: declaredBound,
		Overflow:     toolcall.OverflowPassThrough,
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("fixture: the pass-through declaration must be well formed: %v", err)
	}

	// Over its own bound: not invoked, no refusal, pre-existing replay.
	over := newScopedFin(t, "pass-through", 20, spec, mandatoryToolName)
	a := newToolCallAssembler([]toolcall.Finalizer{over}, sharedCap, catalog)
	if a == nil {
		t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
	}
	args := mandatoryArgsJSON(callBytes)
	released, err := streamToolCallNamed(t, a, "pass-through-over", mandatoryToolName, args)
	if err != nil {
		t.Fatalf("requirements.md 4.5 - a pass-through declaration must not refuse past its own bound: %v", err)
	}
	if over.calls != 0 {
		t.Fatalf("the pass-through declarer must not be invoked past its own bound, got %d invocations", over.calls)
	}
	if released != args {
		t.Fatalf("released %d bytes, want the %d original bytes replayed unchanged", len(released), len(args))
	}

	// Inside its own bound: invoked exactly as any other declarer, and the
	// requirement is satisfied by its usable decision. This half is what makes
	// the case non-vacuous: skipping every declarer would satisfy the first half
	// and fail here.
	under := newScopedFin(t, "pass-through", 20, spec, mandatoryToolName)
	a2 := newToolCallAssembler([]toolcall.Finalizer{under}, sharedCap, catalog)
	small := mandatoryArgsJSON(declaredBound - 4096)
	if len(small) >= declaredBound {
		t.Fatalf("fixture must stay inside the declared bound: %d >= %d", len(small), declaredBound)
	}
	released2, err2 := streamToolCallNamed(t, a2, "pass-through-under", mandatoryToolName, small)
	if err2 != nil {
		t.Fatalf("a call inside its own bound must be decided normally: %v", err2)
	}
	if under.calls != 1 {
		t.Fatalf("the declarer must run inside its own bound, got %d invocations", under.calls)
	}
	if released2 != mandatoryExpandedArgsJSON(declaredBound-4096) {
		t.Fatalf("requirements.md 4.1 - released %d bytes with reserved namespace present=%t, want the expanded document",
			len(released2), strings.Contains(released2, mandatoryVirtualRoot))
	}
}

// TestToolCallAssembler_ACallWithNoApplicableRequirementIsByteIdentical is
// requirement 4.7's positive control: the legacy path must be unchanged for a
// call that no declaration governs, on every unusable-result shape, and the
// assembler must invent no refusal of its own.
func TestToolCallAssembler_ACallWithNoApplicableRequirementIsByteIdentical(t *testing.T) {
	t.Parallel()

	catalog := scopedCatalog()
	args := mandatoryArgsJSON(8 * 1024)

	for _, shape := range postLaterUnusableShapes() {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()
			ordinary := shape.ordinary(postLaterOrder)
			// A declarer that IS on the chain and that this very tool is NOT
			// selected by: the only difference from the legacy chain is its
			// presence, so this case isolates that.
			fin := newScopedFin(t, "scoped-expansion", postDeclaringOrder, toolcall.BufferingSpec{
				MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
				Overflow:     toolcall.OverflowReject,
			}, mandatoryToolName)
			a := newToolCallAssembler([]toolcall.Finalizer{ordinary, fin}, 0, catalog)
			if a == nil {
				t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
			}

			released, err := streamToolCallNamed(t, a, "no-applicable", unselectedToolName, args)

			if IsMandatoryBufferingError(err) {
				t.Fatalf("requirements.md 4.7 - a call with no applicable requirement must not be refused: %q", err.Error())
			}
			// Every shape here produces NO error, because the pre-existing fallback
			// discards a failure it cannot attach to any released document: with no
			// applicable requirement there is no mandatory-safe document, so the
			// replay returns nil. Requirement 4.7 asks for byte-identical existing
			// behavior, and this is that behavior on all four shapes alike.
			if err != nil {
				t.Fatalf("requirements.md 4.7 - the pre-existing replay produced no error: got %T", err)
			}
			if released != args {
				t.Fatalf("requirements.md 4.7 - released %d bytes, want the %d original bytes replayed unchanged",
					len(released), len(args))
			}
		})
	}
}

// panickingApplicabilityFin proves the applicability capability is extension
// code: it panics instead of answering. The assembler must isolate that panic
// and fail closed by treating the undeterminable scope as applicable, rather
// than letting one declarer's bug crash assembly.
type panickingApplicabilityFin struct {
	id    string
	order int
	spec  toolcall.BufferingSpec
}

func (f *panickingApplicabilityFin) ID() string { return f.id }
func (f *panickingApplicabilityFin) Order() int { return f.order }
func (f *panickingApplicabilityFin) ToolCallBufferingRequirement() toolcall.BufferingSpec {
	return f.spec
}

func (f *panickingApplicabilityFin) ToolCallBufferingApplies(string, aipapi.ToolDef, []aipapi.ToolDef) bool {
	panic("applicability must be isolated")
}

func (f *panickingApplicabilityFin) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

// mutatingApplicabilityFin proves the applicability inputs are detached: it
// writes through both the tool definition and the catalog it was handed, then
// answers false. The assembler's own catalog must be unchanged afterwards.
type mutatingApplicabilityFin struct {
	id    string
	order int
	spec  toolcall.BufferingSpec
}

func (f *mutatingApplicabilityFin) ID() string { return f.id }
func (f *mutatingApplicabilityFin) Order() int { return f.order }
func (f *mutatingApplicabilityFin) ToolCallBufferingRequirement() toolcall.BufferingSpec {
	return f.spec
}

func (f *mutatingApplicabilityFin) ToolCallBufferingApplies(_ string, tool aipapi.ToolDef, catalog []aipapi.ToolDef) bool {
	if len(tool.Parameters) > 0 {
		tool.Parameters[0] = 'X'
	}
	if len(catalog) > 0 && len(catalog[0].Parameters) > 0 {
		catalog[0].Parameters[0] = 'Y'
	}
	return false
}

func (f *mutatingApplicabilityFin) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

func TestToolCallAssembler_ApplicabilityIsIsolatedFromExtensionFailures(t *testing.T) {
	t.Parallel()

	spec := toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}
	args := `{"path":"/home/dev/elsewhere/src/main.go"}`

	t.Run("panicking_applicability_does_not_escape_and_fails_closed", func(t *testing.T) {
		t.Parallel()
		fin := &panickingApplicabilityFin{id: "panicking-applicability", order: 20, spec: spec}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, scopedCatalog())
		if a == nil {
			t.Fatal("assembler must be constructed")
		}
		released, err := streamToolCallNamed(t, a, "applicability-panic", mandatoryToolName, args)
		if err != nil {
			t.Fatalf("an isolated applicability panic must not surface as a Go error: %v", err)
		}
		if released != args {
			t.Fatalf("released %d bytes, want the %d original bytes", len(released), len(args))
		}
	})

	t.Run("mutating_applicability_cannot_reach_the_assembler_catalog", func(t *testing.T) {
		t.Parallel()
		fin := &mutatingApplicabilityFin{id: "mutating-applicability", order: 20, spec: spec}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, scopedCatalog())
		if a == nil {
			t.Fatal("assembler must be constructed")
		}
		if _, err := streamToolCallNamed(t, a, "applicability-mutation", unselectedToolName, args); err != nil {
			t.Fatalf("a detached false answer must not fail the call: %v", err)
		}
		for _, entry := range a.catalog {
			if string(entry.Parameters) != `{"type":"object"}` {
				t.Fatalf("assembler catalog mutated: %q", entry.Parameters)
			}
		}
	})
}

// metaMutatingFin proves finalizer metadata is per-invocation state: it
// rewrites the reference-typed views it was handed, so a second finalizer
// that observed the same values would prove the sharing.
type metaMutatingFin struct {
	order int
}

func (f *metaMutatingFin) ID() string { return "meta-mutating-ordinary" }
func (f *metaMutatingFin) Order() int { return f.order }
func (f *metaMutatingFin) Finalize(_ context.Context, _ toolcall.CompletedCall, _ aipapi.ToolDef, _ []aipapi.ToolDef, meta toolcall.Meta) (toolcall.Result, error) {
	if len(meta.Scope.Roles) > 0 {
		meta.Scope.Roles[0] = "forged-role"
	}
	meta.Scope.SafeClaims["mutated"] = "true"
	meta.Session.Labels["mutated"] = "true"
	if len(meta.Workspace.Markers) > 0 {
		meta.Workspace.Markers[0] = "forged-marker"
	}
	meta.Workspace.Labels["mutated"] = "true"
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

// metaObservingFin records exactly the metadata values it was handed, so the
// test can prove they are the authoritative ones rather than a sibling's
// mutations.
type metaObservingFin struct {
	order    int
	roles    []string
	claims   map[string]string
	labels   map[string]string
	markers  []string
	wsLabels map[string]string
}

func (f *metaObservingFin) ID() string { return "meta-observing-ordinary" }
func (f *metaObservingFin) Order() int { return f.order }
func (f *metaObservingFin) Finalize(_ context.Context, _ toolcall.CompletedCall, _ aipapi.ToolDef, _ []aipapi.ToolDef, meta toolcall.Meta) (toolcall.Result, error) {
	f.roles = append([]string(nil), meta.Scope.Roles...)
	f.claims = maps.Clone(meta.Scope.SafeClaims)
	f.labels = maps.Clone(meta.Session.Labels)
	f.markers = append([]string(nil), meta.Workspace.Markers...)
	f.wsLabels = maps.Clone(meta.Workspace.Labels)
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

// TestToolCallAssembler_FinalizerMetadataIsDetachedPerInvocation proves one
// finalizer cannot reach the next finalizer's decision through shared
// metadata, and cannot reach the producer snapshot either.
//
// toolcall.Meta travels by value, but its Scope, Session, and Workspace views
// carry maps and slices by reference. Handing one value to every finalizer on
// the chain shares those references across the chain: a probe mutating
// Meta.Scope.Roles[0] in the first finalizer made the second observe the
// mutation instead of the authoritative role.
func TestToolCallAssembler_FinalizerMetadataIsDetachedPerInvocation(t *testing.T) {
	t.Parallel()

	catalog := scopedCatalog()
	mutator := &metaMutatingFin{order: 10}
	observer := &metaObservingFin{order: 11}
	a := newToolCallAssembler([]toolcall.Finalizer{mutator, observer}, 0, catalog)
	if a == nil {
		t.Fatal("assembler must be constructed")
	}
	meta := toolcall.Meta{
		Scope: scope.PrincipalScopeView{
			Roles:      []string{"authoritative-role"},
			SafeClaims: map[string]string{"stable": "true"},
		},
		Session: session.SessionView{
			Labels: map[string]string{"stable": "true"},
		},
		Workspace: workspace.WorkspaceView{
			ProjectRoot: "/home/dev/workspaces/aip-path-virtualization-worktree",
			Markers:     []string{"authoritative-marker"},
			Labels:      map[string]string{"stable": "true"},
		},
	}
	ctx := context.Background()
	id := "meta-isolation"
	if held, err := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: unselectedToolName,
	}, meta, nil); err != nil || !held {
		t.Fatalf("started: held=%v err=%v", held, err)
	}
	if held, err := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, ToolName: unselectedToolName, Delta: `{}`,
	}, meta, nil); err != nil || !held {
		t.Fatalf("delta: held=%v err=%v", held, err)
	}
	if _, err := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallFinished, ToolCallID: id, ToolName: unselectedToolName,
	}, meta, nil); err != nil {
		t.Fatalf("finished: %v", err)
	}
	for {
		if _, ok := a.popDrain(); !ok {
			break
		}
	}
	if len(observer.roles) != 1 || observer.roles[0] != "authoritative-role" {
		t.Fatalf("second finalizer observed roles %q, want the authoritative role", observer.roles)
	}
	if observer.claims["mutated"] != "" || observer.claims["stable"] != "true" {
		t.Fatalf("second finalizer observed claims %q, want the authoritative claims", observer.claims)
	}
	if observer.labels["mutated"] != "" || observer.labels["stable"] != "true" {
		t.Fatalf("second finalizer observed session labels %q, want the authoritative labels", observer.labels)
	}
	if len(observer.markers) != 1 || observer.markers[0] != "authoritative-marker" {
		t.Fatalf("second finalizer observed markers %q, want the authoritative markers", observer.markers)
	}
	if observer.wsLabels["mutated"] != "" || observer.wsLabels["stable"] != "true" {
		t.Fatalf("second finalizer observed workspace labels %q, want the authoritative labels", observer.wsLabels)
	}
	if meta.Scope.Roles[0] != "authoritative-role" || meta.Scope.SafeClaims["mutated"] != "" ||
		meta.Session.Labels["mutated"] != "" || meta.Workspace.Markers[0] != "authoritative-marker" ||
		meta.Workspace.Labels["mutated"] != "" {
		t.Fatal("the producer snapshot was mutated through a finalizer's metadata")
	}
}

// TestToolCallAssembler_RefusingCallsCountAsUnresolved proves an overflow
// refusal that has not reached its finished event still suppresses automatic
// completion.
//
// When an oversized mandatory-buffered call outgrows its applicable ceiling
// mid-stream, it moves from active to refusing: the buffer is dropped without
// release and the typed refusal waits for the finished event. The old
// hasActiveCalls read only the active set, so a proxy-owned completion drawn
// before that finished event saw no unresolved call and could publish a
// successful answer over the still-pending refusal. A response ending without
// the tool-finished event must not silently discard it either: the finished
// event below is what surfaces the refusal, and until then the call counts.
func TestToolCallAssembler_RefusingCallsCountAsUnresolved(t *testing.T) {
	t.Parallel()

	catalog := scopedCatalog()
	fin := newScopedFin(t, "scoped-expansion", 20, toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}, mandatoryToolName)
	a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
	if a == nil {
		t.Fatal("assembler must be constructed")
	}

	// Past the declarer's own 1 MiB bound, so the call is refused mid-stream
	// rather than finalized.
	const oversize = toolcall.DefaultMandatoryMaxArgsBytes + 8*1024
	args := mandatoryArgsJSON(oversize)
	if len(args) != oversize {
		t.Fatalf("fixture sizing: got %d want %d", len(args), oversize)
	}

	ctx := context.Background()
	meta := toolcall.Meta{}
	id := "refusing-unresolved"
	if held, err := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: mandatoryToolName,
	}, meta, nil); err != nil || !held {
		t.Fatalf("started: held=%v err=%v", held, err)
	}
	if !a.hasActiveCalls() {
		t.Fatal("a held call must count as unresolved")
	}
	if held, err := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, ToolName: mandatoryToolName, Delta: args,
	}, meta, nil); err != nil || !held {
		t.Fatalf("oversized delta: held=%v err=%v", held, err)
	}
	// The call left active for refusing without releasing a byte. A completion
	// boundary drawn here must still see it as unresolved.
	if !a.hasActiveCalls() {
		t.Fatal("a call with a pending overflow refusal must count as unresolved")
	}
	if _, ok := a.refusing[id]; !ok {
		t.Fatal("fixture: the oversized call must sit in the refusing set")
	}

	// The finished event surfaces the refusal instead of discarding it, and
	// only then is the call resolved.
	_, err := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallFinished, ToolCallID: id, ToolName: mandatoryToolName,
	}, meta, nil)
	var mbe *MandatoryBufferingError
	if !errors.As(err, &mbe) || mbe == nil {
		t.Fatalf("the finished event must surface the pending refusal, got %T (%v)", err, err)
	}
	if mbe.Reason != ReasonMandatoryBufferingOverflow {
		t.Fatalf("reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingOverflow)
	}
	if a.hasActiveCalls() {
		t.Fatal("a refused-at-finish call must no longer count as unresolved")
	}
}

// TestToolCallAssembler_ReleasedLegacyOverflowIsNotUnresolved is the control
// the refusal test above must not break: a legacy pass-through overflow
// releases its fragments immediately, so nothing is pending and the call
// correctly counts as resolved.
func TestToolCallAssembler_ReleasedLegacyOverflowIsNotUnresolved(t *testing.T) {
	t.Parallel()

	catalog := scopedCatalog()
	fin := newScopedFin(t, "scoped-expansion", 20, toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}, mandatoryToolName)
	a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
	if a == nil {
		t.Fatal("assembler must be constructed")
	}
	const oversize = toolcall.DefaultMandatoryMaxArgsBytes + 8*1024
	args := mandatoryArgsJSON(oversize)

	ctx := context.Background()
	meta := toolcall.Meta{}
	id := "legacy-released"
	if held, err := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: unselectedToolName,
	}, meta, nil); err != nil || !held {
		t.Fatalf("started: held=%v err=%v", held, err)
	}
	// Past the shared legacy bound with no applicable requirement: the
	// fragments enqueue onto the drain immediately and the call leaves active
	// for pass-through, not for refusing. held stays true because the event
	// was consumed into the replay, not passed on.
	if held, err := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, ToolName: unselectedToolName, Delta: args,
	}, meta, nil); err != nil || !held {
		t.Fatalf("legacy overflow must be consumed into the replay: held=%v err=%v", held, err)
	}
	if a.hasActiveCalls() {
		t.Fatal("a released legacy call must not count as unresolved")
	}
	if _, ok := a.refusing[id]; ok {
		t.Fatal("a legacy call must never enter the refusing set")
	}
}
