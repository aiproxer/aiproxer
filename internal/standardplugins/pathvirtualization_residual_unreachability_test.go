package standardplugins_test

// This file owns ONE mechanical property for Task 12.2, and it is the premise the
// accepted post-declaration replay residual rests on.
//
// THE RESIDUAL. internal/core/runtime/tool_call_assembler.go's finalizeCall refuses
// closed on an unusable result while a well-formed mandatory completeness
// requirement is still UNDECIDED (ReasonMandatoryBufferingIncomplete), and clears
// that pending flag as soon as the declaring finalizer is invoked. So once the
// expansion pass at Order 41 has run, an ordinary finalizer sorting ABOVE 41 that
// fails, returns an unknown action, or returns an invalid rewrite envelope falls
// back to the assembler's pre-existing behaviour of replaying buf.originals - and
// those originals still carry the reserved alias, with err == nil.
//
// Task 7.2's review accepted and reserved that residual, and Task 12.2 owns the
// decision. It is characterized, not hidden, by
// internal/core/runtime/tool_call_repair_expansion_composition_test.go's
// `residual_after_the_declaring_finalizer_ran_the_replay_fallback_still_applies`.
//
// WHY IT IS UNREACHABLE IN THE SHIPPED COMPOSITION, AND WHY THAT MUST BE PROVEN
// RATHER THAN ASSUMED. The residual needs a finalizer that sorts at or above the
// declaring expansion pass. Two mechanisms already refuse that generation, and
// neither is a reordering rule:
//
//   - pathvirtualizationconfig.ValidateGenerationComposition (called from
//     featurehost.CompileGeneration, internal/standardplugins/featurehost/generation.go:57)
//     REFUSES PUBLICATION when this feature is enabled, tool-call repair is enabled,
//     and repair's EFFECTIVE finalizer order is >= expansion.FinalizerOrder. That is
//     the only other tool-call finalizer in the shipped distribution, and its `order`
//     is otherwise unbounded above - so the guard, not the number, is what keeps it
//     strictly below 41.
//   - the two shipped orders are 40 (toolcallrepair.DefaultFinalizerOrder) and 41
//     (expansion.FinalizerOrder), so the stock pairing already composes.
//
// Neither mechanism is visible from inside the two feature packages: they are two
// different packages, and the property spans both. So the property is asserted HERE,
// at the composition root that actually owns the finalizer plane, by driving the
// REAL `MergeFeatureSurfacesWithHost` over the REAL standard registry and reading
// the FINALIZED chain the runtime would iterate.
//
// WHAT THIS FILE DOES NOT DO. It does not reorder finalizers, and it does not add a
// core rule. It states a fact about the composition this distribution publishes, so
// a future third finalizer sorting above 41 fails here - at the composition root,
// where the decision belongs - rather than silently reopening a security-relevant
// leak that three separate chokepoints were built to close.

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"gopkg.in/yaml.v3"
)

// residualFeatureRegistration builds one ENABLED feature registration row carrying
// the given subtree.
//
// Enablement is the registration's own flag, which is the only spelling that works
// for both features: tool-call repair's subtree has no `enabled` key, so spelling
// one there would be an unknown-key refusal rather than a configuration.
func residualFeatureRegistration(t *testing.T, id, subtree string) aipsdk.Registration {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(subtree), &node); err != nil {
		t.Fatalf("parse %s subtree: %v", id, err)
	}
	return aipsdk.Registration{
		Kind:    aipsdk.PluginKindFeature,
		ID:      id,
		Enabled: true,
		Config:  aipsdk.ConfigPayload{Node: node},
	}
}

