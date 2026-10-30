package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Spec: b-leg-path-virtualization Task 7.3, the feature-integration half of the
// tool-call-repair <-> path-expansion composition. Requirements 8.4 and 8.5 and
// design.md sections "Existing Architecture and Placement" (step 11 response
// tool-call assembly/finalization, before step 12 tool policies/reactors),
// "6. Mandatory Buffering / Completeness Contract" ("do not globally raise
// tool-call-repair's own repair budget"), "7. Path Expansion Finalizer" (step 4
// parse completed ArgsJSON; step 9 validate rewritten JSON; step 10 assembler
// synthesizes the canonical rewritten lifecycle), and "Testing Strategy".
//
// WHAT THIS FILE OWNS. requirements.md 8.4 has two clauses: mandatory path
// expansion "shall receive valid completed JSON" and "shall not be bypassed by
// tool-call-repair size policy". The first is an ORDERING contract, the second a
// BOUNDING contract.
//
// The ordering contract is realized entirely by Finalizer.Order(). There is no
// core rule that reorders finalizers, and adding one was explicitly rejected in
// review of Task 7.2: it overrode the authoritative numeric Order() contract in
// pkg/aipsdk/toolcall/sort.go, had no present value, and closed nothing, because
// finalizeCall's err/invalid-envelope/unknown-action fallbacks all replay
// buf.originals. finalizeCall therefore iterates a.finalizers exactly as
// toolcall.MaterializeSorted produced it. This file is the missing evidence for
// that hand-off: it proves the composition is reachable and correct using only
// order numbers, so Task 8.1 knows precisely what its finalizer must declare.
//
// The expansion finalizer Task 8.1 will ship does not exist yet, so it is
// simulated by the existing test stand-in mandatoryExpansionFin, which already
// publishes the REAL shipped toolcall.BufferingRequirement capability. Every
// other participant is the real shipped tool-call-repair finalizer from
// internal/plugins/features/toolcallrepair, at its production order and with its
// production 64 KiB repair budget. No production file changes here.
//
// NOT RE-COVERED HERE. Task 7.2 already owns the assembler-level repair-budget
// behavior with test doubles: TestToolCallAssembler_RaisedMandatoryBoundDoesNotRaiseRepairBudget
// (both subtests) and TestToolCallAssembler_UnrelatedFinalizerFailureCannotSkipMandatoryExpansion.
// The tests below are the composition angles those cannot reach: repair is a real
// shipped finalizer whose decisions are observed, and expansion is declared
// ABOVE repair rather than at its zero-value natural order.

// composedRepairProbe wraps the REAL shipped tool-call-repair finalizer so a
// composition test can observe exactly what repair decided without changing any
// of its behavior: ID() and Order() are promoted from the embedded production
// finalizer, so toolcall.MaterializeSorted sorts on the real production order
// value and Finalize is the real production repair. MaxArgsBytes is left at zero
// so repair.NewFinalizer applies its own shipped default repair budget; nothing
// here widens it.
type composedRepairProbe struct {
	*repair.Finalizer

	calls   int
	seen    []byte
	results []toolcall.Result
	lastErr error
}

func (p *composedRepairProbe) Finalize(
	ctx context.Context,
	call toolcall.CompletedCall,
	tool aipapi.ToolDef,
	catalog []aipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	res, err := p.Finalizer.Finalize(ctx, call, tool, catalog, meta)
	p.calls++
	p.seen = append(p.seen[:0], call.ArgsJSON...)
	p.results = append(p.results, res)
	p.lastErr = err
	return res, err
}

var _ toolcall.Finalizer = (*composedRepairProbe)(nil)

// newComposedRepairProbe builds the production repair finalizer at the given
// order and asserts the probe is observationally transparent: it must not
// accidentally publish a buffering requirement, because the real repair feature
// keeps its own size policy and never declares a mandatory completeness bound
// (design.md "6. Mandatory Buffering / Completeness Contract": do not globally
// raise tool-call-repair's own repair budget).
func newComposedRepairProbe(t *testing.T, order int) *composedRepairProbe {
	t.Helper()
	probe := &composedRepairProbe{Finalizer: repair.NewFinalizer(repair.FinalizerPolicy{
		ID:    repair.DefaultFinalizerID,
		Order: order,
	})}
	if _, ok := toolcall.Finalizer(probe.Finalizer).(toolcall.BufferingRequirement); ok {
		t.Fatal("the shipped tool-call-repair finalizer must not declare a mandatory buffering requirement")
	}
	if _, ok := toolcall.Finalizer(probe).(toolcall.BufferingRequirement); ok {
		t.Fatal("the probe must not change whether tool-call-repair declares a buffering requirement")
	}
	return probe
}

// onlyResult returns the single decision the real repair finalizer made.
func (p *composedRepairProbe) onlyResult(t *testing.T) toolcall.Result {
	t.Helper()
	if len(p.results) != 1 {
		t.Fatalf("tool-call-repair decisions=%d want exactly 1", len(p.results))
	}
	return p.results[0]
}

// newExpansionAboveRepairFin is the stand-in for the path-expansion finalizer
// Task 8.1 will ship: the same mandatory-declaring finalizer the assembler tests
// already use, declared at the first order strictly above the shipped
// tool-call-repair order. That single number is the whole mechanism.
func newExpansionAboveRepairFin() *mandatoryExpansionFin {
	fin := newMandatoryExpansionFin()
	fin.order = repair.DefaultFinalizerOrder + 1
	return fin
}

// composedExpansionProbe wraps the REAL shipped path-virtualization expansion
// finalizer so a composition test can observe exactly what it decided without changing
// any of its behavior. ID(), Order(), and ToolCallBufferingRequirement() are promoted
// from the embedded production finalizer, so toolcall.MaterializeSorted sorts on the
// real production order value and the assembler reads the real production mandatory
// declaration. Only Finalize is intercepted, and only to record.
//
// Task 8.1 shipped that finalizer, so the subtest below now drives it instead of a
// stand-in. That is a strengthening, not a convenience: the failure mode this subtest
// exists to forbid - releasing a malformed, alias-bearing argument document - is
// decided by the production code, so it has to be asserted against the production code.
type composedExpansionProbe struct {
	*expansion.Finalizer

	calls  int
	seen   []byte
	result toolcall.Result
}

func (p *composedExpansionProbe) Finalize(
	ctx context.Context,
	call toolcall.CompletedCall,
	tool aipapi.ToolDef,
	catalog []aipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	res, err := p.Finalizer.Finalize(ctx, call, tool, catalog, meta)
	p.calls++
	p.seen = append(p.seen[:0], call.ArgsJSON...)
	p.result = res
	return res, err
}

var _ toolcall.Finalizer = (*composedExpansionProbe)(nil)

// compositionProjectRoot is the authoritative workspace root the shipped expansion
// finalizer derives its mapping from. It is only ever read from the finalizer metadata
// the runtime hands every finalizer, which is what Task 6.2 populated; the fixture's
// own root constant stays the client-visible spelling the tests assert on.
const compositionProjectRoot = mandatoryRealRoot

