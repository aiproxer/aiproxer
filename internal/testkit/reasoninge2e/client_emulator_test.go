package reasoninge2e_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

func TestClientEmulator_materializeUsesActualVisibleTool_policyAltersReasoning(t *testing.T) {
	t.Parallel()
	tool := &reasoninge2e.ToolExchange{
		ID: "call_1", Name: "lookup", Arguments: `{"q":1}`, Result: `{"ok":true}`,
	}
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   11,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "planned-visible",
			Reasoning:   sampleReasoning("planned-think"),
			Tool:        tool,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	emu := reasoninge2e.NewClientEmulator(plan)

	msgs, err := emu.MaterializeChatRequest("ask-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Fatalf("first request must be user-only; len=%d", len(msgs))
	}

	actualTool := &reasoninge2e.ToolExchange{
		ID: "call_1", Name: "lookup", Arguments: `{"q":1}`,
	}
	if err := emu.Record(reasoninge2e.ChatResponse{
		VisibleText: "planned-visible",
		Reasoning:   sampleReasoning("planned-think"),
		Tool:        actualTool,
	}); err != nil {
		t.Fatal(err)
	}

	msgs, err = emu.MaterializeChatRequest("ask-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("want user,assistant,tool,user; got len=%d", len(msgs))
	}
	asst := msgs[1]
	if asst["role"] != "assistant" {
		t.Fatal("second message must be assistant")
	}
	if asst["content"] != "planned-visible" {
		t.Fatal("visible must come from recorded actual response")
	}
	if _, ok := asst["reasoning_content"]; ok {
		t.Fatal("drop policy must strip submitted reasoning independently of recorded observation")
	}
	tcs, ok := asst["tool_calls"].([]map[string]any)
	if !ok || len(tcs) != 1 {
		t.Fatal("tool structure must come from recorded actual response")
	}
	if tcs[0]["id"] != "call_1" {
		t.Fatal("tool id must come from recorded actual")
	}
	if msgs[2]["role"] != "tool" || msgs[2]["tool_call_id"] != "call_1" {
		t.Fatal("tool result message required after tool-bearing assistant")
	}
	if msgs[2]["content"] != `{"ok":true}` {
		t.Fatal("tool result content must be present for next request")
	}
	if msgs[3]["role"] != "user" {
		t.Fatal("next user prompt must be last")
	}

	obs := emu.ObservedHistory()
	sub := emu.SubmittedHistory()
	if len(obs) != 1 || len(sub) != 1 {
		t.Fatalf("histories len obs=%d sub=%d", len(obs), len(sub))
	}
	if len(obs[0].Reasoning) != 1 {
		t.Fatal("observed history must retain recorded reasoning")
	}
	if len(sub[0].Reasoning) != 0 {
		t.Fatal("submitted history must apply drop policy")
	}
	if sub[0].VisibleText != "planned-visible" || sub[0].Tool == nil || sub[0].Tool.ID != "call_1" {
		t.Fatal("submitted visible/tool must track recorded actual")
	}
}

