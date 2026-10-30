package featurehost

// This file proves requirements.md 8.4's first clause is SATISFIABLE, which is a
// wiring property rather than a unit property. The guard
// pathvirtualizationconfig.ValidateGenerationComposition exists (Task 9.1) and
// refuses a generation in which this feature's mandatory expansion could not
// receive valid completed JSON; a guard nobody calls is not a requirement, it is
// dead code. So these tests drive the real composition entry point.
//
// The cases are the whole truth table of the ordering boundary:
//
//   - repair's effective order exactly AT the expansion order refuses,
//   - repair's effective order one below it publishes,
//   - a generation with no repair registration at all publishes,
//   - and the stock generation, with neither feature configured, publishes.
//
// The publishing cases matter as much as the refusing one: a guard that refused
// everything would satisfy 8.4 by making the feature unusable, which is the same
// kind of defect as a guard that refuses nothing.

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

// enabledFeatureRegistration builds one enabled feature registration row carrying
// the given subtree.
func enabledFeatureRegistration(t *testing.T, id, subtree string) aipsdk.Registration {
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

// pathVirtualizationRegistration is the feature under test, enabled in rewrite mode
// so the expansion pass it publishes is the mutating one.
func pathVirtualizationRegistration(t *testing.T) aipsdk.Registration {
	t.Helper()
	return enabledFeatureRegistration(t, config.ID, "enabled: true\nmode: rewrite\n")
}

// repairRegistrationWithOrder is tool-call repair at an operator-chosen finalizer
// order, which is the unbounded key the guard exists to relate to the expansion
// pass's declared order. Enablement is the REGISTRATION's flag, not a subtree key:
// tool-call repair's subtree has no `enabled` key, so spelling one would be an
// unknown-key refusal rather than a configuration.
func repairRegistrationWithOrder(t *testing.T, order string) aipsdk.Registration {
	t.Helper()
	return enabledFeatureRegistration(t, toolcallrepair.ID, "order: "+order+"\n")
}

// TestTheShippedRepairDefaultComposes states the stock truth in one place: repair's
// own default finalizer order is one below this feature's declared expansion order,
// so a deployment that enables both features without tuning either one publishes.
// Without this case the guard could pass every test above and still refuse every real
// deployment.
func TestTheShippedRepairDefaultComposes(t *testing.T) {
	t.Parallel()
	if toolcallrepair.DefaultFinalizerOrder != expansion.FinalizerOrder-1 {
		t.Fatalf("repair's shipped default order is %d, want %d (one below the expansion order)",
			toolcallrepair.DefaultFinalizerOrder, expansion.FinalizerOrder-1)
	}
	rt := newTestCompactionRuntime(t)
	if _, err := rt.CompileGeneration(context.Background(), GenerationInput{
		Registrations: []aipsdk.Registration{
			pathVirtualizationRegistration(t),
			enabledFeatureRegistration(t, toolcallrepair.ID, "mode: conservative\n"),
		},
	}); err != nil {
		t.Fatalf("the stock pairing must compose: %v", err)
	}
}

func TestGenerationCompilationRefusesARepairOrderAtTheExpansionOrder(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	_, err := rt.CompileGeneration(context.Background(), GenerationInput{
		Registrations: []aipsdk.Registration{
			pathVirtualizationRegistration(t),
			repairRegistrationWithOrder(t, "41"),
		},
	})
	if err == nil {
		t.Fatalf("expected generation compilation to refuse repair order %d", expansion.FinalizerOrder)
	}
}

func TestARefusedGenerationPublishesNothing(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	out, err := rt.CompileGeneration(context.Background(), GenerationInput{
		Registrations: []aipsdk.Registration{
			pathVirtualizationRegistration(t),
			repairRegistrationWithOrder(t, "41"),
		},
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !out.Planes.IsZero() {
		t.Fatal("a refused generation published planes")
	}
	if !out.Bundle.PlaneSet.IsZero() {
		t.Fatal("a refused generation published a bundle")
	}
	if len(out.Lifecycles) != 0 {
		t.Fatalf("a refused generation published %d lifecycles", len(out.Lifecycles))
	}
}

func TestGenerationCompilationPublishesARepairOrderBelowTheExpansionOrder(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	// One below the declared expansion order is the last composing value: it proves
	// the boundary is exact rather than "anything below 41 is fine".
	if _, err := rt.CompileGeneration(context.Background(), GenerationInput{
		Registrations: []aipsdk.Registration{
			pathVirtualizationRegistration(t),
			repairRegistrationWithOrder(t, "40"),
		},
	}); err != nil {
		t.Fatalf("repair order %d must publish: %v", expansion.FinalizerOrder-1, err)
	}
}

func TestTheStockGenerationComposes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		regs func(*testing.T) []aipsdk.Registration
	}{
		{
			name: "this feature alone",
			regs: func(t *testing.T) []aipsdk.Registration {
				t.Helper()
				return []aipsdk.Registration{pathVirtualizationRegistration(t)}
			},
		},
		{
			name: "this feature with repair at its shipped default",
			regs: func(t *testing.T) []aipsdk.Registration {
				t.Helper()
				return []aipsdk.Registration{
					pathVirtualizationRegistration(t),
					// No `order` key: repair's own default applies, and that default
					// is exactly one below the expansion pass's declared order. This is
					// the case that matters most - it is what a stock deployment that
					// enables both features without tuning anything gets.
					enabledFeatureRegistration(t, toolcallrepair.ID, "mode: conservative\n"),
				}
			},
		},
		{
			name: "this feature with repair at its shipped low order",
			regs: func(t *testing.T) []aipsdk.Registration {
				t.Helper()
				return []aipsdk.Registration{
					pathVirtualizationRegistration(t),
					repairRegistrationWithOrder(t, "0"),
				}
			},
		},
		{
			name: "neither feature configured",
			regs: func(*testing.T) []aipsdk.Registration { return nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := newTestCompactionRuntime(t)
			if _, err := rt.CompileGeneration(context.Background(), GenerationInput{
				Registrations: tc.regs(t),
			}); err != nil {
				t.Fatalf("must compose: %v", err)
			}
		})
	}
}

// TestARefusalIsIndependentOfTheRolloutMode pins that the ordering guard is a
// COMPOSITION property, not an audit-mode escape hatch. requirements.md 8.4 asks
// mandatory expansion to receive valid completed JSON, and expansion is mandatory
// in both modes, so audit must refuse exactly where rewrite refuses. A guard placed
// behind a mode check would let an operator believe they had a safe rollout while
// the composition they were measuring was the one that could not expand.
func TestARefusalIsIndependentOfTheRolloutMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"audit", "rewrite"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			rt := newTestCompactionRuntime(t)
			if _, err := rt.CompileGeneration(context.Background(), GenerationInput{
				Registrations: []aipsdk.Registration{
					enabledFeatureRegistration(t, config.ID, "enabled: true\nmode: "+mode+"\n"),
					repairRegistrationWithOrder(t, "41"),
				},
			}); err == nil {
				t.Fatalf("mode %q must refuse repair order %d", mode, expansion.FinalizerOrder)
			}
		})
	}
}