// newComposedExpansionProbe builds the REAL shipped expansion finalizer at its own
// production order, with an operator profile that claims the fixture's exact tool name
// and the fixture's own argument key, so the composition observes the production
// selector path rather than a stub's unconditional string replacement.
//
// The profile is REQUIRED rather than optional: the shipped pass selects nothing for a
// tool no layer claims, so without one this probe would correctly pass every call
// through and the subtest below would prove nothing about it.
func newComposedExpansionProbe(t *testing.T) *composedExpansionProbe {
	t.Helper()
	compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
		Names:       []string{mandatoryToolName},
		ArgPointers: []string{mandatoryFixturePathPointer},
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compile the fixture profile: reject %v", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("new resolver: reject %v", reject)
	}
	fin, err := expansion.NewFinalizer(resolver, rewrite.ModeRewrite, expansion.Policy{})
	if err != nil {
		t.Fatalf("build the shipped expansion finalizer: %v", err)
	}
	if fin.Order() != expansion.FinalizerOrder {
		t.Fatalf("shipped expansion order changed: got %d want %d", fin.Order(), expansion.FinalizerOrder)
	}
	if fin.Order() <= repair.DefaultFinalizerOrder {
		t.Fatalf("requirements.md 8.4 - the shipped expansion order must sit strictly above repair: %d <= %d",
			fin.Order(), repair.DefaultFinalizerOrder)
	}
	probe := &composedExpansionProbe{Finalizer: fin}
	var declared toolcall.Finalizer = probe
	if _, ok := declared.(toolcall.BufferingRequirement); !ok {
		t.Fatal("the shipped expansion finalizer must publish a mandatory buffering requirement")
	}
	return probe
}

// compositionMeta builds the finalizer metadata the runtime hands every finalizer of
// this turn, with the authoritative workspace view Task 6.2 populated.
func compositionMeta() toolcall.Meta {
	return toolcall.Meta{Workspace: workspace.WorkspaceView{ProjectRoot: compositionProjectRoot}}
}

// The fixture's argument member name and the JSON Pointer that names it. The shared
// fixture builders spell the member inline, so these two constants are what let this
// file build a profile and a plain-path document that agree with those fixtures.
const (
	mandatoryFixturePathMember  = "path"
	mandatoryFixturePathPointer = "/" + mandatoryFixturePathMember
)

// streamMandatoryToolCallWithMeta drives one completed tool call through the assembler
// with an explicit finalizer metadata value, and returns the argument deltas the client
// would observe.
//
// It is the same shape as the shared streamMandatoryToolCall, which is deliberately
// left untouched: that helper predates the authoritative finalizer metadata and hardcodes
// the zero value, and changing it would alter Task 7.2's shipped evidence for every
// other test in that file. Duplicating thirty lines here keeps that boundary honest and
// lets this composition supply the one thing its subject needs - a project root for the
// shipped expansion finalizer to derive a mapping from.
func streamMandatoryToolCallWithMeta(
	t *testing.T,
	a *toolCallAssembler,
	id, argsJSON string,
	meta toolcall.Meta,
) (string, error) {
	t.Helper()
	ctx := context.Background()
	var released strings.Builder

	if held, ingErr := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: mandatoryToolName,
	}, meta, nil); ingErr != nil || !held {
		t.Fatalf("started: held=%v err=%v", held, ingErr)
	}
	for _, fragment := range splitMandatoryArgsFragments(argsJSON) {
		held, ingErr := a.ingest(ctx, aipapi.Event{
			Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id,
			ToolName: mandatoryToolName, Delta: fragment,
		}, meta, nil)
		if ingErr != nil {
			t.Fatalf("args delta: %v", ingErr)
		}
		if !held {
			released.WriteString(fragment)
		}
	}
	_, err := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallFinished, ToolCallID: id, ToolName: mandatoryToolName,
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

