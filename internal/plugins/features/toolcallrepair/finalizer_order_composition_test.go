package toolcallrepair_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Spec: b-leg-path-virtualization Task 7.3. Requirements 8.4, 8.5 and design.md
// sections "6. Mandatory Buffering / Completeness Contract" ("do not globally
// raise tool-call-repair's own repair budget") and "7. Path Expansion Finalizer"
// (step 4 parse completed ArgsJSON).
//
// Task 7.3's tool-call-repair <-> path-expansion composition is observed in
// internal/core/runtime, because the tool-call assembler is private to that
// package. This file owns the feature-side half, which the core package cannot
// reach: the production feature-plane composition path a host actually uses to
// assemble the finalizer list.
//
// Two facts are established here, both of them prerequisites for the
// repair-before-expansion contract:
//
//  1. The finalizer plane concatenates contributions in contribution order and
//     materializes them with toolcall.MaterializeSorted, so a second feature
//     lands after tool-call-repair purely by declaring a higher Order().
//     Contribution order does not decide anything. This is the whole mechanism
//     Task 8.1 needs; no core rule exists or is needed.
//
//  2. That materialization returns the contributed finalizer INSTANCES, so the
//     optional toolcall.BufferingRequirement capability a finalizer publishes
//     survives plane composition and is still visible to the assembler's type
//     assertion. A mandatory declaration contributed through the plane is not
//     silently downgraded on the way to the runtime.
//
// It also records the operator-visible hazard: tool-call-repair's order is
// configurable, so "declared above 40" is only correct against the shipped
// default. That is characterized, not fixed.

const expansionStandInID = "path-expansion-stand-in"

// expansionStandInFinalizer occupies a declared order slot and nothing else. It
// is not a path-expansion finalizer, performs no rewriting, and never inspects
// arguments. It exists so the plane composition can be observed without
// depending on the not-yet-shipped pathvirtualization feature.
type expansionStandInFinalizer struct {
	id    string
	order int
	spec  toolcall.BufferingSpec
	calls int
}

func (f *expansionStandInFinalizer) ID() string { return f.id }

func (f *expansionStandInFinalizer) Order() int { return f.order }

func (f *expansionStandInFinalizer) ToolCallBufferingRequirement() toolcall.BufferingSpec {
	return f.spec
}

func (f *expansionStandInFinalizer) Finalize(
	_ context.Context,
	call toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	f.calls++
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

var _ toolcall.Finalizer = (*expansionStandInFinalizer)(nil)

// mandatoryStandInSpec is the declaration design.md section 6 prescribes for a
// finalizer that must receive completed arguments: a 1 MiB bound with
// OverflowReject. Any feature needing it must publish this capability itself;
// it is deliberately NOT obtainable by widening tool-call-repair's budget.
func mandatoryStandInSpec() toolcall.BufferingSpec {
	return toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}
}

// shippedRepairFinalizer returns the finalizer the shipped tool-call-repair
// bundle contributes at its default configuration.
func shippedRepairFinalizer(t *testing.T) toolcall.Finalizer {
	t.Helper()
	bundle, err := toolcallrepair.FeatureBundle(toolcallrepair.Config{})
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	finalizers := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("want 1 contributed finalizer, got %d", len(finalizers))
	}
	if finalizers[0].Order() != toolcallrepair.DefaultFinalizerOrder {
		t.Fatalf("shipped repair order changed: got %d want %d",
			finalizers[0].Order(), toolcallrepair.DefaultFinalizerOrder)
	}
	return finalizers[0]
}

