package runtime

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Override only the identity, leaving the injected failure and order intact.
type transitionInterposer struct {
	toolcall.Finalizer
	id string
}

func (f transitionInterposer) ID() string { return f.id }

func transitionAliasArgs(t *testing.T) (string, string) {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(compositionProjectRoot)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		t.Fatal("fixture must derive an active mapping")
	}
	args := `{"path":` + strconv.Quote(mapping.VirtualRoot+"src/main.go") + `}`
	if len(args) > repair.DefaultMaxArgsBytes {
		t.Fatal("fixture must fit repair's budget")
	}
	return args, mapping.VirtualRoot
}

func TestToolNameRewriteActivatesExpansionBeforeInterposedFailure(t *testing.T) {
	t.Parallel()
	args, alias := transitionAliasArgs(t)
	for _, position := range []struct {
		name  string
		order int
		id    string
	}{
		{"after_repair_same_order", repair.DefaultFinalizerOrder, "zz-after-repair"},
		{"before_expansion_same_order", repair.DefaultFinalizerOrder + 1, "aaa-before-expansion"},
	} {
		for _, shape := range postLaterUnusableShapes() {
			t.Run(position.name+"/"+shape.name, func(t *testing.T) {
				t.Parallel()
				normalize := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
				expand := newComposedExpansionProbe(t)
				middle := transitionInterposer{Finalizer: shape.ordinary(position.order), id: position.id}
				a := newToolCallAssembler([]toolcall.Finalizer{expand, middle, normalize}, 0, mandatoryCatalog())
				if a.finalizers[0].ID() != normalize.ID() || a.finalizers[1].ID() != middle.ID() {
					t.Fatal("fixture must interpose the ordinary finalizer between repair and expansion")
				}
				if a.deriveCallRequirements("read-file") != nil {
					t.Fatal("requirement 3.6 - the initial near-miss name must remain unprofiled")
				}
				released, err := streamToolCallAsNamedWithMeta(t, a, "transition", "read-file", args, compositionMeta())
				result := normalize.onlyResult(t)
				if result.Action != toolcall.ActionRewrite || result.ToolName != mandatoryToolName {
					t.Fatal("fixture must perform a real tool-name repair")
				}
				if expand.calls != 0 {
					t.Fatal("fixture failure must prevent expansion from running")
				}
				if strings.Contains(released, alias) || released != "" {
					t.Fatal("requirements 4.6/8.3 - no original alias-bearing bytes may escape after applicability changes")
				}
				var mandatory *MandatoryBufferingError
				if !errors.As(err, &mandatory) || mandatory.Reason != ReasonMandatoryBufferingIncomplete ||
					mandatory.FinalizerID != expand.ID() {
					t.Fatalf("requirement 4.6 - want incomplete expansion decision, got %v", err)
				}
			})
		}
	}

	t.Run("successful_transition_expands", func(t *testing.T) {
		t.Parallel()
		normalize := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
		expand := newComposedExpansionProbe(t)
		a := newToolCallAssembler([]toolcall.Finalizer{expand, normalize}, 0, mandatoryCatalog())
		released, err := streamToolCallAsNamedWithMeta(t, a, "positive", "read-file", args, compositionMeta())
		if err != nil || normalize.onlyResult(t).Action != toolcall.ActionRewrite || expand.calls != 1 ||
			strings.Contains(released, alias) || !strings.Contains(released, compositionProjectRoot) {
			t.Fatal("requirement 4.1 - successful name repair must reach real expansion and remove the actual alias")
		}
	})
}

