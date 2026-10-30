package stdhttp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	refchat "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

func TestReasoningPreservationHTTP_Deterministic(t *testing.T) {
	t.Run("feature_disabled_drop_missing_at_backend", func(t *testing.T) {
		t.Parallel()
		runChatDropScenario(t, "disabled", false, false)
	})
	t.Run("action_observe_no_mutation", func(t *testing.T) {
		t.Parallel()
		runChatDropScenario(t, "observe", false, false)
	})
	t.Run("restore_drop_all_nonstream", func(t *testing.T) {
		t.Parallel()
		runChatDropScenario(t, "restore", true, false)
	})
	t.Run("restore_drop_all_streaming", func(t *testing.T) {
		t.Parallel()
		runChatDropScenario(t, "restore", true, true)
	})
	t.Run("preserve_all_no_duplicate_reorder", func(t *testing.T) {
		t.Parallel()
		runChatPreserveAll(t)
	})
	t.Run("mixed_reason_no_reason_no_synthesis", func(t *testing.T) {
		t.Parallel()
		runChatMixedNoSynthesis(t)
	})
	t.Run("reasoning_tools_restore", func(t *testing.T) {
		t.Parallel()
		runChatReasoningToolsRestore(t)
	})
	t.Run("conflict_untouched", func(t *testing.T) {
		t.Parallel()
		runChatConflictUntouched(t)
	})
	t.Run("ambiguous_duplicate_anchor_no_rewrite", func(t *testing.T) {
		t.Parallel()
		runChatAmbiguousNoRewrite(t)
	})
	t.Run("changed_anchor_no_rewrite", func(t *testing.T) {
		t.Parallel()
		runChatChangedAnchorNoRewrite(t)
	})
	t.Run("authoritative_session_isolation", func(t *testing.T) {
		t.Parallel()
		runChatSessionIsolation(t)
	})
}

func TestReasoningPreservationHTTP_ReasoningToolReplay(t *testing.T) {
	t.Parallel()
	runChatReasoningToolsRestore(t)
}