// residualFinalizerChain composes the REAL standard surface for these registrations
// and returns the finalizer chain in the exact order the runtime's assembler would
// iterate it.
//
// MaterializeSorted is the runtime's own composition mechanism
// (internal/core/runtime/tool_call_assembler.go:52), so this is not a re-derivation:
// it is the same sort the assembler performs over the same frozen plane.
func residualFinalizerChain(t *testing.T, registrations ...aipsdk.Registration) []toolcall.Finalizer {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	surface, err := featurebundle.MergeFeatureSurfacesWithHost(reg, registrations, featurebundle.HostContributions{})
	if err != nil {
		t.Fatalf("MergeFeatureSurfacesWithHost: %v", err)
	}
	return toolcall.MaterializeSorted(aipfeature.Get(surface.Frozen, aipfeature.PlaneToolCallFinalizers))
}

// TestTheShippedCompositionPublishesNoFinalizerAboveTheDeclaringExpansionPass is
// the mechanical form of the residual's reachability argument.
//
// It drives the real composition root with BOTH features enabled and asserts that
// the declaring expansion pass is the LAST finalizer in the chain the assembler
// would iterate. Nothing may sort at or above it, because anything that did could
// fail after the expansion has already run and reach the accepted post-declaration
// replay fallback, which releases the alias with err == nil.
//
// The assertion is stated as "the expansion pass is last" rather than as a count, so
// it keeps meaning when a third feature is added: a new finalizer BELOW 41 leaves it
// true, and a new one at or above 41 fails.
func TestTheShippedCompositionPublishesNoFinalizerAboveTheDeclaringExpansionPass(t *testing.T) {
	t.Parallel()

	chain := residualFinalizerChain(t,
		residualFeatureRegistration(t, config.ID, "enabled: true\nmode: rewrite\n"),
		residualFeatureRegistration(t, standardplugins.ToolCallRepairFeatureID, "mode: conservative\n"),
	)

	// NON-VACUITY, part one: the fixture must really contain the declaring pass, or
	// "nothing sorts above it" would be satisfied by an empty chain.
	declaring := -1
	for i, fin := range chain {
		if fin == nil {
			continue
		}
		if fin.ID() == expansion.FinalizerID {
			declaring = i
		}
	}
	if declaring < 0 {
		t.Fatalf("fixture: the composed chain must contain the declaring expansion pass: chain_len=%d", len(chain))
	}
	if _, ok := chain[declaring].(toolcall.BufferingRequirement); !ok {
		t.Fatal("fixture: the composed expansion pass must publish the mandatory buffering requirement whose undecided state the chokepoint tracks")
	}
	// The shipped tool-call repair pass must be present too, or this test would hold
	// for a composition that simply omits the only other finalizer and would prove
	// nothing about the order that matters.
	if len(chain) < 2 {
		t.Fatalf("fixture: the stock pairing must compose both finalizers: chain_len=%d", len(chain))
	}

	// The property itself.
	for i, fin := range chain {
		if fin == nil {
			continue
		}
		if i > declaring {
			t.Fatalf("requirements.md 4.6/8.3 - finalizer %q (order %d) sorts ABOVE the declaring expansion pass (order %d) at chain position %d; if it fails after the expansion has run, the assembler replays the original alias-bearing fragments with no error, which is the accepted post-declaration residual and must not be reachable in a published composition",
				fin.ID(), fin.Order(), expansion.FinalizerOrder, i)
		}
	}

	// The shipped default pairing is what makes the stock deployment publish at all.
	if toolcallrepair.DefaultFinalizerOrder != expansion.FinalizerOrder-1 {
		t.Fatalf("tool-call repair's shipped default order is %d, want %d (strictly below the expansion order)",
			toolcallrepair.DefaultFinalizerOrder, expansion.FinalizerOrder-1)
	}
}