func TestClientEmulator_preserveAndConflictPolicies(t *testing.T) {
	t.Parallel()
	t.Run("preserve", func(t *testing.T) {
		t.Parallel()
		plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
			Seed:   12,
			Policy: reasoninge2e.PreserveAllReasoning,
			Turns:  []reasoninge2e.TurnSpec{{VisibleText: "v", Reasoning: sampleReasoning("keep-me")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		emu := reasoninge2e.NewClientEmulator(plan)
		_, _ = emu.MaterializeChatRequest("u1")
		if err := emu.Record(reasoninge2e.ChatResponse{
			VisibleText: "v", Reasoning: sampleReasoning("keep-me"),
		}); err != nil {
			t.Fatal(err)
		}
		msgs, err := emu.MaterializeChatRequest("u2")
		if err != nil {
			t.Fatal(err)
		}
		if msgs[1]["reasoning_content"] != "keep-me" {
			t.Fatal("preserve must submit recorded/planned reasoning text")
		}
	})
	t.Run("conflict", func(t *testing.T) {
		t.Parallel()
		plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
			Seed:   13,
			Policy: reasoninge2e.ConflictReasoning,
			Turns: []reasoninge2e.TurnSpec{{
				VisibleText:       "v",
				Reasoning:         sampleReasoning("observed"),
				ConflictReasoning: sampleReasoning("client-alt"),
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		emu := reasoninge2e.NewClientEmulator(plan)
		_, _ = emu.MaterializeChatRequest("u1")
		if err := emu.Record(reasoninge2e.ChatResponse{
			VisibleText: "v", Reasoning: sampleReasoning("observed"),
		}); err != nil {
			t.Fatal(err)
		}
		msgs, err := emu.MaterializeChatRequest("u2")
		if err != nil {
			t.Fatal(err)
		}
		if msgs[1]["reasoning_content"] != "client-alt" {
			t.Fatal("conflict policy must submit alternate reasoning, not observed")
		}
		if len(emu.ObservedHistory()[0].Reasoning) != 1 || emu.ObservedHistory()[0].Reasoning[0].Text != "observed" {
			t.Fatal("observed history must stay independent of submitted conflict reasoning")
		}
	})
}

func TestClientEmulator_cannotMaterializeUnrecordedTurn(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   14,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a"},
			{VisibleText: "b"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	emu := reasoninge2e.NewClientEmulator(plan)
	_, _ = emu.MaterializeChatRequest("u1")
	if err := emu.Record(reasoninge2e.ChatResponse{VisibleText: "a"}); err != nil {
		t.Fatal(err)
	}
	// After one record, materialize for next turn is OK.
	if _, err := emu.MaterializeChatRequest("u2"); err != nil {
		t.Fatal(err)
	}
	// Recording second turn without having "closed" is fine; but materializing
	// a third request requires the second response to be recorded.
	if _, err := emu.MaterializeChatRequest("u3"); err == nil {
		t.Fatal("must not materialize beyond recorded turns")
	} else if !strings.Contains(err.Error(), "unrecorded") {
		t.Fatalf("error must mention unrecorded; got %v", err)
	}
}

func TestClientEmulator_recordValidatesAndContentSafe(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   15,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "visible-ok",
			Reasoning:   sampleReasoning("secret-payload"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	emu := reasoninge2e.NewClientEmulator(plan)
	_, _ = emu.MaterializeChatRequest("u")
	err = emu.Record(reasoninge2e.ChatResponse{
		VisibleText: "visible-wrong",
		Reasoning:   sampleReasoning("secret-payload"),
	})
	if err == nil {
		t.Fatal("record must reject visible mismatch")
	}
	msg := err.Error()
	if strings.Contains(msg, "secret-payload") || strings.Contains(msg, "visible-wrong") || strings.Contains(msg, "visible-ok") {
		t.Fatalf("record error must be content-safe; got %q", msg)
	}
	if !strings.Contains(msg, "visible_text") {
		t.Fatalf("must name structural field; got %q", msg)
	}
}

func TestClientEmulator_defensiveCopies(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   16,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "v",
			Reasoning:   sampleReasoning("r"),
			Tool: &reasoninge2e.ToolExchange{
				ID: "c1", Name: "n", Arguments: `{}`, Result: `ok`,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	emu := reasoninge2e.NewClientEmulator(plan)
	_, _ = emu.MaterializeChatRequest("u1")
	resp := reasoninge2e.ChatResponse{
		VisibleText: "v",
		Reasoning:   sampleReasoning("r"),
		Tool:        &reasoninge2e.ToolExchange{ID: "c1", Name: "n", Arguments: `{}`},
	}
	if err := emu.Record(resp); err != nil {
		t.Fatal(err)
	}
	resp.VisibleText = "mutated"
	resp.Reasoning[0].Text = "mutated"
	resp.Tool.ID = "mutated"

	obs := emu.ObservedHistory()
	obs[0].VisibleText = "mutated-hist"
	obs[0].Reasoning[0].Text = "mutated-hist"
	obs[0].Tool.ID = "mutated-hist"

	again := emu.ObservedHistory()
	if again[0].VisibleText != "v" || again[0].Reasoning[0].Text != "r" || again[0].Tool.ID != "c1" {
		t.Fatal("ObservedHistory must return defensive copies")
	}
	sub := emu.SubmittedHistory()
	sub[0].VisibleText = "mutated-sub"
	againSub := emu.SubmittedHistory()
	if againSub[0].VisibleText != "v" {
		t.Fatal("SubmittedHistory must return defensive copies")
	}
	msgs, err := emu.MaterializeChatRequest("u2")
	if err != nil {
		t.Fatal(err)
	}
	msgs[1]["content"] = "mutated-msg"
	if tc, ok := msgs[1]["tool_calls"].([]map[string]any); ok && len(tc) > 0 {
		tc[0]["id"] = "mutated-msg"
	}
	// Fresh materialize is blocked until Record; rebuild via SubmittedHistory instead.
	fresh := reasoninge2e.AssistantTurnToChatMessage(emu.SubmittedHistory()[0])
	if fresh["content"] != "v" {
		t.Fatal("submitted assistant message must not share mutable maps with materialize result")
	}
}