func runChatDropScenario(t *testing.T, action string, expectRestore, streaming bool) {
	t.Helper()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   101,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "answer-one",
			Reasoning:   chatReasoning("think-one"),
			Streaming:   streaming,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	scripted := []refchat.ScriptedTurn{
		{VisibleText: "answer-one", Reasoning: "think-one"},
		{VisibleText: "answer-two", Reasoning: "think-two"},
	}
	var validators []refchat.RequestValidator
	if expectRestore {
		var streamFlag *bool
		if streaming {
			v := true
			streamFlag = &v
		}
		validators = chatRestoreValidators(plan, 2, streamFlag, rpHarnessMaxTurnsPerSession)
	} else {
		validators = chatNoRestoreValidators(plan, 2)
	}
	stack := startReasoningPreservationChatStack(t, action, scripted, validators...)
	cli := &reasoninge2e.ChatWireClient{
		BaseURL:    stack.proxyURL + "/v1",
		APIKey:     rpE2EFakeKey,
		HTTPClient: stack.proxy.Client(),
		Model:      rpE2EModel,
	}
	emu := reasoninge2e.NewClientEmulator(plan)
	ctx := context.Background()

	messages, err := emu.MaterializeChatRequest("ask-1")
	if err != nil {
		t.Fatal(err)
	}
	resp1, err := cli.PostChatCompletion(ctx, streaming, messages, nil)
	requireHTTPOK(t, resp1.Status, resp1.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	if streaming {
		requireStreamWire(t, resp1)
	}
	if len(resp1.Reasoning) == 0 {
		t.Fatal("client must observe reasoning structurally; reasoning_count=0")
	}
	if cli.Carriers.SessionID == "" || cli.Carriers.ResumeToken == "" {
		t.Fatal("missing session carriers")
	}
	if err := emu.Record(reasoninge2e.ChatResponseFromTurn(resp1)); err != nil {
		t.Fatal(err)
	}

	messages, err = emu.MaterializeChatRequest("ask-2")
	if err != nil {
		t.Fatal(err)
	}
	resp2, err := cli.PostChatCompletion(ctx, streaming, messages, nil)
	requireHTTPOK(t, resp2.Status, resp2.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	if streaming {
		requireStreamWire(t, resp2)
	}
	requireLedgerOK(t, stack)
	if stack.ledger.Count() != 2 {
		t.Fatalf("oracle request_count=%d want=2", stack.ledger.Count())
	}
}

func runChatPreserveAll(t *testing.T) {
	t.Helper()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   102,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "keep-me",
			Reasoning:   chatReasoning("kept-think"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	validators := chatRestoreValidators(plan, 2, nil, rpHarnessMaxTurnsPerSession)
	stack := startReasoningPreservationChatStack(t, "restore", []refchat.ScriptedTurn{
		{VisibleText: "keep-me", Reasoning: "kept-think"},
		{VisibleText: "next", Reasoning: "next-think"},
	}, validators...)
	cli := &reasoninge2e.ChatWireClient{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey, HTTPClient: stack.proxy.Client(), Model: rpE2EModel,
	}
	emu := reasoninge2e.NewClientEmulator(plan)
	ctx := context.Background()

	msgs, err := emu.MaterializeChatRequest("u1")
	if err != nil {
		t.Fatal(err)
	}
	resp1, err := cli.PostChatCompletion(ctx, false, msgs, nil)
	requireHTTPOK(t, resp1.Status, resp1.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	if err := emu.Record(reasoninge2e.ChatResponseFromTurn(resp1)); err != nil {
		t.Fatal(err)
	}
	msgs, err = emu.MaterializeChatRequest("u2")
	if err != nil {
		t.Fatal(err)
	}
	resp2, err := cli.PostChatCompletion(ctx, false, msgs, nil)
	requireHTTPOK(t, resp2.Status, resp2.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
	sub := emu.SubmittedHistory()
	if len(sub) != 1 || len(sub[0].Reasoning) != 1 {
		t.Fatalf("preserve submitted reasoning_count=%d", len(sub[0].Reasoning))
	}
}

func runChatMixedNoSynthesis(t *testing.T) {
	t.Helper()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   103,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "with-r", Reasoning: chatReasoning("r-a")},
			{VisibleText: "no-r"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	validators := chatRestoreValidators(plan, 3, nil, rpHarnessMaxTurnsPerSession)
	stack := startReasoningPreservationChatStack(t, "restore", []refchat.ScriptedTurn{
		{VisibleText: "with-r", Reasoning: "r-a"},
		{VisibleText: "no-r"},
		{VisibleText: "final", Reasoning: "r-final"},
	}, validators...)
	cli := &reasoninge2e.ChatWireClient{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey, HTTPClient: stack.proxy.Client(), Model: rpE2EModel,
	}
	emu := reasoninge2e.NewClientEmulator(plan)
	ctx := context.Background()

	for _, prompt := range []string{"u1", "u2"} {
		msgs, err := emu.MaterializeChatRequest(prompt)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := cli.PostChatCompletion(ctx, false, msgs, nil)
		requireHTTPOK(t, resp.Status, resp.RawBody)
		if err != nil {
			t.Fatal(err)
		}
		if err := emu.Record(reasoninge2e.ChatResponseFromTurn(resp)); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := emu.MaterializeChatRequest("u3")
	if err != nil {
		t.Fatal(err)
	}
	resp3, err := cli.PostChatCompletion(ctx, false, msgs, nil)
	requireHTTPOK(t, resp3.Status, resp3.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
	obs := emu.ObservedHistory()
	if len(obs) != 2 {
		t.Fatalf("observed_turns=%d", len(obs))
	}
	if len(obs[1].Reasoning) != 0 {
		t.Fatal("no-reason turn must not synthesize reasoning")
	}
}

func runChatReasoningToolsRestore(t *testing.T) {
	t.Helper()
	tool := &reasoninge2e.ToolExchange{
		ID: "call_weather_1", Name: "get_weather", Arguments: `{"city":"NYC"}`, Result: `{"ok":true}`,
	}
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   104,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "checking",
			Reasoning:   chatReasoning("need-tool"),
			Tool:        tool,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	validators := chatRestoreValidators(plan, 2, nil, rpHarnessMaxTurnsPerSession)
	stack := startReasoningPreservationChatStack(t, "restore", []refchat.ScriptedTurn{
		{VisibleText: "checking", Reasoning: "need-tool", ToolID: tool.ID, ToolName: tool.Name, ToolArgs: tool.Arguments},
		{VisibleText: "done", Reasoning: "after-tool"},
	}, validators...)
	cli := &reasoninge2e.ChatWireClient{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey, HTTPClient: stack.proxy.Client(), Model: rpE2EModel,
	}
	emu := reasoninge2e.NewClientEmulator(plan)
	ctx := context.Background()
	tools := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name": "get_weather",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
			},
		},
	}}
	msgs, err := emu.MaterializeChatRequest("weather?")
	if err != nil {
		t.Fatal(err)
	}
	r1, err := cli.PostChatCompletion(ctx, false, msgs, tools)
	requireHTTPOK(t, r1.Status, r1.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Tool == nil || r1.Tool.ID != tool.ID || r1.Tool.Name != tool.Name || r1.Tool.Arguments != tool.Arguments {
		t.Fatalf("tool observe mismatch structurally id_ok=%v name_ok=%v args_ok=%v",
			r1.Tool != nil && r1.Tool.ID == tool.ID,
			r1.Tool != nil && r1.Tool.Name == tool.Name,
			r1.Tool != nil && r1.Tool.Arguments == tool.Arguments)
	}
	if err := emu.Record(reasoninge2e.ChatResponseFromTurn(r1)); err != nil {
		t.Fatal(err)
	}
	msgs, err = emu.MaterializeChatRequest("thanks")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := cli.PostChatCompletion(ctx, false, msgs, tools)
	requireHTTPOK(t, r2.Status, r2.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
	sub := emu.SubmittedHistory()
	if len(sub) != 1 || sub[0].Tool == nil || sub[0].Tool.Result != tool.Result {
		t.Fatal("tool fields not preserved structurally in submitted history")
	}
}

func runChatConflictUntouched(t *testing.T) {
	t.Helper()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   105,
		Policy: reasoninge2e.ConflictReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText:       "ans",
			Reasoning:         chatReasoning("observed-think"),
			ConflictReasoning: chatReasoning("client-conflict"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	validators := chatRestoreValidators(plan, 2, nil, rpHarnessMaxTurnsPerSession)
	stack := startReasoningPreservationChatStack(t, "restore", []refchat.ScriptedTurn{
		{VisibleText: "ans", Reasoning: "observed-think"},
		{VisibleText: "next"},
	}, validators...)
	cli := &reasoninge2e.ChatWireClient{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey, HTTPClient: stack.proxy.Client(), Model: rpE2EModel,
	}
	emu := reasoninge2e.NewClientEmulator(plan)
	ctx := context.Background()

	msgs, err := emu.MaterializeChatRequest("u")
	if err != nil {
		t.Fatal(err)
	}
	r1, err := cli.PostChatCompletion(ctx, false, msgs, nil)
	requireHTTPOK(t, r1.Status, r1.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	if err := emu.Record(reasoninge2e.ChatResponseFromTurn(r1)); err != nil {
		t.Fatal(err)
	}
	msgs, err = emu.MaterializeChatRequest("u2")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := cli.PostChatCompletion(ctx, false, msgs, nil)
	requireHTTPOK(t, r2.Status, r2.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
	sub := emu.SubmittedHistory()
	if len(sub) != 1 || len(sub[0].Reasoning) != 1 || sub[0].Reasoning[0].Text != "client-conflict" {
		t.Fatal("conflict rewrite detected in submitted history")
	}
}

func runChatAmbiguousNoRewrite(t *testing.T) {
	t.Helper()
	// Negative control: intentionally craft malformed/ambiguous duplicate-anchor history.
	stack := startReasoningPreservationChatStack(t, "restore", []refchat.ScriptedTurn{
		{VisibleText: "same", Reasoning: "r1"},
		{VisibleText: "same", Reasoning: "r2"},
		{VisibleText: "fin"},
	})
	cli := &reasoninge2e.ChatWireClient{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey, HTTPClient: stack.proxy.Client(), Model: rpE2EModel,
	}
	ctx := context.Background()
	r1, err := cli.PostChatCompletion(ctx, false, []map[string]any{reasoninge2e.UserMessage("a")}, nil)
	requireHTTPOK(t, r1.Status, r1.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	_ = drainOracleBodies(t, stack.oracleCh, 1)
	r2, err := cli.PostChatCompletion(ctx, false, []map[string]any{
		reasoninge2e.UserMessage("a"),
		{"role": "assistant", "content": "same"},
		reasoninge2e.UserMessage("b"),
	}, nil)
	requireHTTPOK(t, r2.Status, r2.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	_ = drainOracleBodies(t, stack.oracleCh, 1)
	r3, err := cli.PostChatCompletion(ctx, false, []map[string]any{
		reasoninge2e.UserMessage("a"),
		{"role": "assistant", "content": "same"},
		reasoninge2e.UserMessage("b"),
		{"role": "assistant", "content": "same"},
		reasoninge2e.UserMessage("c"),
	}, nil)
	requireHTTPOK(t, r3.Status, r3.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	bodies := drainOracleBodies(t, stack.oracleCh, 1)
	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(bodies[0], &req); err != nil {
		t.Fatal(err)
	}
	for _, m := range req.Messages {
		if m["role"] != "assistant" {
			continue
		}
		if _, ok := m["reasoning_content"]; ok {
			t.Fatal("ambiguous duplicate must not rewrite/insert reasoning")
		}
	}
}

func runChatChangedAnchorNoRewrite(t *testing.T) {
	t.Helper()
	// Negative control: intentionally craft changed-anchor history.
	stack := startReasoningPreservationChatStack(t, "restore", []refchat.ScriptedTurn{
		{VisibleText: "original", Reasoning: "secret"},
		{VisibleText: "next"},
	})
	cli := &reasoninge2e.ChatWireClient{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey, HTTPClient: stack.proxy.Client(), Model: rpE2EModel,
	}
	ctx := context.Background()
	r1, err := cli.PostChatCompletion(ctx, false, []map[string]any{reasoninge2e.UserMessage("u")}, nil)
	requireHTTPOK(t, r1.Status, r1.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	_ = drainOracleBodies(t, stack.oracleCh, 1)
	r2, err := cli.PostChatCompletion(ctx, false, []map[string]any{
		reasoninge2e.UserMessage("u"),
		{"role": "assistant", "content": "altered-anchor"},
		reasoninge2e.UserMessage("u2"),
	}, nil)
	requireHTTPOK(t, r2.Status, r2.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	bodies := drainOracleBodies(t, stack.oracleCh, 1)
	if strings.Contains(string(bodies[0]), `"reasoning_content"`) {
		t.Fatal("changed anchor must not restore/rewrite reasoning")
	}
}

func runChatSessionIsolation(t *testing.T) {
	t.Helper()
	stack := startReasoningPreservationChatStack(t, "restore", []refchat.ScriptedTurn{
		{VisibleText: "shared-visible", Reasoning: "sess-a-secret"},
		{VisibleText: "shared-visible", Reasoning: "sess-b-secret"},
		{VisibleText: "follow-a"},
		{VisibleText: "follow-b"},
	})
	cliA := &reasoninge2e.ChatWireClient{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey, HTTPClient: stack.proxy.Client(), Model: rpE2EModel,
	}
	cliB := &reasoninge2e.ChatWireClient{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey, HTTPClient: stack.proxy.Client(), Model: rpE2EModel,
	}
	ctx := context.Background()
	ra, err := cliA.PostChatCompletion(ctx, false, []map[string]any{reasoninge2e.UserMessage("a")}, nil)
	requireHTTPOK(t, ra.Status, ra.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	_ = drainOracleBodies(t, stack.oracleCh, 1)
	rb, err := cliB.PostChatCompletion(ctx, false, []map[string]any{reasoninge2e.UserMessage("a")}, nil)
	requireHTTPOK(t, rb.Status, rb.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	_ = drainOracleBodies(t, stack.oracleCh, 1)
	if cliA.Carriers.SessionID == "" || cliA.Carriers.SessionID == cliB.Carriers.SessionID {
		t.Fatal("sessions must be distinct")
	}
	rb2, err := cliB.PostChatCompletion(ctx, false, []map[string]any{
		reasoninge2e.UserMessage("a"),
		{"role": "assistant", "content": "shared-visible"},
		reasoninge2e.UserMessage("b"),
	}, nil)
	requireHTTPOK(t, rb2.Status, rb2.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	bodies := drainOracleBodies(t, stack.oracleCh, 1)
	if strings.Contains(string(bodies[0]), "sess-a-secret") {
		t.Fatal("cross-session reasoning leakage")
	}
	ra2, err := cliA.PostChatCompletion(ctx, false, []map[string]any{
		reasoninge2e.UserMessage("a"),
		{"role": "assistant", "content": "shared-visible"},
		reasoninge2e.UserMessage("b"),
	}, nil)
	requireHTTPOK(t, ra2.Status, ra2.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	bodiesA := drainOracleBodies(t, stack.oracleCh, 1)
	if !strings.Contains(string(bodiesA[0]), `"reasoning_content":"sess-a-secret"`) {
		t.Fatal("session A must restore its own reasoning")
	}
	if strings.Contains(string(bodiesA[0]), "sess-b-secret") {
		t.Fatal("session A must not receive B reasoning")
	}
}
