package toolnamecorrection_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolnamecorrection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestFeatureBundle_ContributesOneFinalizerOrderedLast(t *testing.T) {
	t.Parallel()
	cfg, err := decodeConfig(t, "{}\n")
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	bundle, err := toolnamecorrection.FeatureBundle(cfg)
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatalf("bundle.Validate: %v", err)
	}
	contributed := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(contributed) != 1 {
		t.Fatalf("bundle contributes %d finalizers, want exactly 1", len(contributed))
	}
	fin := contributed[0]
	if fin.ID() != toolnamecorrection.ID {
		t.Fatalf("finalizer ID=%q, want %q", fin.ID(), toolnamecorrection.ID)
	}
	if fin.Order() <= toolcallrepair.DefaultFinalizerOrder || fin.Order() <= expansion.FinalizerOrder {
		t.Fatalf("order %d must sort after tool-call repair %d and path virtualization %d",
			fin.Order(), toolcallrepair.DefaultFinalizerOrder, expansion.FinalizerOrder)
	}

	// Execution order, not only the declared number: contribute this feature
	// first and let the real request materializer sort. Only Order() may decide
	// (requirements.md 1.3 - tool-call repair has already run when this
	// finalizer sees the call).
	repairBundle, err := toolcallrepair.FeatureBundle(toolcallrepair.Config{})
	if err != nil {
		t.Fatalf("toolcallrepair.FeatureBundle: %v", err)
	}
	repairFinalizers := aipfeature.Get(repairBundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, toolnamecorrection.ID, []toolcall.Finalizer{fin}); err != nil {
		t.Fatalf("Contribute %s: %v", toolnamecorrection.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, toolcallrepair.ID, repairFinalizers); err != nil {
		t.Fatalf("Contribute %s: %v", toolcallrepair.ID, err)
	}
	frozen := cs.Freeze()
	if err := frozen.Validate(); err != nil {
		t.Fatalf("frozen plane set: %v", err)
	}
	requestFrozen := aipfeature.FreezeRequestPlanes(frozen)
	if err := requestFrozen.Validate(); err != nil {
		t.Fatalf("request-frozen plane set: %v", err)
	}
	materialized := aipfeature.RequestExecution(requestFrozen).ToolCallFinalizers()
	if len(materialized) != 2 || materialized[0].ID() != toolcallrepair.ID || materialized[1].ID() != toolnamecorrection.ID {
		t.Fatalf("materialized order [%s], want [%s %s]",
			joinedFinalizerIDs(materialized), toolcallrepair.ID, toolnamecorrection.ID)
	}
}

func TestFeatureBundle_HandBuiltConfigStaysWithinTheSteerContract(t *testing.T) {
	t.Parallel()
	_, err := toolnamecorrection.FeatureBundle(toolnamecorrection.Config{MaxCorrectionsPerTurn: 4})
	if err == nil || !strings.Contains(err.Error(), "max_corrections_per_turn") {
		t.Fatalf("out-of-range budget: err=%v, want an error naming max_corrections_per_turn", err)
	}
	_, err = toolnamecorrection.FeatureBundle(toolnamecorrection.Config{MaxCorrectionsPerTurn: 1, PassThroughNames: []string{"["}})
	if err == nil || !strings.Contains(err.Error(), "pass_through_names") {
		t.Fatalf("malformed pattern: err=%v, want an error naming pass_through_names", err)
	}

	// A zero budget means unset and takes the shipped default, so even a
	// hand-built Config describes a usable steer request.
	fin := featureFinalizer(t, toolnamecorrection.Config{})
	res, err := fin.Finalize(t.Context(),
		toolcall.CompletedCall{ToolCallID: "call-1", ToolName: "nope"},
		aipapi.ToolDef{}, []aipapi.ToolDef{{Name: "read"}}, toolcall.Meta{})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	steer, ok := res.UsableSteer()
	if !ok {
		t.Fatalf("zero Config must still steer usably: %+v", res)
	}
	if steer.TurnBudget != 1 {
		t.Fatalf("TurnBudget=%d, want the shipped default 1", steer.TurnBudget)
	}
}

func TestFeatureBundle_ZeroConfigMatchesYamlDefaults(t *testing.T) {
	t.Parallel()
	yamlCfg, err := decodeConfig(t, "{}\n")
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	cases := []struct {
		name string
		cfg  toolnamecorrection.Config
	}{
		{name: "zero Config", cfg: toolnamecorrection.Config{}},
		{name: "empty YAML defaults", cfg: yamlCfg},
	}
	var instructions []string
	for _, tc := range cases {
		fin := featureFinalizer(t, tc.cfg)
		res, err := fin.Finalize(t.Context(),
			toolcall.CompletedCall{ToolCallID: "call-1", ToolName: "nope"},
			aipapi.ToolDef{}, []aipapi.ToolDef{{Name: "read"}}, toolcall.Meta{})
		if err != nil {
			t.Fatalf("%s Finalize: %v", tc.name, err)
		}
		steer, ok := res.UsableSteer()
		if !ok {
			t.Fatalf("%s produced no usable steer: %+v", tc.name, res)
		}
		if steer.TurnBudget != 1 || !steer.AllowAfterReasoning {
			t.Fatalf("%s budget=%d allowAfterReasoning=%t, want 1/true",
				tc.name, steer.TurnBudget, steer.AllowAfterReasoning)
		}
		instructions = append(instructions, steer.Instruction)
	}
	if instructions[0] != instructions[1] {
		t.Fatalf("zero Config instruction=%q, empty YAML instruction=%q, want identical",
			instructions[0], instructions[1])
	}
}

func joinedFinalizerIDs(finalizers []toolcall.Finalizer) string {
	ids := make([]string, 0, len(finalizers))
	for _, fin := range finalizers {
		ids = append(ids, fin.ID())
	}
	return strings.Join(ids, " ")
}
