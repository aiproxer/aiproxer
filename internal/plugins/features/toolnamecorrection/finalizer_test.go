package toolnamecorrection_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolnamecorrection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// The fixed sentence of the hidden correction message (design.md
// "CorrectionFinalizer and FeatureConfig"; requirements.md 4.1, 4.2).
const (
	messageHeadOpen  = `Tool "`
	messageHeadClose = `" does not exist. Available tools: `
	messageTail      = `. Call one of the available tools, or answer without calling a tool.`
)

// featureFinalizer returns the single finalizer the shipped bundle contributes
// for cfg, through the plane composition a host actually uses.
func featureFinalizer(t *testing.T, cfg toolnamecorrection.Config) toolcall.Finalizer {
	t.Helper()
	bundle, err := toolnamecorrection.FeatureBundle(cfg)
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	finalizers := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("bundle contributes %d finalizers, want exactly 1", len(finalizers))
	}
	return finalizers[0]
}

// defaultFinalizer is the finalizer a host installs for the shipped defaults.
func defaultFinalizer(t *testing.T) toolcall.Finalizer {
	t.Helper()
	cfg, err := decodeConfig(t, "{}\n")
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	return featureFinalizer(t, cfg)
}

// steerInstruction runs one completed call through the finalizer and returns the
// usable steer instruction, failing when the finalizer produces no usable steer.
func steerInstruction(t *testing.T, fin toolcall.Finalizer, name string, catalog []aipapi.ToolDef) string {
	t.Helper()
	res, err := fin.Finalize(t.Context(),
		toolcall.CompletedCall{ToolCallID: "call-1", ToolName: name},
		aipapi.ToolDef{}, catalog, toolcall.Meta{})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	steer, ok := res.UsableSteer()
	if !ok {
		t.Fatalf("Finalize(%q) produced no usable steer: action=%v reason=%q", name, res.Action, res.ReasonCode)
	}
	return steer.Instruction
}

func TestFinalizer_Decisions(t *testing.T) {
	t.Parallel()
	cfg, err := decodeConfig(t, "max_corrections_per_turn: 2\npass_through_names: [\"mcp__*\", \"exact_name\"]\n")
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	fin := featureFinalizer(t, cfg)
	catalog := []aipapi.ToolDef{
		{Name: "read", Description: "reads a file", Parameters: []byte(`{"type":"object"}`)},
		{Name: "write"},
	}

	cases := []struct {
		name       string
		call       toolcall.CompletedCall
		tool       aipapi.ToolDef
		catalog    []aipapi.ToolDef
		wantAction toolcall.Action
		wantReason string
	}{
		{
			name:       "offered tool",
			call:       toolcall.CompletedCall{ToolCallID: "call-1", ToolName: "read"},
			tool:       catalog[0],
			catalog:    catalog,
			wantAction: toolcall.ActionPass,
			wantReason: toolcall.ReasonValidPassThrough,
		},
		{
			name:       "name an earlier finalizer rewrote",
			call:       toolcall.CompletedCall{ToolCallID: "call-1", ToolName: "rad"},
			tool:       catalog[0],
			catalog:    catalog,
			wantAction: toolcall.ActionPass,
			wantReason: toolcall.ReasonValidPassThrough,
		},
		{
			name:       "pass-through glob",
			call:       toolcall.CompletedCall{ToolCallID: "call-1", ToolName: "mcp__files__read"},
			catalog:    catalog,
			wantAction: toolcall.ActionPass,
			wantReason: toolcall.ReasonValidPassThrough,
		},
		{
			name:       "pass-through exact name",
			call:       toolcall.CompletedCall{ToolCallID: "call-1", ToolName: "exact_name"},
			catalog:    catalog,
			wantAction: toolcall.ActionPass,
			wantReason: toolcall.ReasonValidPassThrough,
		},
		{
			name:       "no tools were offered",
			call:       toolcall.CompletedCall{ToolCallID: "call-1", ToolName: "nope"},
			wantAction: toolcall.ActionPass,
			wantReason: toolcall.ReasonValidPassThrough,
		},
		{
			name:       "unknown tool",
			call:       toolcall.CompletedCall{ToolCallID: "call-1", ToolName: "nope", ArgsJSON: []byte(`{}`)},
			catalog:    catalog,
			wantAction: toolcall.ActionSteer,
			wantReason: toolcall.ReasonUnknownToolName,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := fin.Finalize(t.Context(), tc.call, tc.tool, tc.catalog, toolcall.Meta{})
			if err != nil {
				t.Fatalf("Finalize: %v", err)
			}
			if res.Action != tc.wantAction || res.ReasonCode != tc.wantReason {
				t.Fatalf("result %v/%q, want %v/%q", res.Action, res.ReasonCode, tc.wantAction, tc.wantReason)
			}
			if tc.wantAction != toolcall.ActionSteer {
				if res.Steer != nil {
					t.Fatalf("pass result carries a steer request: %+v", res.Steer)
				}
				return
			}
			if res.ToolName != "" || res.ArgsJSON != nil {
				t.Fatalf("steer result must carry no rewritten call: name=%q args=%s", res.ToolName, res.ArgsJSON)
			}
			steer, ok := res.UsableSteer()
			if !ok {
				t.Fatalf("steer result is not usable: %+v", res)
			}
			want := messageHeadOpen + "nope" + messageHeadClose + `"read", "write"` + messageTail
			if steer.Instruction != want {
				t.Fatalf("instruction=%q\nwant         =%q", steer.Instruction, want)
			}
			if steer.TurnBudget != 2 || !steer.AllowAfterReasoning {
				t.Fatalf("steer budget=%d allowAfterReasoning=%t, want 2/true",
					steer.TurnBudget, steer.AllowAfterReasoning)
			}
		})
	}
}

func TestFinalizer_ZeroConfigUsesDocumentedDefaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  toolnamecorrection.Config
	}{
		{name: "zero Config", cfg: toolnamecorrection.Config{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fin := featureFinalizer(t, tc.cfg)
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
			if steer.TurnBudget != 1 || !steer.AllowAfterReasoning {
				t.Fatalf("budget=%d allowAfterReasoning=%t, want 1/true",
					steer.TurnBudget, steer.AllowAfterReasoning)
			}
		})
	}
}