// composeContributedFinalizers freezes one finalizer plane from the given
// contributors, in the order given, through the real aipfeature machinery a
// host uses. It returns both the concatenated contribution order and the
// request-materialized execution order.
//
// The execution order is read from aipfeature.FreezeRequestPlanes, which is the
// step that evaluates PlaneToolCallFinalizers.RequestMaterializer
// (toolcall.MaterializeSorted) once per request snapshot. Reading it from the
// unfrozen set instead would return the contribution order and prove nothing.
func composeContributedFinalizers(t *testing.T, contributors ...[]toolcall.Finalizer) (
	contributed, materialized []toolcall.Finalizer,
) {
	t.Helper()
	cs := aipfeature.NewContributionSet()
	for i, group := range contributors {
		// Distinct contributor IDs: the plane is Multiplicity MultOrdered with
		// CombConcatenate, so two features may both contribute to it.
		id := "test-contributor-" + string(rune('a'+i))
		if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, id, group); err != nil {
			t.Fatalf("Contribute group %d: %v", i, err)
		}
	}
	frozen := cs.Freeze()
	if err := frozen.Validate(); err != nil {
		t.Fatalf("frozen plane set: %v", err)
	}
	requestFrozen := aipfeature.FreezeRequestPlanes(frozen)
	if err := requestFrozen.Validate(); err != nil {
		t.Fatalf("request-frozen plane set: %v", err)
	}
	return aipfeature.Get(frozen, aipfeature.PlaneToolCallFinalizers),
		aipfeature.RequestExecution(requestFrozen).ToolCallFinalizers()
}

func TestFeaturePlaneCompositionOrdersRepairFirstByOrderAlone(t *testing.T) {
	t.Parallel()

	t.Run("a_finalizer_contributed_above_the_repair_order_runs_after_it", func(t *testing.T) {
		t.Parallel()
		repairFin := shippedRepairFinalizer(t)
		// design.md section 6's shape for a finalizer that must receive
		// completed arguments, declared at the first order above the shipped
		// tool-call-repair order. This is exactly what Task 8.1 must contribute.
		expansion := &expansionStandInFinalizer{
			id:    expansionStandInID,
			order: toolcallrepair.DefaultFinalizerOrder + 1,
			spec:  mandatoryStandInSpec(),
		}
		if expansion.Order() <= repairFin.Order() {
			t.Fatalf("the declaration must sit strictly above the repair order: expansion=%d repair=%d",
				expansion.Order(), repairFin.Order())
		}

		// Contributed in the OPPOSITE order on purpose: only Order() may decide.
		contributed, materialized := composeContributedFinalizers(t,
			[]toolcall.Finalizer{expansion},
			[]toolcall.Finalizer{repairFin},
		)
		if len(contributed) != 2 || len(materialized) != 2 {
			t.Fatalf("plane size: contributed=%d materialized=%d, want 2 and 2", len(contributed), len(materialized))
		}
		// The plane preserves contribution order, so the materialized order is
		// produced by sorting and by nothing else.
		if contributed[0].ID() != expansionStandInID || contributed[1].ID() != toolcallrepair.ID {
			t.Fatalf("contribution order changed: [%s %s]", contributed[0].ID(), contributed[1].ID())
		}
		if materialized[0].ID() != toolcallrepair.ID || materialized[1].ID() != expansionStandInID {
			t.Fatalf("requirements.md 8.4 - repair must materialize before expansion: [%s %s]",
				materialized[0].ID(), materialized[1].ID())
		}

		// The materializer must hand back the contributed instances, so the
		// optional mandatory declaration is still readable by the assembler's
		// plain type assertion after plane composition.
		if materialized[1] != toolcall.Finalizer(expansion) {
			t.Fatal("the plane materializer must not replace a contributed finalizer instance")
		}
		declaration, ok := materialized[1].(toolcall.BufferingRequirement)
		if !ok {
			t.Fatal("requirements.md 4.5/4.6 - a mandatory declaration must survive feature-plane composition")
		}
		spec := declaration.ToolCallBufferingRequirement()
		if err := spec.Validate(); err != nil {
			t.Fatalf("the declared spec must validate: %v", err)
		}
		if !spec.DeclaresMandatoryBound() || spec.Overflow != toolcall.OverflowReject {
			t.Fatalf("declared spec changed: declares=%t overflow=%q",
				spec.DeclaresMandatoryBound(), spec.Overflow)
		}
		// Plane composition is pure ordering: nothing runs a finalizer while the
		// list is assembled. Only the runtime's assembler invokes them.
		if expansion.calls != 0 {
			t.Fatalf("plane composition must not invoke a finalizer: invocations=%d", expansion.calls)
		}
	})

	t.Run("a_finalizer_contributed_below_the_repair_order_runs_before_it", func(t *testing.T) {
		t.Parallel()
		// Negative control at the plane level: the same two contributors with
		// the declaration moved one order below the shipped repair order. It
		// proves the ordering assertion above is genuinely order-driven.
		repairFin := shippedRepairFinalizer(t)
		expansion := &expansionStandInFinalizer{
			id:    expansionStandInID,
			order: toolcallrepair.DefaultFinalizerOrder - 1,
			spec:  mandatoryStandInSpec(),
		}

		_, materialized := composeContributedFinalizers(t,
			[]toolcall.Finalizer{repairFin},
			[]toolcall.Finalizer{expansion},
		)
		if len(materialized) != 2 {
			t.Fatalf("materialized finalizers=%d want 2", len(materialized))
		}
		if materialized[0].ID() != expansionStandInID || materialized[1].ID() != toolcallrepair.ID {
			t.Fatalf("negative control: the lower declaration must materialize first: [%s %s]",
				materialized[0].ID(), materialized[1].ID())
		}
	})

	t.Run("an_operator_configured_repair_order_moves_the_composition_point", func(t *testing.T) {
		t.Parallel()
		// tool-call-repair's order is operator-configurable, so "above 40" is
		// only correct against the shipped default. Characterized, not fixed:
		// a finalizer that declares DefaultFinalizerOrder+1 and an operator who
		// raises tool-call-repair's order compose the other way round, and the
		// mandatory finalizer then sees a document repair has not touched.
		// Task 8.1/9.1 must state this operator interaction explicitly.
		raised := toolcallrepair.DefaultFinalizerOrder + 40
		cfg := toolcallrepair.Config{Order: &raised}
		bundle, err := toolcallrepair.FeatureBundle(cfg)
		if err != nil {
			t.Fatalf("FeatureBundle with a raised order: %v", err)
		}
		repairFin := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)[0]
		if repairFin.Order() != raised {
			t.Fatalf("operator order was not applied: got %d want %d", repairFin.Order(), raised)
		}
		expansion := &expansionStandInFinalizer{
			id:    expansionStandInID,
			order: toolcallrepair.DefaultFinalizerOrder + 1,
			spec:  mandatoryStandInSpec(),
		}

		_, materialized := composeContributedFinalizers(t,
			[]toolcall.Finalizer{expansion},
			[]toolcall.Finalizer{repairFin},
		)
		if len(materialized) != 2 {
			t.Fatalf("materialized finalizers=%d want 2", len(materialized))
		}
		if materialized[0].ID() != expansionStandInID {
			t.Fatalf("an operator order above the declaration inverts the composition: [%s %s]",
				materialized[0].ID(), materialized[1].ID())
		}
	})
}