// TestToolCallRepairRunsBeforeMandatoryExpansionByOrderAlone is the hand-off
// evidence Task 8.1 needs. It proves, with zero core change, that declaring a
// FinalizerOrder above toolcallrepair.DefaultFinalizerOrder puts syntax repair
// ahead of mandatory path expansion, and that the expansion finalizer therefore
// observes completed VALID JSON (requirements.md 8.4 first clause, design.md
// section 7 step 4).
func TestToolCallRepairRunsBeforeMandatoryExpansionByOrderAlone(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()

	t.Run("materialize_sorted_orders_repair_before_expansion_from_order_alone", func(t *testing.T) {
		t.Parallel()
		expansion := newExpansionAboveRepairFin()
		repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)

		if repairFin.Order() != repair.DefaultFinalizerOrder {
			t.Fatalf("shipped repair order changed: got %d want %d", repairFin.Order(), repair.DefaultFinalizerOrder)
		}
		if expansion.Order() <= repairFin.Order() {
			t.Fatalf("the expansion declaration must sit strictly above the repair order: expansion=%d repair=%d",
				expansion.Order(), repairFin.Order())
		}

		// Registered in the OPPOSITE order on purpose, so only the Order()
		// contract can produce the composition.
		sorted := toolcall.MaterializeSorted([]toolcall.Finalizer{expansion, repairFin})
		if len(sorted) != 2 {
			t.Fatalf("materialized finalizers=%d want 2", len(sorted))
		}
		if sorted[0].ID() != repair.DefaultFinalizerID {
			t.Fatalf("sorted[0]=%q want the repair finalizer", sorted[0].ID())
		}
		if sorted[1].ID() != expansion.ID() {
			t.Fatalf("sorted[1]=%q want the expansion finalizer", sorted[1].ID())
		}

		// The same must hold through the assembler's own construction path,
		// which is the only ordering decision the runtime makes.
		a := newToolCallAssembler([]toolcall.Finalizer{expansion, repairFin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if len(a.finalizers) != 2 {
			t.Fatalf("assembler finalizers=%d want 2", len(a.finalizers))
		}
		if a.finalizers[0].ID() != repair.DefaultFinalizerID || a.finalizers[1].ID() != expansion.ID() {
			t.Fatalf("assembler iteration order changed: [%s %s]",
				a.finalizers[0].ID(), a.finalizers[1].ID())
		}
	})

	t.Run("malformed_json_is_repaired_before_expansion_observes_it", func(t *testing.T) {
		t.Parallel()
		expansion := newExpansionAboveRepairFin()
		repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
		a := newToolCallAssembler([]toolcall.Finalizer{expansion, repairFin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}

		// A legacy PartJSON tool call: the model emitted malformed JSON whose
		// closing quote and brace never arrived, delivered as stream deltas cut
		// through the reserved alias so no single fragment is valid JSON.
		args := mandatoryMalformedArgsJSON(4 * 1024)
		repairedDocument := mandatoryArgsJSON(4 * 1024)
		if json.Valid([]byte(args)) {
			t.Fatal("fixture must be malformed JSON")
		}
		if len(args) >= repair.DefaultMaxArgsBytes {
			t.Fatalf("fixture must stay inside the repair budget: %d >= %d", len(args), repair.DefaultMaxArgsBytes)
		}
		if len(args) > a.mandatory.assemblyMaxArgsBytes {
			t.Fatalf("fixture must stay inside the declared mandatory bound: %d > %d",
				len(args), a.mandatory.assemblyMaxArgsBytes)
		}

		released, err := streamMandatoryToolCall(t, a, "repair-then-expand", args)
		if err != nil {
			t.Fatalf("the composition must not fail the call: %v", err)
		}

		// The shipped repair finalizer really ran first and really decided.
		if repairFin.calls != 1 {
			t.Fatalf("tool-call-repair invocations=%d want 1", repairFin.calls)
		}
		if repairFin.lastErr != nil {
			t.Fatalf("the shipped repair finalizer must not surface a Go error to the assembler")
		}
		if !bytes.Equal(repairFin.seen, []byte(args)) {
			t.Fatalf("repair must observe the malformed document: saw %d bytes want %d",
				len(repairFin.seen), len(args))
		}
		decision := repairFin.onlyResult(t)
		if decision.Action != toolcall.ActionRewrite || decision.ReasonCode != toolcall.ReasonSyntaxRepaired {
			t.Fatalf("repair decision action=%v reason=%q want rewrite / syntax_repaired",
				decision.Action, decision.ReasonCode)
		}
		if !json.Valid(decision.ArgsJSON) {
			t.Fatalf("repair must emit valid JSON: action=%v reason=%q", decision.Action, decision.ReasonCode)
		}

		// Requirement 8.4 first clause: the expansion finalizer receives the
		// REPAIRED document, not the malformed one.
		if expansion.calls != 1 {
			t.Fatalf("expansion invocations=%d want 1", expansion.calls)
		}
		if len(expansion.seen) != len(repairedDocument) {
			t.Fatalf("expansion must receive the completed repaired document: saw %d bytes want %d",
				len(expansion.seen), len(repairedDocument))
		}
		if !json.Valid(expansion.seen) {
			t.Fatal("expansion received invalid JSON: repair did not run first")
		}
		if !bytes.Equal(expansion.seen, []byte(repairedDocument)) {
			t.Fatalf("expansion must observe the repaired bytes, not the original fragments: saw %d valid bytes",
				len(expansion.seen))
		}

		// Requirement 8.5 plus design.md section 7 steps 9/10: the released
		// lifecycle is the assembler's synthesized rewrite of the validated
		// expanded document, with no reserved alias left in it.
		want := mandatoryExpandedArgsJSON(4 * 1024)
		if len(released) != len(want) {
			t.Fatalf("released %d bytes want %d", len(released), len(want))
		}
		if !json.Valid([]byte(released)) {
			t.Fatal("the released argument document must be valid JSON")
		}
		if strings.Contains(released, mandatoryVirtualRoot) {
			t.Fatal("the reserved virtual alias reached the client unexpanded")
		}
		if !bytes.Equal([]byte(released), []byte(want)) {
			t.Fatal("released arguments are not the expanded repaired document")
		}
	})

	t.Run("expansion_declared_below_the_repair_order_sees_the_unrepaired_document", func(t *testing.T) {
		t.Parallel()
		// The negative control: the identical fixture and the identical two
		// shipped/stand-in participants, differing ONLY in the declared order.
		// It proves the observation above is genuinely order-driven, so it would
		// fail if finalizeCall stopped applying a repair rewrite or if the sort
		// contract were inverted.
		expansion := newMandatoryExpansionFin()
		expansion.order = repair.DefaultFinalizerOrder - 1
		repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
		a := newToolCallAssembler([]toolcall.Finalizer{expansion, repairFin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}

		args := mandatoryMalformedArgsJSON(4 * 1024)
		released, err := streamMandatoryToolCall(t, a, "expand-then-repair", args)
		if err != nil {
			t.Fatalf("the composition must not fail the call: %v", err)
		}
		if expansion.calls != 1 {
			t.Fatalf("expansion invocations=%d want 1", expansion.calls)
		}
		if json.Valid(expansion.seen) {
			t.Fatal("with expansion ordered first it must observe the malformed document, not a repaired one")
		}
		if !bytes.Equal(expansion.seen, []byte(args)) {
			t.Fatalf("expansion must observe the original fragments: saw %d bytes want %d",
				len(expansion.seen), len(args))
		}
		// Repair still ran and still produced valid JSON, so the document the
		// client receives is syntactically repaired but never expanded: the
		// reserved alias is still there. That is the composition difference the
		// order declaration buys, stated in the negative.
		if repairFin.calls != 1 {
			t.Fatalf("tool-call-repair invocations=%d want 1", repairFin.calls)
		}
		if !json.Valid([]byte(released)) {
			t.Fatal("repair must still repair the document when it runs second")
		}
		if !strings.Contains(released, mandatoryVirtualRoot) {
			t.Fatal("negative control: with expansion ordered first the alias must remain unexpanded")
		}
		if strings.Contains(released, mandatoryRealRoot) {
			t.Fatal("negative control: expansion must not have run at all in this ordering")
		}
	})
}

// TestToolCallRepairSizePolicyCannotSkipMandatoryExpansion is requirements.md
// 8.4's second clause: tool-call-repair's own size policy may decline a large
// call, but that decline must never skip mandatory expansion. The repair budget
// is a repair budget, not the assembler's assembly bound
// (design.md "6. Mandatory Buffering / Completeness Contract").
//
// The boundary probe below is what pins the budget as unchanged BEHAVIOR: at the
// exact repair budget the shipped finalizer still decides, one byte past it the
// shipped finalizer declines with args_too_large, and both sit far below the
// effective assembly bound the mandatory declaration raised. That is stronger
// than re-asserting the constant, and it complements rather than repeats Task
// 7.2's assembler-level constant equality subtest.
func TestToolCallRepairSizePolicyCannotSkipMandatoryExpansion(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()

	newComposition := func(t *testing.T) (*toolCallAssembler, *composedRepairProbe, *mandatoryExpansionFin) {
		t.Helper()
		expansion := newExpansionAboveRepairFin()
		repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
		a := newToolCallAssembler([]toolcall.Finalizer{expansion, repairFin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		// The declaration raises the effective assembly bound only; the shared
		// legacy bound keeps its pre-existing value and no finalizer's own size
		// policy is read or widened here.
		if a.maxArgsBytes != defaultToolCallFinalizationMaxArgsBytes {
			t.Fatalf("shared legacy bound changed: got %d want %d", a.maxArgsBytes, defaultToolCallFinalizationMaxArgsBytes)
		}
		if a.mandatory.assemblyMaxArgsBytes != toolcall.DefaultMandatoryMaxArgsBytes {
			t.Fatalf("effective assembly bound: got %d want the declared %d",
				a.mandatory.assemblyMaxArgsBytes, toolcall.DefaultMandatoryMaxArgsBytes)
		}
		if a.mandatory.assemblyMaxArgsBytes <= repair.DefaultMaxArgsBytes {
			t.Fatalf("the effective assembly bound must stay above the repair budget: %d <= %d",
				a.mandatory.assemblyMaxArgsBytes, repair.DefaultMaxArgsBytes)
		}
		return a, repairFin, expansion
	}

	t.Run("repair_declines_past_its_own_budget_and_expansion_still_runs", func(t *testing.T) {
		t.Parallel()
		// The REAL shipped expansion finalizer, not the stand-in. Task 7.3 could not
		// use it; Task 8.1 shipped it, and this subtest's subject is now decided by
		// production code.
		expansionFin := newComposedExpansionProbe(t)
		repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
		a := newToolCallAssembler([]toolcall.Finalizer{expansionFin, repairFin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		// The shipped declaration raises the effective assembly bound only; the
		// shared legacy bound keeps its pre-existing value and no finalizer's own
		// size policy is read or widened here.
		if a.maxArgsBytes != defaultToolCallFinalizationMaxArgsBytes {
			t.Fatalf("shared legacy bound changed: got %d want %d", a.maxArgsBytes, defaultToolCallFinalizationMaxArgsBytes)
		}
		if a.mandatory.assemblyMaxArgsBytes != toolcall.DefaultMandatoryMaxArgsBytes {
			t.Fatalf("effective assembly bound: got %d want the declared %d",
				a.mandatory.assemblyMaxArgsBytes, toolcall.DefaultMandatoryMaxArgsBytes)
		}
		if a.mandatory.assemblyMaxArgsBytes <= repair.DefaultMaxArgsBytes {
			t.Fatalf("the effective assembly bound must stay above the repair budget: %d <= %d",
				a.mandatory.assemblyMaxArgsBytes, repair.DefaultMaxArgsBytes)
		}

		// Malformed and past the shipped repair budget, so the shipped repair
		// finalizer declines: this is the one shape where syntax repair does NOT
		// happen, and mandatory expansion must still be reached.
		args := mandatoryMalformedArgsJSON(repair.DefaultMaxArgsBytes + 8*1024)
		if len(args) <= repair.DefaultMaxArgsBytes {
			t.Fatalf("fixture must exceed the repair budget: %d <= %d", len(args), repair.DefaultMaxArgsBytes)
		}
		if len(args) > a.mandatory.assemblyMaxArgsBytes {
			t.Fatalf("fixture must stay inside the declared mandatory bound: %d > %d",
				len(args), a.mandatory.assemblyMaxArgsBytes)
		}

		released, err := streamMandatoryToolCallWithMeta(t, a, "repair-declines", args, compositionMeta())

		// The decline is observable and is a decline, not a silent pass-through
		// of an uninspected document.
		if repairFin.calls != 1 {
			t.Fatalf("tool-call-repair invocations=%d want 1", repairFin.calls)
		}
		decision := repairFin.onlyResult(t)
		if decision.Action != toolcall.ActionPass || decision.ReasonCode != toolcall.ReasonArgsTooLarge {
			t.Fatalf("repair must decline past its own budget: action=%v reason=%q",
				decision.Action, decision.ReasonCode)
		}

		// Requirement 8.4 second clause: expansion was NOT skipped. The
		// declaring finalizer ran exactly once on the COMPLETE document, which
		// is only reachable because the assembler buffers past the repair
		// budget up to the declared mandatory bound.
		if expansionFin.calls != 1 {
			t.Fatalf("requirements.md 8.4 - repair's size policy bypassed mandatory expansion: invocations=%d",
				expansionFin.calls)
		}
		if len(expansionFin.seen) != len(args) {
			t.Fatalf("requirements.md 8.4 - expansion must receive the complete document: saw %d bytes want %d",
				len(expansionFin.seen), len(args))
		}
		if !bytes.Equal(expansionFin.seen, []byte(args)) {
			t.Fatal("requirements.md 8.4 - expansion received a different document than the model emitted")
		}

		// REQUIREMENT FIX, Task 8.1. This assertion previously read `released ==
		// args`, which pinned the OPPOSITE of what requirements.md 4.4 and 8.3
		// require, because the old stand-in passed on invalid JSON. design.md
		// "Error Handling" is explicit: "Malformed model JSON: existing
		// repair/finalizer policy may repair first; a recognized applicable alias
		// must never bypass required expansion and reach the client." The shipped
		// finalizer therefore REFUSES the call closed, and the change from
		// `released == args` to "nothing released, bounded refusal" is a
		// strengthening: it removes an assertion that permitted an alias to reach
		// the client and replaces it with one that forbids it.
		//
		// Note that the repair budget is still proven unchanged by the decision the
		// shipped repair finalizer recorded above. The refusal comes from the
		// expansion finalizer, and the bounded reason below says so.
		if released != "" {
			t.Fatalf("requirements.md 4.4/8.3 - %d argument bytes were released on a refused call "+
				"(alias_present=%t); the reserved alias must never bypass required expansion",
				len(released), strings.Contains(released, mandatoryVirtualRoot))
		}
		var reject *toolcall.RejectError
		if !errors.As(err, &reject) || reject == nil {
			t.Fatalf("requirements.md 4.4/8.3 - want a bounded refusal, got %v", err)
		}
		if got, ok := expansion.ParseReason(reject.ReasonCode); !ok || got != expansion.ReasonArgsUnparseable {
			t.Fatalf("refusal reason=%q want %q", reject.ReasonCode, expansion.ReasonArgsUnparseable)
		}
		if expansionFin.result.Action != toolcall.ActionReject {
			t.Fatalf("the shipped expansion finalizer must reject, got action %d", int(expansionFin.result.Action))
		}

		// POSITIVE CONTROLS, so the refusal above cannot be vacuous. The same live
		// finalizer, over the same assembler and the same metadata, reaches two
		// other bounded verdicts on this exact fixture family. Each control has a
		// DIFFERENT reason, so the three assertions together prove the production
		// code is deciding rather than blanket-refusing.
		t.Run("control_the_same_finalizer_expands_a_parseable_alias_bearing_document", func(t *testing.T) {
			t.Parallel()
			fin := newComposedExpansionProbe(t)
			repairProbe := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
			asm := newToolCallAssembler([]toolcall.Finalizer{fin, repairProbe}, 0, catalog)

			// Repair is inside its budget for this document, so it hands the
			// expansion finalizer a VALID alias-bearing one. The fixture's alias
			// tag is not 20 characters of the frozen base32 alphabet, so the
			// shipped pass refuses it as a MALFORMED reserved alias - a different
			// bounded reason from the unparseable-document one above, reached
			// through the parseable path this subtest's sibling already covers.
			valid := mandatoryArgsJSON(4 * 1024)
			got, gotErr := streamMandatoryToolCallWithMeta(t, asm, "control-parseable", valid, compositionMeta())
			if got != "" {
				t.Fatalf("a refused call must release nothing, got %d bytes", len(got))
			}
			if reason, ok := expansion.ParseReason(fin.result.ReasonCode); !ok ||
				reason != expansion.ReasonMalformedReservedAlias {
				t.Fatalf("control reason=%q want %q", fin.result.ReasonCode, expansion.ReasonMalformedReservedAlias)
			}
			if gotErr == nil {
				t.Fatal("a refused call must raise an error the runtime can classify")
			}
		})

		t.Run("control_a_document_with_no_reserved_namespace_is_released_unchanged", func(t *testing.T) {
			t.Parallel()
			fin := newComposedExpansionProbe(t)
			repairProbe := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
			asm := newToolCallAssembler([]toolcall.Finalizer{fin, repairProbe}, 0, catalog)

			// requirement 4.7: a call carrying no virtual root keeps the
			// pre-existing pass-through behavior, and this proves the refusal above
			// is caused by the reserved namespace rather than by size, by repair, or
			// by the assembler.
			plain := `{"` + mandatoryFixturePathMember + `":"/home/dev/elsewhere/src/main.go","content":"kept"}`
			got, gotErr := streamMandatoryToolCallWithMeta(t, asm, "control-plain", plain, compositionMeta())
			if gotErr != nil {
				t.Fatalf("requirements.md 4.7 - a call with no virtual root must not fail: %v", gotErr)
			}
			if got != plain {
				t.Fatalf("requirements.md 4.7 - released %d bytes want the %d unchanged originals",
					len(got), len(plain))
			}
			if reason, ok := expansion.ParseReason(fin.result.ReasonCode); !ok || reason != expansion.ReasonNoAlias {
				t.Fatalf("control reason=%q want %q", fin.result.ReasonCode, expansion.ReasonNoAlias)
			}
		})
	})

	t.Run("the_effective_assembly_bound_is_not_the_repair_budget", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name       string
			argsBytes  int
			wantReason string
		}{
			{
				name:       "at_the_repair_budget_the_shipped_finalizer_still_decides",
				argsBytes:  repair.DefaultMaxArgsBytes,
				wantReason: toolcall.ReasonValidPassThrough,
			},
			{
				name:       "one_byte_past_the_repair_budget_it_declines",
				argsBytes:  repair.DefaultMaxArgsBytes + 1,
				wantReason: toolcall.ReasonArgsTooLarge,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				a, repairFin, expansion := newComposition(t)

				// A syntactically valid, alias-bearing document sized so the ONLY
				// thing that can decide the call is repair's own size policy.
				args := mandatoryArgsJSON(tc.argsBytes)
				if len(args) != tc.argsBytes {
					t.Fatalf("fixture sizing: got %d bytes want %d", len(args), tc.argsBytes)
				}
				if len(args) > a.mandatory.assemblyMaxArgsBytes {
					t.Fatalf("the probe must never hit the assembler bound: %d > %d",
						len(args), a.mandatory.assemblyMaxArgsBytes)
				}

				released, err := streamMandatoryToolCall(t, a, "repair-budget-"+tc.name, args)
				if err != nil {
					t.Fatalf("a repair size-policy decline must not fail the call: %v", err)
				}
				decision := repairFin.onlyResult(t)
				if decision.ReasonCode != tc.wantReason {
					t.Fatalf("at %d bytes repair reason=%q want %q",
						len(args), decision.ReasonCode, tc.wantReason)
				}
				if decision.Action != toolcall.ActionPass {
					t.Fatalf("at %d bytes repair action=%v want pass", len(args), decision.Action)
				}

				// Either way the declaring finalizer decided on the complete
				// document and its expansion is what reaches the client, so the
				// repair budget governs only whether repair did any work.
				if expansion.calls != 1 || len(expansion.seen) != len(args) {
					t.Fatalf("requirements.md 8.4 - at %d bytes expansion must still run on the complete document: invocations=%d seen=%d",
						len(args), expansion.calls, len(expansion.seen))
				}
				if strings.Contains(released, mandatoryVirtualRoot) {
					t.Fatalf("requirements.md 4.5 - at %d bytes the reserved alias reached the client unexpanded", len(args))
				}
				if !json.Valid([]byte(released)) {
					t.Fatalf("requirements.md 8.5 - at %d bytes the released document is not valid JSON", len(args))
				}
			})
		}
	})
}

// TestToolCallRepairComposesWithInvalidFinalizerRewriteSemantics characterizes
// requirements.md 8.5 for this composition: every mutation a finalizer performs
// is validated before the next finalizer sees it and before anything is
// released. The assembler has exactly three unusable-result fallbacks (a Go
// error, an invalid rewrite envelope, and an unknown action) and they all reach
// one chokepoint, toolCallAssembler.unusableResult. Both of its boundaries are
// characterized here relative to the shipped repair finalizer, which is what makes
// them composition facts rather than assembler-internal ones.
//
// The chokepoint has three answers, and this file pins all of them: an UNDECIDED
// declared requirement refuses closed (the subtest below), a SATISFIED one
// preserves the document the declaring pass produced (the subtest after it), and a
// call with no declared requirement keeps the pre-existing replay unconditionally.
func TestToolCallRepairComposesWithInvalidFinalizerRewriteSemantics(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()

	// unusableShapes are the four ways an ordinary, non-declaring finalizer can
	// return something the assembler cannot use. This finalizer never stands in
	// for a declaring one and never publishes a buffering requirement.
	unusableShapes := []struct {
		name string
		res  toolcall.Result
		err  error
	}{
		{
			name: "invalid_rewrite_envelope_json",
			res: toolcall.Result{
				Action:     toolcall.ActionRewrite,
				ToolName:   mandatoryToolName,
				ArgsJSON:   []byte(`{"path":`),
				ReasonCode: toolcall.ReasonValidPassThrough,
			},
		},
		{
			name: "invalid_rewrite_envelope_missing_tool_name",
			res: toolcall.Result{
				Action:     toolcall.ActionRewrite,
				ArgsJSON:   []byte(`{}`),
				ReasonCode: toolcall.ReasonValidPassThrough,
			},
		},
		{
			name: "invalid_rewrite_envelope_missing_args",
			res: toolcall.Result{
				Action:     toolcall.ActionRewrite,
				ToolName:   mandatoryToolName,
				ReasonCode: toolcall.ReasonValidPassThrough,
			},
		},
		{
			name: "unknown_action",
			res: toolcall.Result{
				Action:     toolcall.Action(999),
				ReasonCode: toolcall.ReasonValidPassThrough,
			},
		},
		{
			name: "finalizer_error",
			err:  context.Canceled,
		},
	}

	const (
		// BeforeRepairOrder is below the shipped tool-call-repair order, so an
		// unusable result there short-circuits the whole composition.
		BeforeRepairOrder = repair.DefaultFinalizerOrder - 30
		// AfterExpansionOrder is above both the repair order and the mandatory
		// declaration, so the declaring finalizer has already run by then.
		AfterExpansionOrder = repair.DefaultFinalizerOrder + 2
	)

	t.Run("before_the_declaring_finalizer_the_call_is_refused_closed", func(t *testing.T) {
		t.Parallel()
		wantMessage := "tool call finalization: tool call assembler: mandatory buffering requirement not honored (" +
			ReasonMandatoryBufferingIncomplete + ")"

		for _, shape := range unusableShapes {
			t.Run(shape.name, func(t *testing.T) {
				t.Parallel()
				var ordinary toolcall.Finalizer = &unusableOrdinaryFin{order: BeforeRepairOrder, res: shape.res}
				if shape.err != nil {
					ordinary = &failingOrdinaryFin{order: BeforeRepairOrder}
				}
				if _, ok := ordinary.(toolcall.BufferingRequirement); ok {
					t.Fatal("fixture must be a finalizer without a declaration")
				}
				expansion := newExpansionAboveRepairFin()
				repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
				a := newToolCallAssembler([]toolcall.Finalizer{ordinary, repairFin, expansion}, 0, catalog)
				if a == nil {
					t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
				}
				if !a.mandatory.mandatoryBoundDeclared {
					t.Fatal("a declaring finalizer must arm the undecided-mandatory chokepoint")
				}

				released, err := streamMandatoryToolCall(t, a, "unusable-"+shape.name, mandatoryArgsJSON(8*1024))

				// Requirements 4.6/8.4/8.5: an unusable result before the
				// declaring finalizer decides refuses the call closed, so no
				// possibly alias-bearing argument is released and the shipped
				// repair finalizer never gets to run on a document another
				// finalizer mangled.
				if released != "" {
					t.Fatalf("%d original argument bytes were replayed", len(released))
				}
				if strings.Contains(released, mandatoryVirtualRoot) {
					t.Fatal("the reserved alias was released without expansion")
				}
				if repairFin.calls != 0 {
					t.Fatalf("tool-call-repair must not run after an unusable earlier result: invocations=%d", repairFin.calls)
				}
				if expansion.calls != 0 {
					t.Fatalf("the declaring finalizer must not have decided: invocations=%d", expansion.calls)
				}
				var mbe *MandatoryBufferingError
				if !errors.As(err, &mbe) || mbe == nil {
					t.Fatalf("want a typed MandatoryBufferingError, got %v", err)
				}
				if !errors.Is(err, ErrMandatoryBuffering) || !IsMandatoryBufferingError(err) {
					t.Fatalf("refusal must classify through the sentinel, got %v", err)
				}
				if mbe.Reason != ReasonMandatoryBufferingIncomplete {
					t.Fatalf("reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingIncomplete)
				}
				if mbe.FinalizerID != expansion.ID() {
					t.Fatalf("refusal must name the declaring finalizer, got %q", mbe.FinalizerID)
				}
				if mbe.MaxArgsBytes != 0 {
					t.Fatalf("no bound was exceeded, so MaxArgsBytes must stay zero: got %d", mbe.MaxArgsBytes)
				}
				if err.Error() != wantMessage {
					t.Fatalf("refusal message must be the bounded, content-free classification")
				}
			})
		}
	})

	t.Run("after_the_declaring_finalizer_ran_the_mandatory_safe_result_is_preserved", func(t *testing.T) {
		t.Parallel()
		// DELIBERATE CHANGE OF AN ACCEPTED CHARACTERIZATION, and the reason is
		// recorded here because the previous assertion was the opposite of this one.
		//
		// This subtest used to be named
		// "residual_after_the_declaring_finalizer_ran_the_replay_fallback_still_applies"
		// and asserted, for all five unusable-result shapes, that the assembler
		// replayed the ORIGINAL fragments so the reserved alias reached the client
		// error-free. It was a faithful characterization of Task 7.2's accepted
		// residual, and the post-certification review later WITHDREW that acceptance:
		// it found the third option the earlier note had not considered, namely
		// PRESERVING the document the declaring finalizer produced instead of choosing
		// between refusing and replaying.
		//
		// So the assertion is now the fixed behaviour. The expansion the declaring pass
		// already performed is what this call releases, the reserved alias never reaches
		// the client, and the later finalizer's own failure still surfaces. The
		// composition fact this file exists for - every mutation a finalizer performs
		// is validated before the next finalizer sees it and before anything is
		// released - is unchanged and is what the "before_the_declaring_finalizer"
		// subtest above still pins. What changed is only the POST-declaration fallback,
		// and it is emphatically NOT a reordering of finalizers: the later participant's
		// order is still an absolute constant above the declaring one, and nothing
		// about MaterializeSorted or Finalizer.Order() moved.
		//
		// Two shapes behave differently from the other three, and the difference is the
		// pre-existing one rather than a new asymmetry: an unusable RESULT (an invalid
		// rewrite envelope, an unknown action) is not a failure the finalizer reported,
		// so there is no error of its own to surface and the assembler does not invent
		// one. A returned Go error does surface, as itself.
		for _, shape := range unusableShapes {
			t.Run(shape.name, func(t *testing.T) {
				t.Parallel()
				var ordinary toolcall.Finalizer = &unusableOrdinaryFin{order: AfterExpansionOrder, res: shape.res}
				if shape.err != nil {
					ordinary = &failingOrdinaryFin{order: AfterExpansionOrder}
				}
				expansion := newExpansionAboveRepairFin()
				repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
				a := newToolCallAssembler([]toolcall.Finalizer{expansion, ordinary, repairFin}, 0, catalog)
				if a == nil {
					t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
				}

				args := mandatoryArgsJSON(8 * 1024)
				expanded := mandatoryExpandedArgsJSON(8 * 1024)
				released, err := streamMandatoryToolCall(t, a, "preserved-"+shape.name, args)

				// The declaring pass really ran before the failure, so there was a
				// mandatory-safe result to preserve and the case is not vacuous.
				if expansion.calls != 1 {
					t.Fatalf("the declaring finalizer must have been invoked first: invocations=%d", expansion.calls)
				}
				if repairFin.calls != 1 {
					t.Fatalf("the shipped repair finalizer must have been invoked first: invocations=%d", repairFin.calls)
				}
				// Requirements 4.1 and 4.4: the expansion the declaring pass already made
				// is what this call releases, and the reserved alias reaches nobody.
				if strings.Contains(released, mandatoryVirtualRoot) {
					t.Fatalf("requirements.md 4.1/4.4 - the reserved alias was released: %d bytes", len(released))
				}
				if released != expanded {
					t.Fatalf("released %d bytes, want the %d bytes the declaring pass produced", len(released), len(expanded))
				}
				if released == args {
					t.Fatalf("requirements.md 4.4 - the original %d alias-bearing bytes were replayed", len(released))
				}
				// The later failure surfaces as itself, and a satisfied requirement is
				// never reported as an undecided one.
				if shape.err != nil {
					if !errors.Is(err, shape.err) {
						t.Fatalf("requirements.md 4.6 - the surfaced failure must be the later finalizer's own error: got %T", err)
					}
				} else if err != nil {
					t.Fatalf("an unusable result carries no error to surface, so none may be invented: got %T", err)
				}
				if IsMandatoryBufferingError(err) {
					t.Fatalf("requirements.md 4.6 - a SATISFIED requirement must not be refused as undecided: reason=%q",
						err.Error())
				}
			})
		}
	})

	t.Run("the_shipped_repair_finalizer_never_returns_an_unusable_result", func(t *testing.T) {
		t.Parallel()
		// Across this composition the shipped repair finalizer must never trip
		// any of the three unusable-result fallbacks: it returns no Go error, no
		// unknown action, and every rewrite it does return is a non-empty tool
		// name with valid JSON arguments. That is what makes requirements.md 8.5
		// ("preserve canonical call/event validation after every mutation") hold
		// for the repair feature rather than merely for the assembler.
		for _, tc := range []struct {
			name string
			args string
		}{
			{name: "valid_document", args: mandatoryArgsJSON(4 * 1024)},
			{name: "malformed_document_repairable", args: mandatoryMalformedArgsJSON(4 * 1024)},
			{name: "malformed_document_past_the_repair_budget", args: mandatoryMalformedArgsJSON(repair.DefaultMaxArgsBytes + 4096)},
			{name: "empty_arguments", args: ""},
			{name: "truncated_root", args: `{"path":"/`},
			{name: "trailing_garbage", args: mandatoryArgsJSON(1024) + `garbage`},
			{name: "duplicate_members", args: `{"path":"/a","path":"/b"}`},
			{name: "non_object_root", args: `["/a"]`},
			{name: "null_root", args: `null`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				expansion := newExpansionAboveRepairFin()
				repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
				a := newToolCallAssembler([]toolcall.Finalizer{expansion, repairFin}, 0, catalog)
				if a == nil {
					t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
				}

				released, err := streamMandatoryToolCall(t, a, "envelope-"+tc.name, tc.args)
				if repairFin.calls != 1 {
					t.Fatalf("tool-call-repair invocations=%d want 1", repairFin.calls)
				}
				if repairFin.lastErr != nil {
					t.Fatal("the shipped repair finalizer surfaced a Go error to the assembler")
				}
				decision := repairFin.onlyResult(t)
				if decision.ReasonCode == "" {
					t.Fatal("the shipped repair finalizer must always give a bounded reason code")
				}
				switch decision.Action {
				case toolcall.ActionPass, toolcall.ActionRewrite, toolcall.ActionReject:
				default:
					t.Fatalf("the shipped repair finalizer returned an action the assembler cannot use: %d",
						int(decision.Action))
				}
				if decision.Action == toolcall.ActionRewrite {
					// Mirrors the assembler's rewrite-envelope validation
					// directly instead of calling it, so a relaxation of that
					// production rule cannot silently weaken this assertion.
					if strings.TrimSpace(decision.ToolName) == "" {
						t.Fatalf("requirements.md 8.5 - a repair rewrite carried an empty tool name (reason=%q)",
							decision.ReasonCode)
					}
					if decision.ArgsJSON == nil {
						t.Fatalf("requirements.md 8.5 - a repair rewrite carried nil arguments (reason=%q)",
							decision.ReasonCode)
					}
					if !json.Valid(decision.ArgsJSON) {
						t.Fatalf("requirements.md 8.5 - a repair rewrite carried invalid JSON (reason=%q)",
							decision.ReasonCode)
					}
					if len(decision.ArgsJSON) > repair.DefaultMaxArgsBytes {
						t.Fatalf("requirements.md 8.4 - a repair rewrite grew the document past its own budget: %d > %d",
							len(decision.ArgsJSON), repair.DefaultMaxArgsBytes)
					}
				}
				if decision.Action != toolcall.ActionRewrite && decision.ArgsJSON != nil {
					t.Fatalf("a non-rewrite decision must not carry a mutated document: action=%d args_bytes=%d",
						int(decision.Action), len(decision.ArgsJSON))
				}
				// The composition consequence, asserted only once repair's own
				// result contract holds, so a repair regression is reported as a
				// repair regression rather than as an assembler refusal.
				if err != nil {
					t.Fatalf("the shipped repair finalizer must not fail the call: %v", err)
				}
				// Whatever repair decided, the call still went through the
				// assembler: once the expansion finalizer has seen valid JSON,
				// whatever leaves the assembler must be valid JSON too, whether
				// expansion rewrote it or passed it through.
				if expansion.calls == 1 && json.Valid(expansion.seen) && !json.Valid([]byte(released)) {
					t.Fatalf("requirements.md 8.5 - invalid JSON released after expansion saw valid JSON (released=%d bytes)",
						len(released))
				}
			})
		}
	})
}

// ordinaryArgsJSON builds a valid argument document with no reserved namespace,
// sized to at least minBytes. It isolates assembler overflow enforcement from
// expansion decisions: the document gives the expansion pass nothing to expand,
// so any refusal can only come from the assembler honoring a declared overflow
// policy before the finalizer runs.
func ordinaryArgsJSON(minBytes int) string {
	prefix := `{"path":"/home/dev/elsewhere/src/main.go","content":"`
	suffix := `"}`
	pad := max(minBytes-len(prefix)-len(suffix), 0)
	return prefix + strings.Repeat("x", pad) + suffix
}

// TestRealExpansionAuditModeDoesNotEnforceTheDeclaredBound is the assembler half
// of requirement 7.3.
//
// Feature-level tests invoke Finalize directly and assert audit passes rather
// than rejects. That bypasses the assembler, which is exactly where mandatory
// overflow refusal happens. Audit mode needs the same complete document to
// detect and measure, so it declares the same bound; past that bound it must
// declare pass-through rather than refusal. Otherwise turning measurement on
// would break real traffic it was meant only to observe.
func TestRealExpansionAuditModeDoesNotEnforceTheDeclaredBound(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()
	const (
		declaredBound = toolcall.MinMandatoryMaxArgsBytes
		callBytes     = defaultToolCallFinalizationMaxArgsBytes + 4096
	)
	args := ordinaryArgsJSON(callBytes)
	if len(args) != callBytes {
		t.Fatalf("fixture sizing: got %d want %d", len(args), callBytes)
	}
	if strings.Contains(args, mandatoryVirtualRoot) {
		t.Fatal("fixture must carry no reserved namespace, or the refusal could come from expansion")
	}

	newRealExpansion := func(t *testing.T, mode rewrite.Mode) *expansion.Finalizer {
		t.Helper()
		compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
			Names:       []string{mandatoryToolName},
			ArgPointers: []string{mandatoryFixturePathPointer},
		}})
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("compile the fixture profile: reject %v", reject)
		}
		resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("new resolver: reject %v", reject)
		}
		fin, err := expansion.NewFinalizer(resolver, mode, expansion.Policy{MandatoryMaxArgsBytes: declaredBound})
		if err != nil {
			t.Fatalf("build the shipped expansion finalizer: %v", err)
		}
		return fin
	}

	t.Run("audit_mode_passes_an_oversized_ordinary_call_through", func(t *testing.T) {
		t.Parallel()
		fin := newRealExpansion(t, rewrite.ModeAudit)
		if fin.ToolCallBufferingRequirement().Overflow != toolcall.OverflowPassThrough {
			t.Fatalf("audit mode must declare pass-through past its bound, got %q",
				fin.ToolCallBufferingRequirement().Overflow)
		}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed")
		}
		released, err := streamToolCallNamed(t, a, "audit-over-bound", mandatoryToolName, args)
		if err != nil {
			t.Fatalf("requirements.md 7.3 - audit mode must not enforce the declared bound: %v", err)
		}
		if IsMandatoryBufferingError(err) {
			t.Fatalf("requirements.md 7.3 - measurement must not manufacture a mandatory refusal: %q", err.Error())
		}
		if released != args {
			t.Fatalf("requirements.md 7.3 - released %d bytes, want the %d original bytes unchanged",
				len(released), len(args))
		}
	})

	t.Run("rewrite_mode_control_refuses_the_same_call", func(t *testing.T) {
		t.Parallel()
		fin := newRealExpansion(t, rewrite.ModeRewrite)
		if fin.ToolCallBufferingRequirement().Overflow != toolcall.OverflowReject {
			t.Fatalf("rewrite mode must declare refusal past its bound, got %q",
				fin.ToolCallBufferingRequirement().Overflow)
		}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed")
		}
		released, err := streamToolCallNamed(t, a, "rewrite-over-bound", mandatoryToolName, args)
		if released != "" {
			t.Fatalf("requirements.md 4.5 - a call past the declared bound must release nothing: released=%d bytes",
				len(released))
		}
		var mbe *MandatoryBufferingError
		if !errors.As(err, &mbe) || mbe == nil {
			t.Fatalf("requirements.md 4.5 - want a typed MandatoryBufferingError, got %T", err)
		}
		if mbe.Reason != ReasonMandatoryBufferingOverflow {
			t.Fatalf("requirements.md 4.5 - reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingOverflow)
		}
	})
}

