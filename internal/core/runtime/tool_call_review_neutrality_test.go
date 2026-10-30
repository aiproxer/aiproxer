package runtime

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestExpansionNearMissHasNoMandatoryRequirement(t *testing.T) {
	t.Parallel()
	fin := newComposedExpansionProbe(t)
	if fin.ToolCallBufferingApplies("read-file", mandatoryCatalog()[0], mandatoryCatalog()) {
		t.Fatal("requirement 3.6 - expansion applicability itself must be byte-exact")
	}
	if !fin.ToolCallBufferingApplies(mandatoryToolName, mandatoryCatalog()[0], mandatoryCatalog()) {
		t.Fatal("requirement 4.6 - expansion applicability must cover the exact-name control")
	}
	a := newToolCallAssembler([]toolcall.Finalizer{
		&failingOrdinaryFin{order: expansion.FinalizerOrder - 1}, fin,
	}, 0, mandatoryCatalog())
	if a.deriveCallRequirements("read-file") != nil {
		t.Fatal("requirement 3.6 - near-miss name must not inherit a mandatory requirement")
	}
	if a.deriveCallRequirements(mandatoryToolName) == nil {
		t.Fatal("requirement 4.6 - exact-name control must have a mandatory requirement")
	}
	args := ordinaryArgsJSON(8 * 1024)
	released, err := streamToolCallAsNamedWithMeta(t, a, "near-miss", "read-file", args, compositionMeta())
	if err != nil || released != args {
		t.Fatal("requirement 3.6 - unprofiled call must retain legacy fallback semantics")
	}
}

func TestExpansionOversizedNamePolicyUsesActualAlias(t *testing.T) {
	t.Parallel()
	mapping, reason := pathvirtualization.DeriveMapping(compositionProjectRoot)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		t.Fatal("fixture must derive an active mapping")
	}
	args := `{"path":"` + mapping.VirtualRoot + `src/main.go","content":"` +
		strings.Repeat("x", repair.DefaultMaxArgsBytes) + `"}`
	for _, name := range []string{mandatoryToolName, "read-file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expand := newComposedExpansionProbe(t)
			normalize := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
			a := newToolCallAssembler([]toolcall.Finalizer{normalize, expand}, 0, mandatoryCatalog())
			released, err := streamToolCallAsNamedWithMeta(t, a, "large", name, args, compositionMeta())
			if err != nil {
				t.Fatalf("unexpected failure: %v", err)
			}
			if name == mandatoryToolName {
				if normalize.calls != 1 || normalize.onlyResult(t).ReasonCode != toolcall.ReasonArgsTooLarge {
					t.Fatal("fixture must exercise repair's size decline")
				}
				if expand.calls != 1 || strings.Contains(released, mapping.VirtualRoot) ||
					!strings.Contains(released, compositionProjectRoot) {
					t.Fatal("requirement 4.1 - exact-name call must expand the actual derived alias despite repair decline")
				}
			} else if released != args || normalize.calls != 0 || expand.calls != 0 {
				t.Fatal("requirement 3.6 - oversized unprofiled spelling must keep legacy pass-through, not inherit a profile")
			}
		})
	}
}

func TestAuditAfterRepairKeepsLegacyFailureFallback(t *testing.T) {
	t.Parallel()
	compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
		Names: []string{mandatoryToolName}, ArgPointers: []string{"/path"},
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatal("compile fixture profile")
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatal("compile fixture resolver")
	}
	audit, err := expansion.NewFinalizer(resolver, rewrite.ModeAudit, expansion.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	// Missing closing brace forces a real syntax rewrite, while the exact name
	// ensures audit's best-effort declaration applies even after names are exact.
	const args = `{"path":"/ordinary/file"`
	for _, enabled := range []bool{false, true} {
		normalize := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
		chain := []toolcall.Finalizer{normalize}
		if enabled {
			chain = append(chain, audit)
		}
		chain = append(chain, &failingOrdinaryFin{order: expansion.FinalizerOrder + 1})
		a := newToolCallAssembler(chain, 0, mandatoryCatalog())
		released, err := streamToolCallAsNamedWithMeta(t, a, "audit-fallback", mandatoryToolName, args, compositionMeta())
		if normalize.onlyResult(t).Action != toolcall.ActionRewrite {
			t.Fatal("fixture must exercise a real repair rewrite")
		}
		if err != nil || released != args {
			t.Fatalf("requirement 7.3 - audit=%v must retain the original fragments and legacy error behavior", enabled)
		}
	}
}

func TestBestEffortRewriteDoesNotRetainMandatorySafe(t *testing.T) {
	t.Parallel()
	fin := newMandatoryExpansionFin()
	fin.spec.Completeness = toolcall.CompletenessBestEffort
	fin.spec.Overflow = toolcall.OverflowPassThrough
	a := newToolCallAssembler([]toolcall.Finalizer{
		fin, &failingOrdinaryFin{order: fin.order + 1},
	}, 0, mandatoryCatalog())
	args := mandatoryArgsJSON(8 * 1024)
	released, err := streamMandatoryToolCall(t, a, "best-effort-rewrite", args)
	if fin.calls != 1 {
		t.Fatal("fixture must invoke the best-effort rewriter")
	}
	if err != nil || released != args {
		t.Fatal("best-effort rewrite must not replace legacy fallback with a mandatory-safe snapshot")
	}
}