func TestNewlyApplicableRequirementEnforcesItsOwnBoundBeforeNextFinalizer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		spec toolcall.BufferingSpec
		want string
	}{
		{"overflow", toolcall.BufferingSpec{MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes, Overflow: toolcall.OverflowReject}, ReasonMandatoryBufferingOverflow},
		{"invalid", toolcall.BufferingSpec{MaxArgsBytes: 1, Overflow: toolcall.OverflowReject}, ReasonMandatoryBufferingDeclarationInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			renamer := &unusableOrdinaryFin{order: 10, res: toolcall.Result{
				Action: toolcall.ActionRewrite, ToolName: mandatoryToolName,
				ArgsJSON: []byte(ordinaryArgsJSON(toolcall.MinMandatoryMaxArgsBytes + 1)),
			}}
			middle := &unusableOrdinaryFin{order: 11, res: toolcall.Result{Action: toolcall.ActionPass}}
			decl := &scopedExpansionFin{
				id: "new-declarer", order: 20, spec: tc.spec,
				selected: map[string]struct{}{mandatoryToolName: {}},
			}
			a := newToolCallAssembler([]toolcall.Finalizer{decl, middle, renamer}, 0, mandatoryCatalog())
			released, err := streamToolCallAsNamedWithMeta(t, a, "bound", "unprofiled", `{}`, compositionMeta())
			var mandatory *MandatoryBufferingError
			if released != "" || !errors.As(err, &mandatory) || mandatory.Reason != tc.want ||
				mandatory.FinalizerID != decl.ID() || middle.calls != 0 || decl.calls != 0 {
				t.Fatalf("new declaration must enforce its own bound before the next finalizer: %v", err)
			}
		})
	}
}

func TestDynamicBestEffortActivationKeepsLegacyFallback(t *testing.T) {
	t.Parallel()
	for _, bytes := range []int{8 * 1024, toolcall.MinMandatoryMaxArgsBytes + 1} {
		t.Run(strconv.Itoa(bytes), func(t *testing.T) {
			t.Parallel()
			renamer := &unusableOrdinaryFin{order: 10, res: toolcall.Result{
				Action: toolcall.ActionRewrite, ToolName: mandatoryToolName, ArgsJSON: []byte(ordinaryArgsJSON(bytes)),
			}}
			decl := &scopedExpansionFin{
				id: "observer", order: 20,
				spec: toolcall.BufferingSpec{
					MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes,
					Overflow:     toolcall.OverflowPassThrough, Completeness: toolcall.CompletenessBestEffort,
				},
				selected: map[string]struct{}{mandatoryToolName: {}},
			}
			a := newToolCallAssembler([]toolcall.Finalizer{renamer, &failingOrdinaryFin{order: 11}, decl}, 0, mandatoryCatalog())
			released, err := streamToolCallAsNamedWithMeta(t, a, "observer", "unprofiled", `{}`, compositionMeta())
			if err != nil || released != `{}` || decl.calls != 0 {
				t.Fatal("requirement 7.3 - a newly applicable observer must not change legacy fallback, inside or past its bound")
			}
		})
	}
}

func TestDynamicActivationPreservesExistingRequirementsAndChainOrder(t *testing.T) {
	t.Parallel()
	spec := toolcall.BufferingSpec{MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes, Overflow: toolcall.OverflowReject}
	past := &mandatoryDeclaration{index: 0, id: "processed-invalid", valid: false}
	newlyApplicable := &mandatoryDeclaration{index: 1, id: "new", valid: true, spec: spec}
	existing := &mandatoryDeclaration{index: 2, id: "existing", valid: true, spec: spec}
	a := &toolCallAssembler{
		catalog: mandatoryCatalog(), maxArgsBytes: defaultToolCallFinalizationMaxArgsBytes,
		mandatory: mandatoryBuffering{declarations: []*mandatoryDeclaration{past, newlyApplicable, existing}},
	}
	buf := &toolCallBuffer{id: "activation", requirements: &callRequirements{limitBytes: a.maxArgsBytes}}
	buf.requirements.add(existing)
	buf.requirements.requirementFor(existing.index).pending = false
	for range 2 {
		if err := a.activateRemainingRequirements(buf, past.index, mandatoryToolName, 100); err != nil {
			t.Fatal(err)
		}
		if len(buf.requirements.items) != 2 || buf.requirements.pendingCount() != 1 ||
			buf.requirements.firstPendingID() != newlyApplicable.id ||
			buf.requirements.items[0].decl != newlyApplicable ||
			buf.requirements.requirementFor(existing.index).pending {
			t.Fatal("activation must preserve existing decisions, skip processed declarations, and avoid duplicates in chain order")
		}
	}
}