// streamToolCallAsNamedWithMeta drives one completed call for an arbitrary
// model-emitted tool name through the assembler. The shared composition helper
// hardcodes the exact catalog name, so it cannot exercise a spelling variant
// that repair must normalize before expansion can apply.
func streamToolCallAsNamedWithMeta(
	t *testing.T,
	a *toolCallAssembler,
	id, toolName, argsJSON string,
	meta toolcall.Meta,
) (string, error) {
	t.Helper()
	ctx := context.Background()
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

// TestRepairNormalizationCannotBypassMandatoryExpansionBySpelling is
// requirement 8.4 for calls within repair's own budget. Only an actual repair
// rewrite grants the exact catalog identity used by expansion.
func TestRepairNormalizationCannotBypassMandatoryExpansionBySpelling(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()
	mapping, reason := pathvirtualization.DeriveMapping(compositionProjectRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive fixture root: reason %v", reason)
	}
	alias := mapping.VirtualRoot + "src/main.go"
	const callBytes = 8 * 1024
	prefix := `{"` + mandatoryFixturePathMember + `":` + strconv.Quote(alias) + `,"content":"`
	suffix := `"}`
	pad := callBytes - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("fixture sizing: prefix/suffix already exceed %d", callBytes)
	}
	args := prefix + strings.Repeat("x", pad) + suffix
	if len(args) != callBytes {
		t.Fatalf("fixture sizing: got %d want %d", len(args), callBytes)
	}
	if len(args) > repair.DefaultMaxArgsBytes {
		t.Fatalf("fixture must fit the repair budget: %d > %d",
			len(args), repair.DefaultMaxArgsBytes)
	}
	if len(args) > toolcall.DefaultMandatoryMaxArgsBytes {
		t.Fatalf("fixture must stay inside the declared mandatory bound: %d > %d",
			len(args), toolcall.DefaultMandatoryMaxArgsBytes)
	}

	expansionFin := newComposedExpansionProbe(t)
	repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
	a := newToolCallAssembler([]toolcall.Finalizer{expansionFin, repairFin}, 0, catalog)
	if a == nil {
		t.Fatal("assembler must be constructed")
	}

	released, err := streamToolCallAsNamedWithMeta(t, a, "spelling-variant", "read-file", args, compositionMeta())
	if err != nil {
		t.Fatalf("the composition must not fail the call: %v", err)
	}
	if repairFin.calls != 1 {
		t.Fatalf("tool-call-repair invocations=%d want 1: the variant spelling must still reach repair", repairFin.calls)
	}
	decision := repairFin.onlyResult(t)
	if decision.Action != toolcall.ActionRewrite || decision.ToolName != mandatoryToolName {
		t.Fatalf("repair must actually normalize the name: action=%v reason=%q",
			decision.Action, decision.ReasonCode)
	}
	if expansionFin.calls != 1 {
		t.Fatalf("requirements.md 8.4 - repair size policy bypassed mandatory expansion: invocations=%d", expansionFin.calls)
	}
	if strings.Contains(released, mapping.VirtualRoot) {
		t.Fatal("requirements.md 4.4/8.3 - the reserved alias reached the client unexpanded")
	}
	if !strings.Contains(released, compositionProjectRoot) {
		t.Fatal("requirements.md 4.1 - the actual derived alias must expand to the real root")
	}
	if !json.Valid([]byte(released)) {
		t.Fatal("requirements.md 8.5 - the released document is not valid JSON")
	}
}

