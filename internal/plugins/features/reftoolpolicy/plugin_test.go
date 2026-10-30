package reftoolpolicy_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftoolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

func TestToolCatalogFilter_dropsBlocked(t *testing.T) {
	t.Parallel()
	f := reftoolpolicy.NewToolCatalogFilter(reftoolpolicy.Config{
		BlockNames:    []string{"gone"},
		BlockPrefixes: nil,
	})
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "x"}},
		}},
		Tools: []aipapi.ToolDef{
			{Name: "ok"},
			{Name: "gone"},
			{Name: "ok2"},
		},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
	}
	if err := extensions.RunToolCatalogFilterStage(context.Background(), nil, nil, []toolcatalog.Filter{f}, &call, toolcatalog.CatalogMeta{}, toolcatalog.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if len(call.Tools) != 2 {
		t.Fatalf("tools %#v", call.Tools)
	}
	if call.Tools[0].Name != "ok" || call.Tools[1].Name != "ok2" {
		t.Fatalf("tools %#v", call.Tools)
	}
}

func TestToolReactor_swallowsBlockedName(t *testing.T) {
	t.Parallel()
	r := reftoolpolicy.NewToolReactor(reftoolpolicy.Config{
		BlockNames:    []string{"blockme"},
		BlockPrefixes: nil,
	})
	if r.ID() != reftoolpolicy.ID+"-reactor" {
		t.Fatalf("id %q", r.ID())
	}
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolName: "blockme", ToolCallID: "c1", ArgsDelta: "{}"}
	dec, out, err := r.HandleToolEvent(context.Background(), te, sdk.ToolMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != sdk.ToolSwallow || (out != aipapi.ToolEvent{}) {
		t.Fatalf("dec %v out %#v", dec, out)
	}
}

func TestToolReactor_passesUnblocked(t *testing.T) {
	t.Parallel()
	r := reftoolpolicy.NewToolReactor(reftoolpolicy.Config{BlockNames: []string{"blockme"}})
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolName: "ok", ToolCallID: "c1"}
	dec, out, err := r.HandleToolEvent(context.Background(), te, sdk.ToolMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != sdk.ToolPass || (out != aipapi.ToolEvent{}) {
		t.Fatalf("dec %v", dec)
	}
}

func TestToolReactor_blockByPrefix(t *testing.T) {
	t.Parallel()
	r := reftoolpolicy.NewToolReactor(reftoolpolicy.Config{BlockPrefixes: []string{"ref_blocked_"}})
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolName: "ref_blocked_x", ToolCallID: "1"}
	dec, out, err := r.HandleToolEvent(context.Background(), te, sdk.ToolMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != sdk.ToolSwallow || (out != aipapi.ToolEvent{}) {
		t.Fatalf("dec %v", dec)
	}
}

func TestToolCallPolicy_deniesBlockedEmittedTool(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockNames: []string{"blocked"},
	})
	if p.ID() != reftoolpolicy.ID+"-tool-policy" {
		t.Fatalf("id %q", p.ID())
	}
	ev := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolName: "blocked", ToolCallID: "tc1"}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Policies: toolpolicy.MaterializeSorted([]toolpolicy.Policy{p}),
		Event:    ev,
	})
	if err == nil {
		t.Fatal("want deny error")
	}
}

func TestToolCallPolicy_allowsUnblockedTool(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{BlockNames: []string{"blocked"}})
	ev := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolName: "ok", ToolCallID: "tc1"}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Policies: toolpolicy.MaterializeSorted([]toolpolicy.Policy{p}),
		Event:    ev,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestToolCallPolicy_emptyBlocksIsNoOp(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockNames:    []string{},
		BlockPrefixes: []string{},
	})
	ev := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolName: "anything", ToolCallID: "tc1"}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Policies: toolpolicy.MaterializeSorted([]toolpolicy.Policy{p}),
		Event:    ev,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestToolCallPolicy_Handle_DenyBlocked(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockNames: []string{"blocked"},
	})
	ev := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolName: "blocked", ToolCallID: "tc1"}
	dec, err := p.Handle(context.Background(), ev, toolpolicy.Meta{}, toolpolicy.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != toolpolicy.DecisionDeny {
		t.Fatalf("want DecisionDeny, got %v", dec)
	}
}

func TestToolCallPolicy_Handle_AllowUnblocked(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockNames: []string{"blocked"},
	})
	ev := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolName: "ok", ToolCallID: "tc1"}
	dec, err := p.Handle(context.Background(), ev, toolpolicy.Meta{}, toolpolicy.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != toolpolicy.DecisionAllow {
		t.Fatalf("want DecisionAllow, got %v", dec)
	}
}

func TestToolCallPolicy_Handle_AllowEmptyToolName(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockNames: []string{"blocked"},
	})
	// ToolName is empty, which can happen for argument delta events.
	ev := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolName: "", ToolCallID: "tc1", ArgsDelta: "{}"}
	dec, err := p.Handle(context.Background(), ev, toolpolicy.Meta{}, toolpolicy.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != toolpolicy.DecisionAllow {
		t.Fatalf("want DecisionAllow, got %v", dec)
	}
}

func TestToolCallPolicy_Handle_DenyBlockedPrefix(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockPrefixes: []string{"bad_"},
	})
	ev := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolName: "bad_tool", ToolCallID: "tc1"}
	dec, err := p.Handle(context.Background(), ev, toolpolicy.Meta{}, toolpolicy.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != toolpolicy.DecisionDeny {
		t.Fatalf("want DecisionDeny, got %v", dec)
	}
}

func TestNewToolCallPolicy(t *testing.T) {
	t.Parallel()

	t.Run("default_order", func(t *testing.T) {
		t.Parallel()
		p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{})
		// We can't access reftoolpolicy.defaultOrder directly since it's unexported and we are in _test.
		// Let's rely on the tested behavior, which is that it falls back to a non-zero value,
		// or explicitly provide 45 because we see it in code.
		if p.Order() != 45 {
			t.Errorf("expected order 45, got %d", p.Order())
		}
		expectedID := "ref-tool-policy-tool-policy"
		if p.ID() != expectedID {
			t.Errorf("expected id %q, got %q", expectedID, p.ID())
		}
		if p.FailureMode() != sdk.FailClosed {
			t.Errorf("expected failure mode %d, got %d", sdk.FailClosed, p.FailureMode())
		}
	})

	t.Run("custom_order", func(t *testing.T) {
		t.Parallel()
		customOrder := 10
		p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{Order: &customOrder})
		if p.Order() != customOrder {
			t.Errorf("expected order %d, got %d", customOrder, p.Order())
		}
	})
}