// TestShippedRepairBundleDeclaresNoMandatoryCompletenessRequirement pins the
// design.md section 6 consequence that makes the whole composition possible:
// tool-call-repair keeps its own size policy and is NOT a mandatory
// completeness declarer. If it ever published toolcall.BufferingRequirement,
// its 64 KiB budget would silently become an assembler-wide assembly bound, which
// is precisely the coupling section 6 forbids.
func TestShippedRepairBundleDeclaresNoMandatoryCompletenessRequirement(t *testing.T) {
	t.Parallel()

	repairFin := shippedRepairFinalizer(t)
	if _, ok := repairFin.(toolcall.BufferingRequirement); ok {
		t.Fatal("the shipped tool-call-repair finalizer must not publish a mandatory buffering requirement")
	}

	// Positive control: the capability and its validation are reachable through
	// the SDK at all, so the negative assertion above is not vacuous.
	spec := mandatoryStandInSpec()
	if !spec.DeclaresMandatoryBound() {
		t.Fatal("the design.md section 6 declaration shape must declare a mandatory bound")
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("the design.md section 6 declaration shape must validate: %v", err)
	}
	if spec.Overflow != toolcall.OverflowReject {
		t.Fatalf("path expansion must declare OverflowReject, got %q", spec.Overflow)
	}

	// The repair budget the bundle contributes is a repair budget: it is the
	// finalizer's own policy value, which the assembler must never adopt as the
	// shared assembly bound. Task 7.2's assembler-level constant tests pin the
	// two numbers; here only the feature-side fact is asserted, that a
	// buffer-bound contribution is not a completeness declaration.
	permissive := toolcall.BufferingSpec{MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes}
	if err := permissive.Validate(); err != nil {
		t.Fatalf("a bare bound without a policy must stay valid and declare a bound: %v", err)
	}
	if !permissive.DeclaresMandatoryBound() {
		t.Fatal("a bare bound must declare a mandatory bound")
	}
}