// TestRealExpansionAuditModeCannotRefuseThroughAnotherFinalizersFailure is the
// rest of requirement 7.3, and it is a different clause from the one above.
//
// Audit mode publishes a completeness requirement because it needs the complete
// document in order to measure, and a bound that is BINDING also says the call
// may not be released while that requirement is undecided. Every unrelated
// optional finalizer on the same chain therefore becomes a way to reject traffic
// the instant an operator enables measurement: one of them errors, panics, or
// returns a result the assembler cannot use, the audit requirement is still
// pending, and the pre-existing replay of the original fragments is replaced by
// a hard `mandatory_buffering_incomplete` refusal.
//
// That is measurement enforcing. Before this feature existed the same chain
// released the fragments, so audit must not change that outcome. Declaring
// OverflowPassThrough is NOT sufficient to prevent it, because the pass-through
// policy only speaks about size; the refusal comes from the requirement still
// being undecided. The audit pass must therefore declare its bound BEST-EFFORT.
//
// The control in the last sub-case is what makes this a real assertion: the very
// same chain in rewrite mode DOES refuse, so the fixture cannot pass vacuously
// by being unable to produce a refusal at all.
func TestRealExpansionAuditModeCannotRefuseThroughAnotherFinalizersFailure(t *testing.T) {
	t.Parallel()

	const declaredBound = toolcall.MinMandatoryMaxArgsBytes
	// Small, well inside both the declared bound and the shared legacy cap, so
	// the ONLY possible cause of a refusal is the undecided requirement.
	const callBytes = 8 * 1024
	args := mandatoryArgsJSON(callBytes)
	if len(args) != callBytes {
		t.Fatalf("fixture sizing: got %d want %d", len(args), callBytes)
	}
	if !strings.Contains(args, mandatoryVirtualRoot) {
		t.Fatal("fixture must carry the reserved namespace, or the requirement would never apply")
	}

	newRealExpansion := func(t *testing.T, mode rewrite.Mode) *expansion.Finalizer {
		t.Helper()
		compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
			Names:       []string{mandatoryToolName},
			ArgPointers: []string{mandatoryFixturePathPointer},
		}})
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("compile the fixture profile: reject %v", reject)
		}
		resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("new resolver: reject %v", reject)
		}
		fin, err := expansion.NewFinalizer(resolver, mode, expansion.Policy{MandatoryMaxArgsBytes: declaredBound})
		if err != nil {
			t.Fatalf("build the shipped expansion finalizer: %v", err)
		}
		return fin
	}

	// everyUnusableShape is one ordinary finalizer ordered strictly BELOW
	// expansion, per unusable surface: a Go error, a panic, an action outside
	// the closed vocabulary, and a rewrite envelope the assembler cannot use.
	everyUnusableShape := map[string]func() toolcall.Finalizer{
		"go_error": func() toolcall.Finalizer {
			return &failingOrdinaryFin{order: expansion.FinalizerOrder - 1}
		},
		"unknown_action": func() toolcall.Finalizer {
			return &unusableOrdinaryFin{order: expansion.FinalizerOrder - 1, res: toolcall.Result{
				Action:     toolcall.Action(999),
				ReasonCode: toolcall.ReasonValidPassThrough,
			}}
		},
		"invalid_rewrite_envelope": func() toolcall.Finalizer {
			return &unusableOrdinaryFin{order: expansion.FinalizerOrder - 1, res: toolcall.Result{
				Action:     toolcall.ActionRewrite,
				ToolName:   mandatoryToolName,
				ArgsJSON:   []byte(`{"path":`),
				ReasonCode: toolcall.ReasonValidPassThrough,
			}}
		},
	}

	for name, newBroken := range everyUnusableShape {
		t.Run("audit_mode/"+name, func(t *testing.T) {
			t.Parallel()
			fin := newRealExpansion(t, rewrite.ModeAudit)
			if got := fin.ToolCallBufferingRequirement().Completeness; got != toolcall.CompletenessBestEffort {
				t.Fatalf("requirements.md 7.3 - audit mode must declare a best-effort bound so it can "+
					"never bind a call, got completeness %q", got)
			}
			a := newToolCallAssembler([]toolcall.Finalizer{newBroken(), fin}, 0, mandatoryCatalog())
			if a == nil {
				t.Fatal("assembler must be constructed")
			}
			released, err := streamToolCallNamed(t, a, "audit-"+name, mandatoryToolName, args)
			if IsMandatoryBufferingError(err) {
				t.Fatalf("requirements.md 7.3 - measurement must not turn an unrelated finalizer's %s "+
					"into a hard rejection: %v", name, err)
			}
			if released != args {
				t.Fatalf("requirements.md 7.3 - audit mode must keep the pre-existing replay: "+
					"released %d bytes, want the %d originals unchanged", len(released), len(args))
			}
		})
	}

	t.Run("rewrite_mode_refuses_the_same_chain", func(t *testing.T) {
		t.Parallel()
		fin := newRealExpansion(t, rewrite.ModeRewrite)
		if got := fin.ToolCallBufferingRequirement().Completeness; got != toolcall.CompletenessMandatory {
			t.Fatalf("rewrite mode must keep a binding bound, got completeness %q", got)
		}
		a := newToolCallAssembler(
			[]toolcall.Finalizer{&failingOrdinaryFin{order: expansion.FinalizerOrder - 1}, fin},
			0, mandatoryCatalog())
		if a == nil {
			t.Fatal("assembler must be constructed")
		}
		released, err := streamToolCallNamed(t, a, "rewrite-unrelated-failure", mandatoryToolName, args)
		if released != "" {
			t.Fatalf("requirements.md 4.6 - a binding requirement must still refuse closed: released %d bytes",
				len(released))
		}
		if !IsMandatoryBufferingError(err) {
			t.Fatalf("requirements.md 4.6 - want a mandatory-buffering refusal, got %v", err)
		}
	})
}