// TestARepairRegistrationThatCannotBeReadRefuses proves the guard fails CLOSED on
// an unreadable repair subtree. An effective order this build cannot determine
// cannot be proven to sort before the expansion pass, so the honest answer is a
// refusal rather than an assumption - the same rule the whole configuration surface
// follows (requirements.md 7.5).
func TestARepairRegistrationThatCannotBeReadRefuses(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	if _, err := rt.CompileGeneration(context.Background(), GenerationInput{
		Registrations: []aipsdk.Registration{
			pathVirtualizationRegistration(t),
			// An unknown key is an unreadable subtree, not an ignored one.
			enabledFeatureRegistration(t, toolcallrepair.ID, "order: 10\nnot_a_key: 1\n"),
		},
	}); err == nil {
		t.Fatal("an unreadable repair subtree must refuse the generation")
	}
}

// TestADisabledPathVirtualizationNeverTriggersTheGuard proves the guard is inert
// for every generation that does not enable the feature, including one whose repair
// order would otherwise refuse. This is what makes the guard safe to call
// unconditionally from the composition root.
func TestADisabledPathVirtualizationNeverTriggersTheGuard(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{name: "absent subtree", yaml: "null\n"},
		{name: "empty subtree", yaml: "{}\n"},
		{name: "explicitly disabled", yaml: "enabled: false\n"},
		{name: "disabled with a mode", yaml: "enabled: false\nmode: rewrite\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := newTestCompactionRuntime(t)
			if _, err := rt.CompileGeneration(context.Background(), GenerationInput{
				Registrations: []aipsdk.Registration{
					enabledFeatureRegistration(t, config.ID, tc.yaml),
					repairRegistrationWithOrder(t, "41"),
				},
			}); err != nil {
				t.Fatalf("a disabled feature must not trigger the cross-feature guard: %v", err)
			}
		})
	}
}

// TestARegistrationDisabledAtTheOuterLevelNeverTriggersTheGuard covers the other
// half of "disabled": the registration row's own Enabled flag. The subtree may be
// fully enabled while the row is not, and an operator who turned the row off must
// not be refused for a composition that publishes nothing.
func TestARegistrationDisabledAtTheOuterLevelNeverTriggersTheGuard(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	disabled := pathVirtualizationRegistration(t)
	disabled.Enabled = false
	if _, err := rt.CompileGeneration(context.Background(), GenerationInput{
		Registrations: []aipsdk.Registration{
			disabled,
			repairRegistrationWithOrder(t, "41"),
		},
	}); err != nil {
		t.Fatalf("a disabled registration must not trigger the cross-feature guard: %v", err)
	}
}