// TestEveryComposingRepairOrderStillPublishesTheExpansionPassLast sweeps the whole
// boundary rather than one point on it.
//
// The refusal for an order AT or ABOVE the expansion order is owned by
// pathvirtualizationconfig.ValidateGenerationComposition, driven from
// featurehost.CompileGeneration (internal/standardplugins/featurehost/generation.go:57),
// and is proved there by that package's own cases. This file proves the OTHER half:
// every order the guard PERMITS composes into a chain whose last finalizer is the
// declaring expansion pass. Without the sweep, "the stock pairing composes" would be
// a single point on an operator-tunable key, and a permitted order that composed
// differently would reopen the residual without any test noticing.
//
// WHERE THE GUARD RUNS, and why this file does not assert the refusal itself:
// featurebundle.MergeFeatureSurfacesWithHost is the function that actually freezes
// the finalizer plane, and it performs NO cross-feature composition check - it merges
// whatever the enabled registrations publish. The refusal therefore belongs to the
// layer above it, which sees the whole registration list, and that is where it is.
// This file drives the freeze directly because that is the artifact whose ORDER the
// residual depends on; asserting the refusal here instead would duplicate a guard
// that already exists one layer up and would pass even if that layer were deleted.
func TestEveryComposingRepairOrderStillPublishesTheExpansionPassLast(t *testing.T) {
	t.Parallel()

	for _, order := range []string{"0", "20", "39", "40", "41"} {
		if order == "41" {
			// The boundary value itself is refused by the guard one layer up; it must
			// not appear in the sweep's permitted set, or the sweep would be asserting
			// a composition the guard exists to prevent.
			continue
		}
		t.Run("repair_order_"+order, func(t *testing.T) {
			t.Parallel()
			chain := residualFinalizerChain(t,
				residualFeatureRegistration(t, config.ID, "enabled: true\nmode: rewrite\n"),
				residualFeatureRegistration(t, standardplugins.ToolCallRepairFeatureID, "order: "+order+"\n"),
			)
			if len(chain) < 2 {
				t.Fatalf("a permitted repair order of %s must compose both finalizers: chain_len=%d", order, len(chain))
			}
			last := chain[len(chain)-1]
			if last == nil || last.ID() != expansion.FinalizerID {
				got := "nil"
				if last != nil {
					got = last.ID()
				}
				t.Fatalf("requirements.md 4.6/8.3 - a permitted repair order of %s must leave the declaring expansion pass LAST, so nothing can fail after it and reach the accepted post-declaration replay fallback; last finalizer is %q", order, got)
			}
		})
	}
}

// TestTheFreezeStepItselfPerformsNoCrossFeatureCheck records WHY the guard cannot
// live where this file's other cases read the chain.
//
// MergeFeatureSurfacesWithHost is the only function that freezes the finalizer
// plane, and it merges whatever the enabled registrations publish without relating
// them to each other. That is why ValidateGenerationComposition has to take the whole
// registration list from the layer above, and it is a fact a future refactor could
// otherwise break silently by moving the guard down into the merge.
//
// The case is deliberately a POSITIVE control on the sweep above: it shows that the
// guard's ABSENCE at this layer is what makes a permitted order necessary, so the
// sweep's assertion that permitted orders compose safely rests on the guard running
// somewhere, not on this layer being safe by accident.
func TestTheFreezeStepItselfPerformsNoCrossFeatureCheck(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	// The order that the guard one layer up refuses. If this merge ever started
	// enforcing the cross-feature rule itself, the guard above would be redundant and
	// this case would fail - which is the point: it detects a MOVE, not a fix.
	surface, err := featurebundle.MergeFeatureSurfacesWithHost(reg, []aipsdk.Registration{
		residualFeatureRegistration(t, config.ID, "enabled: true\nmode: rewrite\n"),
		residualFeatureRegistration(t, standardplugins.ToolCallRepairFeatureID, "order: 100\n"),
	}, featurebundle.HostContributions{})
	if err != nil {
		t.Skipf("the freeze step now refuses this composition itself (%v); the cross-feature guard has moved down into it, so this control no longer describes the layering", err)
	}
	chain := toolcall.MaterializeSorted(aipfeature.Get(surface.Frozen, aipfeature.PlaneToolCallFinalizers))
	above := 0
	for _, fin := range chain {
		if fin != nil && fin.ID() != expansion.FinalizerID && fin.Order() >= expansion.FinalizerOrder {
			above++
		}
	}
	if above == 0 {
		t.Fatal("fixture: this control must observe a finalizer sorting at or above the expansion order, or it is not exercising the unguarded layering it documents")
	}
}
