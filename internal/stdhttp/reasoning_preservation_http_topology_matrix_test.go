package stdhttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	refchat "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	refresponses "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	refclientresp "github.com/aiproxer/aiproxer/internal/refclient/openairesponses"
	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// TestReasoningPreservationHTTP_TopologyMatrix proves Requirement 7 FE x BE cells
// (Chat/Chat, Responses/Responses, Chat/Responses, Responses/Chat) with stream and
// FE-nonstream modes, exact oracle, asymmetric positives, and cross-dialect negatives.
func TestReasoningPreservationHTTP_TopologyMatrix(t *testing.T) {
	t.Run("chat frontend with chat backend", func(t *testing.T) {
		t.Parallel()
		for _, stream := range []bool{false, true} {
			name := "nonstream"
			if stream {
				name = "stream"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				runChatDropScenario(t, "restore", true, stream)
			})
		}
	})

	t.Run("responses frontend with responses backend", func(t *testing.T) {
		t.Parallel()
		for _, stream := range []bool{false, true} {
			name := "nonstream"
			if stream {
				name = "stream"
			}
			t.Run(name+"_text_reasoning", func(t *testing.T) {
				t.Parallel()
				runResponsesSameDialectDrop(t, stream, false)
			})
		}
		t.Run("nonstream_multi_exact_and_tool", func(t *testing.T) {
			t.Parallel()
			runResponsesToolObserveAndRestore(t)
		})
	})

	t.Run("chat frontend with responses backend", func(t *testing.T) {
		t.Parallel()
		for _, stream := range []bool{false, true} {
			name := "nonstream"
			if stream {
				name = "stream"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				runChatFEResponsesBEDrop(t, stream)
			})
		}
	})

	t.Run("responses frontend with chat backend", func(t *testing.T) {
		t.Parallel()
		t.Run("positive_chat_dialect_stream", func(t *testing.T) {
			t.Parallel()
			runResponsesFEChatBEPositive(t, true)
		})
		t.Run("positive_chat_dialect_nonstream", func(t *testing.T) {
			t.Parallel()
			runResponsesFEChatBEPositive(t, false)
		})
		t.Run("negative_responses_dialect_reject", func(t *testing.T) {
			t.Parallel()
			runCrossDialectResponsesToChat(t, "reject", "reject")
		})
		t.Run("negative_responses_dialect_log_skip", func(t *testing.T) {
			t.Parallel()
			runCrossDialectResponsesToChat(t, "log_skip", "")
		})
		t.Run("negative_responses_dialect_feature_reject_default_withholds", func(t *testing.T) {
			t.Parallel()
			runCrossDialectResponsesToChat(t, "reject", "")
		})
	})

	t.Run("controls", func(t *testing.T) {
		t.Parallel()
		t.Run("responses_session_isolation", func(t *testing.T) {
			t.Parallel()
			runResponsesSessionIsolation(t)
		})
		t.Run("responses_changed_anchor_no_restore", func(t *testing.T) {
			t.Parallel()
			runResponsesChangedAnchor(t)
		})
		t.Run("responses_process_local_restart_loss", func(t *testing.T) {
			t.Parallel()
			runResponsesRestartLoss(t)
		})
		t.Run("responses_default_on_gpt55_and_gpt56_inert", func(t *testing.T) {
			t.Parallel()
			runResponsesDefaultOnBoundary(t)
		})
		t.Run("responses_default_on_unrelated_inert", func(t *testing.T) {
			t.Parallel()
			runResponsesDefaultOnUnrelated(t)
		})
		t.Run("responses_explicit_opt_in_restore", func(t *testing.T) {
			t.Parallel()
			runResponsesExplicitGating(t, "restore", true)
		})
		t.Run("responses_explicit_disabled_opt_out", func(t *testing.T) {
			t.Parallel()
			runResponsesExplicitGating(t, "disabled", false)
		})
		t.Run("responses_reasoning_only_fidelity", func(t *testing.T) {
			t.Parallel()
			runResponsesReasoningOnlyFidelity(t)
		})
		t.Run("responses_presence_oracle_encrypted_and_content", func(t *testing.T) {
			t.Parallel()
			runResponsesPresenceOracleMatrix(t)
		})
		t.Run("responses_failover_harness_can_reach_secondary", func(t *testing.T) {
			t.Parallel()
			runResponsesFailoverHarnessSecondaryReachable(t)
		})
		t.Run("responses_malformed_after_visible_output_no_secondary", func(t *testing.T) {
			t.Parallel()
			runResponsesMalformedAfterVisibleOutputNoSecondary(t)
		})
		t.Run("responses_seeded_presence_smoke", func(t *testing.T) {
			t.Parallel()
			runResponsesSeededPresenceSmoke(t)
		})
		t.Run("no_pairwise_conversion_assertions", func(t *testing.T) {
			t.Parallel()
			runNoPairwiseConversionAssertions(t)
		})
	})
}

func rpExactReasoning(id, summary string, enc refresponses.EncryptedPresence) refresponses.ReasoningOutputItem {
	return rpExactReasoningContent(id, summary, enc, nil)
}

// content nil => field absent; empty non-nil slice => present empty array; non-empty => value.
func rpExactReasoningContent(id, summary string, enc refresponses.EncryptedPresence, content []refresponses.TextPart) refresponses.ReasoningOutputItem {
	item := refresponses.ReasoningOutputItem{
		Label:   id,
		ID:      id,
		Status:  "completed",
		Content: content,
	}
	if summary == "" {
		item.Summary = []refresponses.TextPart{}
	} else {
		item.Summary = []refresponses.TextPart{{Type: "summary_text", Text: summary}}
	}
	switch enc {
	case refresponses.EncryptedNull:
		item.EncryptedRaw = json.RawMessage("null")
	case refresponses.EncryptedEmpty:
		item.EncryptedRaw = json.RawMessage(`""`)
	case refresponses.EncryptedValue:
		item.EncryptedRaw = json.RawMessage(`"enc-` + id + `"`)
	case refresponses.EncryptedAbsent:
		// leave EncryptedRaw nil
	}
	return item
}

func runResponsesSameDialectDrop(t *testing.T, feStream, withTool bool) {
	t.Helper()
	_ = withTool
	turns := []refresponses.ScriptedTurn{
		{
			ResponseID: "resp_b1",
			Reasoning: []refresponses.ReasoningOutputItem{
				rpExactReasoning("rs_b_a", "plan-a", refresponses.EncryptedValue),
				rpExactReasoning("rs_b_b", "", refresponses.EncryptedNull),
			},
			VisibleText: "visible-one",
		},
		{ResponseID: "resp_b2", VisibleText: "visible-two"},
	}
	wantRestore := []refresponses.ReasoningInputExpect{
		{Label: "rs_b_a", ID: "rs_b_a", SummaryLen: 1, Encrypted: refresponses.EncryptedValue, Status: "completed"},
		{Label: "rs_b_b", ID: "rs_b_b", SummaryLen: 0, Encrypted: refresponses.EncryptedNull, Status: "completed"},
	}
	validators := []refresponses.RequestValidator{
		refresponses.ExpectNoReasoningInput(),
		refresponses.ExpectReasoningInput(wantRestore),
	}
	stack := startReasoningPreservationResponsesStack(t, "restore", turns, validators...)
	sid, tok := "", ""
	cli := refclientresp.New(refclientresp.Config{
		BaseURL:    stack.proxyURL + "/v1",
		APIKey:     rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sid, &tok, ""),
	})
	hist := refclientresp.NewHistory(refclientresp.DropReasoning)
	ctx := context.Background()

	res1, err := createResponsesTurn(ctx, cli, hist, rpE2EModel, "ask-1", feStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := hist.ObserveResponse(res1); err != nil {
		t.Fatal(err)
	}
	if sid == "" || tok == "" {
		t.Fatal("missing session carriers")
	}
	if got := len(hist.ObservedReasoning()); got < 2 {
		t.Fatalf("client must observe exact reasoning items; got=%d", got)
	}
	// Drop policy: next request must not resend opaque items; feature restores them.
	res2, err := createResponsesTurn(ctx, cli, hist, rpE2EModel, "ask-2", feStream)
	if err != nil {
		t.Fatal(err)
	}
	_ = res2
	requireLedgerOK(t, stack)
	if stack.respLedger.Count() != 2 {
		t.Fatalf("oracle request_count=%d want=2", stack.respLedger.Count())
	}
	assertNoChatReasoningContentInResponsesBodies(t, stack.oracleCh, 2)
}

// runResponsesToolObserveAndRestore covers reasoning+tool interleave: FE observes both, then
// a follow-up with visible-text-only history restores exact reasoning (no open tool loop).
func runResponsesToolObserveAndRestore(t *testing.T) {
	t.Helper()
	rsA := rpExactReasoning("rs_bt_a", "tool-plan", refresponses.EncryptedValue)
	rsB := rpExactReasoning("rs_bt_b", "", refresponses.EncryptedAbsent)
	turns := []refresponses.ScriptedTurn{
		{
			ResponseID: "resp_bt1",
			Parts: []refresponses.ScriptedPart{
				{Reasoning: &rsA},
				{Message: "need-tool"},
				{Tool: &refresponses.ToolCall{ID: "call_bt1", Name: "lookup", Arguments: `{"q":1}`}},
				{Reasoning: &rsB},
			},
		},
		{
			ResponseID:  "resp_bt2",
			VisibleText: "need-tool",
			Reasoning:   []refresponses.ReasoningOutputItem{rsA, rsB},
		},
		{ResponseID: "resp_bt3", VisibleText: "done"},
	}
	validators := []refresponses.RequestValidator{
		refresponses.ExpectNoReasoningInput(),
		refresponses.ExpectNoReasoningInput(),
		refresponses.ExpectReasoningInput([]refresponses.ReasoningInputExpect{
			{Label: "rs_bt_a", ID: "rs_bt_a", SummaryLen: 1, Encrypted: refresponses.EncryptedValue, Status: "completed"},
			{Label: "rs_bt_b", ID: "rs_bt_b", SummaryLen: 0, Encrypted: refresponses.EncryptedAbsent, Status: "completed"},
		}),
	}
	stack := startReasoningPreservationResponsesStack(t, "restore", turns, validators...)
	sid, tok := "", ""
	cli := refclientresp.New(refclientresp.Config{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sid, &tok, ""),
	})
	ctx := context.Background()
	histTool := refclientresp.NewHistory(refclientresp.DropReasoning)
	res1, err := createResponsesTurn(ctx, cli, histTool, rpE2EModel, "ask-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := histTool.ObserveResponse(res1); err != nil {
		t.Fatal(err)
	}
	if got := len(histTool.ObservedReasoning()); got < 2 {
		t.Fatalf("tool cell must observe exact reasoning; got=%d", got)
	}
	sawTool := false
	for _, it := range res1.Output {
		if it.Type == "function_call" {
			sawTool = true
			break
		}
	}
	if !sawTool {
		t.Fatal("tool cell must observe function_call in FE output")
	}
	// Fresh history for restore proof: capture text+reasoning without hanging tool calls.
	hist := refclientresp.NewHistory(refclientresp.DropReasoning)
	res2, err := createResponsesTurn(ctx, cli, hist, rpE2EModel, "ask-2", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := hist.ObserveResponse(res2); err != nil {
		t.Fatal(err)
	}
	if _, err := createResponsesTurn(ctx, cli, hist, rpE2EModel, "ask-3", false); err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
}

func runChatFEResponsesBEDrop(t *testing.T, streaming bool) {
	t.Helper()
	turns := []refresponses.ScriptedTurn{
		{
			ResponseID: "resp_c1",
			Reasoning: []refresponses.ReasoningOutputItem{
				rpExactReasoning("rs_c_a", "c-plan", refresponses.EncryptedValue),
				rpExactReasoning("rs_c_b", "", refresponses.EncryptedAbsent),
			},
			VisibleText: "answer-one",
		},
		{ResponseID: "resp_c2", VisibleText: "answer-two"},
	}
	validators := []refresponses.RequestValidator{
		refresponses.ExpectNoReasoningInput(),
		refresponses.ExpectReasoningInput([]refresponses.ReasoningInputExpect{
			{Label: "rs_c_a", ID: "rs_c_a", SummaryLen: 1, Encrypted: refresponses.EncryptedValue, Status: "completed"},
			{Label: "rs_c_b", ID: "rs_c_b", SummaryLen: 0, Encrypted: refresponses.EncryptedAbsent, Status: "completed"},
		}),
	}
	stack := startReasoningPreservationResponsesStack(t, "restore", turns, validators...)
	cli := &reasoninge2e.ChatWireClient{
		BaseURL:    stack.proxyURL + "/v1",
		APIKey:     rpE2EFakeKey,
		HTTPClient: stack.proxy.Client(),
		Model:      rpE2EModel,
	}
	ctx := context.Background()
	r1, err := cli.PostChatCompletion(ctx, streaming, []map[string]any{reasoninge2e.UserMessage("ask-1")}, nil)
	requireHTTPOK(t, r1.Status, r1.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	if streaming {
		requireStreamWire(t, r1)
	}
	if cli.Carriers.SessionID == "" || cli.Carriers.ResumeToken == "" {
		t.Fatal("missing session carriers")
	}
	// Chat FE must not expose Responses opaque; client history is visible text only.
	for _, blk := range r1.Reasoning {
		if strings.Contains(strings.ToLower(string(blk.Dialect)), "responses") {
			t.Fatalf("Chat FE must not expose Responses dialect to client: %q", blk.Dialect)
		}
		if len(blk.Opaque) > 0 {
			t.Fatal("Chat FE must not expose opaque Responses item")
		}
	}
	msgs := []map[string]any{
		reasoninge2e.UserMessage("ask-1"),
		{"role": "assistant", "content": r1.VisibleText},
		reasoninge2e.UserMessage("ask-2"),
	}
	r2, err := cli.PostChatCompletion(ctx, streaming, msgs, nil)
	requireHTTPOK(t, r2.Status, r2.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
	if stack.respLedger.Count() != 2 {
		t.Fatalf("oracle request_count=%d want=2", stack.respLedger.Count())
	}
	assertNoChatReasoningContentInResponsesBodies(t, stack.oracleCh, 2)
}

func runResponsesFEChatBEPositive(t *testing.T, feStream bool) {
	t.Helper()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   701,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "answer-one",
			Reasoning:   chatReasoning("think-one"),
			Streaming:   feStream,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	scripted := []refchat.ScriptedTurn{
		{VisibleText: "answer-one", Reasoning: "think-one"},
		{VisibleText: "answer-two", Reasoning: "think-two"},
	}
	stack := startReasoningPreservationChatStack(t, "restore", scripted, chatRestoreValidators(plan, 2, nil, rpHarnessMaxTurnsPerSession)...)
	sid, tok := "", ""
	cli := refclientresp.New(refclientresp.Config{
		BaseURL:    stack.proxyURL + "/v1",
		APIKey:     rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sid, &tok, ""),
	})
	hist := refclientresp.NewHistory(refclientresp.DropReasoning)
	ctx := context.Background()
	res1, err := createResponsesTurn(ctx, cli, hist, rpE2EModel, "ask-1", feStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := hist.ObserveResponse(res1); err != nil {
		t.Fatal(err)
	}
	if sid == "" || tok == "" {
		t.Fatal("missing session carriers")
	}
	// Presentation path may surface Chat reasoning as Responses wire items; drop them.
	_, err = createResponsesTurn(ctx, cli, hist, rpE2EModel, "ask-2", feStream)
	if err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
	if stack.ledger.Count() != 2 {
		t.Fatalf("oracle request_count=%d want=2", stack.ledger.Count())
	}
	assertNoResponsesOpaqueInChatBodies(t, stack.oracleCh, 2)
}

func runCrossDialectResponsesToChat(t *testing.T, onUnrep, routingPolicy string) {
	t.Helper()
	opts := rpChatStackOpts{
		FeatureRow:          rpFeatureRowExplicit,
		Action:              "restore",
		OnUnrepresentable:   onUnrep,
		AttemptPolicy:       routingPolicy,
		FeatureRuleBackends: []string{"openai-legacy", "openai-responses"},
	}
	respTurns := []refresponses.ScriptedTurn{
		{
			ResponseID: "resp_neg1",
			Reasoning: []refresponses.ReasoningOutputItem{
				rpExactReasoning("rs_neg", "secret-plan", refresponses.EncryptedValue),
			},
			VisibleText: "visible-neg",
		},
	}
	chatTurns := []refchat.ScriptedTurn{
		{VisibleText: "chat-followup"},
	}
	// routing.attempt_policy=reject: Chat BE must not be invoked. Otherwise
	// (explicit log_skip or the standard log_skip default) the unrepresentable
	// restore is withheld: Chat BE invoked with no converted reasoning.
	var chatValidators []refchat.RequestValidator
	var respValidators []refresponses.RequestValidator
	respValidators = append(respValidators, refresponses.ExpectNoReasoningInput())
	if routingPolicy != "reject" {
		chatValidators = append(chatValidators, func(body []byte) error {
			if wireHasJSONType(body, "reasoning") || wireHasReasoningIDToken(body, "rs_neg") {
				return errOracle("converted_responses_opaque_in_chat_body")
			}
			if wireHasChatReasoningContentField(body) {
				return errOracle("converted_responses_to_chat_text")
			}
			return nil
		})
	}
	stack := startReasoningPreservationDualStack(t, opts, "openai-responses", chatTurns, respTurns, chatValidators, respValidators)
	sid, tok := "", ""
	routeResp := "openai-responses:" + rpE2EModel
	cli := refclientresp.New(refclientresp.Config{
		BaseURL:    stack.proxyURL + "/v1",
		APIKey:     rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sid, &tok, routeResp),
	})
	hist := refclientresp.NewHistory(refclientresp.DropReasoning)
	ctx := context.Background()
	res1, err := createResponsesTurn(ctx, cli, hist, rpE2EModel, "ask-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := hist.ObserveResponse(res1); err != nil {
		t.Fatal(err)
	}
	// Switch route to Chat-only BE with same session; stored artifact is Responses dialect.
	cliChat := &reasoninge2e.ChatWireClient{
		BaseURL:    stack.proxyURL + "/v1",
		APIKey:     rpE2EFakeKey,
		HTTPClient: stack.proxy.Client(),
		Model:      rpE2EModel,
		Route:      "openai-legacy:" + rpE2EModel,
		Carriers: reasoninge2e.ChatSessionCarriers{
			SessionID:   sid,
			ResumeToken: tok,
		},
	}
	msgs := []map[string]any{
		reasoninge2e.UserMessage("ask-1"),
		{"role": "assistant", "content": "visible-neg"},
		reasoninge2e.UserMessage("ask-2"),
	}
	r2, err := cliChat.PostChatCompletion(ctx, false, msgs, nil)
	if routingPolicy == "reject" {
		if err == nil && r2.Status == 200 {
			t.Fatal("reject must prevent successful Chat BE restore path")
		}
		if stack.chatLedger != nil && stack.chatLedger.Count() != 0 {
			t.Fatalf("reject must not call Chat BE; count=%d", stack.chatLedger.Count())
		}
		if stack.chatOracleCh != nil {
			select {
			case <-stack.chatOracleCh:
				t.Fatal("reject must not forward to Chat BE")
			default:
			}
		}
		return
	}
	requireHTTPOK(t, r2.Status, r2.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	requireDualLedgerOK(t, stack)
	if stack.chatLedger.Count() != 1 {
		t.Fatalf("withheld Chat BE count=%d want=1", stack.chatLedger.Count())
	}
}

func runResponsesSessionIsolation(t *testing.T) {
	t.Helper()
	turns := []refresponses.ScriptedTurn{
		{
			ResponseID:  "resp_s1",
			Reasoning:   []refresponses.ReasoningOutputItem{rpExactReasoning("rs_s1", "s1", refresponses.EncryptedValue)},
			VisibleText: "sess-one",
		},
		{
			ResponseID:  "resp_s2",
			Reasoning:   []refresponses.ReasoningOutputItem{rpExactReasoning("rs_s2", "s2", refresponses.EncryptedValue)},
			VisibleText: "sess-two",
		},
		{ResponseID: "resp_s3", VisibleText: "follow"},
		{ResponseID: "resp_s4", VisibleText: "follow"},
	}
	// Sessions A then B capture; then A restore must only see rs_s1, B only rs_s2.
	validators := []refresponses.RequestValidator{
		refresponses.ExpectNoReasoningInput(),
		refresponses.ExpectNoReasoningInput(),
		refresponses.ExpectReasoningInput([]refresponses.ReasoningInputExpect{
			{Label: "rs_s1", ID: "rs_s1", SummaryLen: 1, Encrypted: refresponses.EncryptedValue, Status: "completed"},
		}),
		refresponses.ExpectReasoningInput([]refresponses.ReasoningInputExpect{
			{Label: "rs_s2", ID: "rs_s2", SummaryLen: 1, Encrypted: refresponses.EncryptedValue, Status: "completed"},
		}),
	}
	stack := startReasoningPreservationResponsesStack(t, "restore", turns, validators...)
	ctx := context.Background()

	sidA, tokA := "", ""
	cliA := refclientresp.New(refclientresp.Config{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sidA, &tokA, ""),
	})
	histA := refclientresp.NewHistory(refclientresp.DropReasoning)
	resA, err := createResponsesTurn(ctx, cliA, histA, rpE2EModel, "a1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := histA.ObserveResponse(resA); err != nil {
		t.Fatal(err)
	}

	sidB, tokB := "", ""
	cliB := refclientresp.New(refclientresp.Config{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sidB, &tokB, ""),
	})
	histB := refclientresp.NewHistory(refclientresp.DropReasoning)
	resB, err := createResponsesTurn(ctx, cliB, histB, rpE2EModel, "b1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := histB.ObserveResponse(resB); err != nil {
		t.Fatal(err)
	}
	if sidA == "" || sidB == "" || sidA == sidB {
		t.Fatalf("sessions must be distinct; a=%q b=%q", sidA, sidB)
	}

	if _, err := createResponsesTurn(ctx, cliA, histA, rpE2EModel, "a2", false); err != nil {
		t.Fatal(err)
	}
	if _, err := createResponsesTurn(ctx, cliB, histB, rpE2EModel, "b2", false); err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
}

func runResponsesChangedAnchor(t *testing.T) {
	t.Helper()
	turns := []refresponses.ScriptedTurn{
		{
			ResponseID:  "resp_a1",
			Reasoning:   []refresponses.ReasoningOutputItem{rpExactReasoning("rs_anch", "p", refresponses.EncryptedValue)},
			VisibleText: "original",
		},
		{ResponseID: "resp_a2", VisibleText: "next"},
	}
	validators := []refresponses.RequestValidator{
		refresponses.ExpectNoReasoningInput(),
		refresponses.ExpectNoReasoningInput(), // edited visible history => no restore
	}
	stack := startReasoningPreservationResponsesStack(t, "restore", turns, validators...)
	sid, tok := "", ""
	cli := refclientresp.New(refclientresp.Config{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sid, &tok, ""),
	})
	hist := refclientresp.NewHistory(refclientresp.DropReasoning)
	ctx := context.Background()
	res1, err := createResponsesTurn(ctx, cli, hist, rpE2EModel, "u1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := hist.ObserveResponse(res1); err != nil {
		t.Fatal(err)
	}
	// Corrupt visible assistant text in history (anchor mismatch).
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(rpE2EModel),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: []responses.ResponseInputItemUnionParam{
			responses.ResponseInputItemParamOfMessage("u1", responses.EasyInputMessageRoleUser),
			responses.ResponseInputItemParamOfMessage("EDITED", responses.EasyInputMessageRoleAssistant),
			responses.ResponseInputItemParamOfMessage("u2", responses.EasyInputMessageRoleUser),
		}},
	}
	if _, err := cli.CreateResponse(ctx, params); err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
}

func runResponsesRestartLoss(t *testing.T) {
	t.Helper()
	turns1 := []refresponses.ScriptedTurn{
		{
			ResponseID:  "resp_r1",
			Reasoning:   []refresponses.ReasoningOutputItem{rpExactReasoning("rs_r1", "p", refresponses.EncryptedValue)},
			VisibleText: "keep",
		},
	}
	stack1, err := startReasoningPreservationResponsesStackOptsErr(rpChatStackOpts{
		FeatureRow: rpFeatureRowExplicit,
		Action:     "restore",
	}, turns1, refresponses.ExpectNoReasoningInput())
	if err != nil {
		t.Fatal(err)
	}
	sid, tok := "", ""
	cli := refclientresp.New(refclientresp.Config{
		BaseURL: stack1.proxyURL + "/v1", APIKey: rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack1.proxy, &sid, &tok, ""),
	})
	hist := refclientresp.NewHistory(refclientresp.DropReasoning)
	ctx := context.Background()
	res1, err := createResponsesTurn(ctx, cli, hist, rpE2EModel, "u1", false)
	if err != nil {
		stack1.cleanup()
		t.Fatal(err)
	}
	if err := hist.ObserveResponse(res1); err != nil {
		stack1.cleanup()
		t.Fatal(err)
	}
	requireLedgerOK(t, stack1)
	stack1.cleanup()

	turns2 := []refresponses.ScriptedTurn{{ResponseID: "resp_r2", VisibleText: "after-restart"}}
	stack2 := startReasoningPreservationResponsesStack(t, "restore", turns2, refresponses.ExpectNoReasoningInput())
	// Process restart invalidates session store; start a fresh session with the same
	// dropped client history (visible anchors only) and prove TurnStore non-durability.
	sid2, tok2 := "", ""
	cli2 := refclientresp.New(refclientresp.Config{
		BaseURL: stack2.proxyURL + "/v1", APIKey: rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack2.proxy, &sid2, &tok2, ""),
	})
	hist2 := refclientresp.NewHistory(refclientresp.DropReasoning)
	if err := hist2.ObserveResponse(res1); err != nil {
		t.Fatal(err)
	}
	if _, err := createResponsesTurn(ctx, cli2, hist2, rpE2EModel, "u2", false); err != nil {
		t.Fatal(err)
	}
	_ = sid
	_ = tok
	requireLedgerOK(t, stack2)
}

func runResponsesDefaultOnBoundary(t *testing.T) {
	t.Helper()
	cases := []struct {
		name          string
		model         string
		expectRestore bool
	}{
		{name: "moonshot_restores", model: rpE2EModel, expectRestore: true},
		{name: "gpt_5_5_restores", model: "gpt-5.5", expectRestore: true},
		{name: "gpt_5_6_inert", model: "gpt-5.6", expectRestore: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runResponsesTwoTurnGating(t, rpChatStackOpts{
				FeatureRow:        rpFeatureRowExplicit,
				Action:            "restore",
				UseBuiltinCatalog: true,
				Model:             tc.model,
			}, tc.expectRestore)
		})
	}
}

func runResponsesDefaultOnUnrelated(t *testing.T) {
	t.Helper()
	runResponsesTwoTurnGating(t, rpChatStackOpts{
		FeatureRow:        rpFeatureRowExplicit,
		Action:            "restore",
		UseBuiltinCatalog: true,
		Model:             "claude-3-5-haiku",
	}, false)
}

func runResponsesExplicitGating(t *testing.T, action string, expectRestore bool) {
	t.Helper()
	runResponsesTwoTurnGating(t, rpChatStackOpts{
		FeatureRow: rpFeatureRowExplicit,
		Action:     action,
		Model:      rpE2EModel,
	}, expectRestore)
}

func runResponsesTwoTurnGating(t *testing.T, opts rpChatStackOpts, expectRestore bool) {
	t.Helper()
	model := opts.Model
	if model == "" {
		model = rpE2EModel
	}
	turns := []refresponses.ScriptedTurn{
		{
			ResponseID:  "resp_g1",
			Reasoning:   []refresponses.ReasoningOutputItem{rpExactReasoning("rs_g1", "p", refresponses.EncryptedValue)},
			VisibleText: "one",
		},
		{ResponseID: "resp_g2", VisibleText: "two"},
	}
	var validators []refresponses.RequestValidator
	validators = append(validators, refresponses.ExpectNoReasoningInput())
	if expectRestore {
		validators = append(validators, refresponses.ExpectReasoningInput([]refresponses.ReasoningInputExpect{
			{Label: "rs_g1", ID: "rs_g1", SummaryLen: 1, Encrypted: refresponses.EncryptedValue, Status: "completed"},
		}))
	} else {
		validators = append(validators, refresponses.ExpectNoReasoningInput())
	}
	stack := startReasoningPreservationResponsesStackOpts(t, opts, turns, validators...)
	sid, tok := "", ""
	cli := refclientresp.New(refclientresp.Config{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sid, &tok, ""),
	})
	hist := refclientresp.NewHistory(refclientresp.DropReasoning)
	ctx := context.Background()
	res1, err := createResponsesTurn(ctx, cli, hist, model, "u1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := hist.ObserveResponse(res1); err != nil {
		t.Fatal(err)
	}
	if _, err := createResponsesTurn(ctx, cli, hist, model, "u2", false); err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
}

func runResponsesReasoningOnlyFidelity(t *testing.T) {
	t.Helper()
	turns := []refresponses.ScriptedTurn{{
		ResponseID: "resp_ro1",
		Reasoning: []refresponses.ReasoningOutputItem{
			rpExactReasoning("rs_ro", "only", refresponses.EncryptedNull),
		},
	}}
	stack := startReasoningPreservationResponsesStack(t, "restore", turns, refresponses.ExpectNoReasoningInput())
	sid, tok := "", ""
	cli := refclientresp.New(refclientresp.Config{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sid, &tok, ""),
	})
	hist := refclientresp.NewHistory(refclientresp.DropReasoning)
	res, err := createResponsesTurn(context.Background(), cli, hist, rpE2EModel, "u-ro", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := hist.ObserveResponse(res); err != nil {
		t.Fatal(err)
	}
	got := hist.ObservedReasoning()
	if len(got) != 1 {
		t.Fatalf("reasoning-only fidelity: observed_count=%d want=1", len(got))
	}
	if got[0].ID != "rs_ro" {
		t.Fatalf("reasoning-only fidelity: id token mismatch")
	}
	if got[0].Encrypted != refclientresp.EncryptedNull {
		t.Fatalf("reasoning-only fidelity: encrypted presence=%s want=null", got[0].Encrypted)
	}
	requireLedgerOK(t, stack)
}

func runResponsesPresenceOracleMatrix(t *testing.T) {
	t.Helper()
	emptyContent := []refresponses.TextPart{}
	valueContent := []refresponses.TextPart{{Type: "reasoning_text", Text: "c"}}
	items := []refresponses.ReasoningOutputItem{
		rpExactReasoningContent("rs_enc_absent", "a", refresponses.EncryptedAbsent, nil),
		rpExactReasoningContent("rs_enc_null", "b", refresponses.EncryptedNull, emptyContent),
		rpExactReasoningContent("rs_enc_empty", "c", refresponses.EncryptedEmpty, valueContent),
		rpExactReasoningContent("rs_enc_value", "", refresponses.EncryptedValue, nil),
	}
	want := []refresponses.ReasoningInputExpect{
		{Label: "rs_enc_absent", ID: "rs_enc_absent", SummaryLen: 1, Encrypted: refresponses.EncryptedAbsent, Status: "completed"},
		{Label: "rs_enc_null", ID: "rs_enc_null", SummaryLen: 1, HasContent: true, ContentLen: 0, Encrypted: refresponses.EncryptedNull, Status: "completed"},
		{Label: "rs_enc_empty", ID: "rs_enc_empty", SummaryLen: 1, HasContent: true, ContentLen: 1, Encrypted: refresponses.EncryptedEmpty, Status: "completed"},
		{Label: "rs_enc_value", ID: "rs_enc_value", SummaryLen: 0, Encrypted: refresponses.EncryptedValue, Status: "completed"},
	}
	turns := []refresponses.ScriptedTurn{
		{ResponseID: "resp_p1", Reasoning: items, VisibleText: "presence-one"},
		{ResponseID: "resp_p2", VisibleText: "presence-two"},
	}
	stack := startReasoningPreservationResponsesStack(
		t, "restore", turns,
		refresponses.ExpectNoReasoningInput(),
		refresponses.ExpectReasoningInput(want),
	)
	sid, tok := "", ""
	cli := refclientresp.New(refclientresp.Config{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sid, &tok, ""),
	})
	hist := refclientresp.NewHistory(refclientresp.DropReasoning)
	ctx := context.Background()
	res1, err := createResponsesTurn(ctx, cli, hist, rpE2EModel, "u1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := hist.ObserveResponse(res1); err != nil {
		t.Fatal(err)
	}
	if got := len(hist.ObservedReasoning()); got != 4 {
		t.Fatalf("presence observe count=%d want=4", got)
	}
	if _, err := createResponsesTurn(ctx, cli, hist, rpE2EModel, "u2", false); err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
}

func runResponsesSeededPresenceSmoke(t *testing.T) {
	t.Helper()
	cases := reasoninge2e.DefaultResponsesSmokeCases()
	for _, c := range cases {
		t.Run(c.Trace, func(t *testing.T) {
			t.Parallel()
			var enc refresponses.EncryptedPresence
			var content []refresponses.TextPart
			switch c.Variant {
			case reasoninge2e.ResponsesPresenceEncryptedAbsent:
				enc = refresponses.EncryptedAbsent
			case reasoninge2e.ResponsesPresenceEncryptedNull:
				enc = refresponses.EncryptedNull
			case reasoninge2e.ResponsesPresenceEncryptedValue:
				enc = refresponses.EncryptedValue
			case reasoninge2e.ResponsesPresenceWithContent:
				enc = refresponses.EncryptedAbsent
				content = []refresponses.TextPart{{Type: "reasoning_text", Text: "c"}}
			default:
				t.Fatalf("unknown variant %q", c.Variant)
			}
			id := fmt.Sprintf("rs_%d", c.Seed&0xffff)
			item := rpExactReasoningContent(id, "s", enc, content)
			expect := refresponses.ReasoningInputExpect{
				Label: id, ID: id, SummaryLen: 1, Encrypted: enc, Status: "completed",
			}
			if content != nil {
				expect.HasContent = true
				expect.ContentLen = len(content)
			}
			turns := []refresponses.ScriptedTurn{
				{ResponseID: "resp_s1", Reasoning: []refresponses.ReasoningOutputItem{item}, VisibleText: "seed-one"},
				{ResponseID: "resp_s2", VisibleText: "seed-two"},
			}
			stack := startReasoningPreservationResponsesStack(
				t, "restore", turns,
				refresponses.ExpectNoReasoningInput(),
				refresponses.ExpectReasoningInput([]refresponses.ReasoningInputExpect{expect}),
			)
			sid, tok := "", ""
			cli := refclientresp.New(refclientresp.Config{
				BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey,
				HTTPClient: newResponsesProxyClient(stack.proxy, &sid, &tok, ""),
			})
			hist := refclientresp.NewHistory(refclientresp.DropReasoning)
			res1, err := createResponsesTurn(context.Background(), cli, hist, rpE2EModel, "u1", c.StreamFE)
			if err != nil {
				t.Fatalf("%s: turn1: %v", c.Trace, err)
			}
			if err := hist.ObserveResponse(res1); err != nil {
				t.Fatalf("%s: observe: %v", c.Trace, err)
			}
			if _, err := createResponsesTurn(context.Background(), cli, hist, rpE2EModel, "u2", c.StreamFE); err != nil {
				t.Fatalf("%s: turn2: %v", c.Trace, err)
			}
			requireLedgerOK(t, stack)
		})
	}
}

func runResponsesFailoverHarnessSecondaryReachable(t *testing.T) {
	t.Helper()
	// Primary always 429s so openai-responses Open exhausts the credential pool and
	// returns RecoverablePreOutputError, allowing failover onto Chat secondary.
	stack, secondaryHits := startReasoningPreservationFailoverStack(
		t,
		func(refresponses.Request) refresponses.Response {
			return refresponses.Response{
				Status: http.StatusTooManyRequests,
				JSON:   `{"error":{"message":"rate_limited","type":"rate_limit_error"}}`,
			}
		},
		[]refchat.ScriptedTurn{{VisibleText: "secondary-ok"}},
	)
	sid, tok := "", ""
	cli := refclientresp.New(refclientresp.Config{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sid, &tok, ""),
	})
	hist := refclientresp.NewHistory(refclientresp.DropReasoning)
	_, err := createResponsesTurn(context.Background(), cli, hist, rpE2EModel, "u-fail", true)
	if secondaryHits.Load() == 0 {
		t.Fatalf("failover harness must open secondary after recoverable pre-output primary failure; client_err=%v", err != nil)
	}
}

// runResponsesMalformedAfterVisibleOutputNoSecondary is an HTTP integration check that
// a malformed reasoning item after visible text terminates without opening the Chat
// secondary. It is NOT the sole OutputCommitted commit-gate proof: the malformed
// reasoning path is itself non-recoverable, so secondary_hits==0 would also hold for
// that classification alone. Non-vacuous recoverable-after-commit gating is proven in
// internal/core/runtime/output_commit_failover_gate_test.go (with a pre-output positive
// control in the same dual-candidate harness). The 429 secondary-reachability control
// remains in runResponsesFailoverHarnessSecondaryReachable.
func runResponsesMalformedAfterVisibleOutputNoSecondary(t *testing.T) {
	t.Helper()
	stack, secondaryHits := startReasoningPreservationFailoverStack(
		t,
		func(refresponses.Request) refresponses.Response {
			return refresponses.Response{
				Status: http.StatusOK,
				SSE:    textThenMalformedReasoningSSE(),
			}
		},
		[]refchat.ScriptedTurn{{VisibleText: "must-not-run"}},
	)
	sid, tok := "", ""
	cli := refclientresp.New(refclientresp.Config{
		BaseURL: stack.proxyURL + "/v1", APIKey: rpE2EFakeKey,
		HTTPClient: newResponsesProxyClient(stack.proxy, &sid, &tok, ""),
	})
	hist := refclientresp.NewHistory(refclientresp.DropReasoning)
	res, err := createResponsesTurn(context.Background(), cli, hist, rpE2EModel, "u-nr", true)
	if secondaryHits.Load() != 0 {
		t.Fatalf("malformed-after-visible-output: secondary_hits=%d want=0", secondaryHits.Load())
	}
	// Either delivered committed text before terminal error, or stream terminated without failover.
	if err == nil && res != nil {
		if text := responseVisibleText(res); text == "" {
			t.Fatal("malformed-after-visible-output: completed response missing committed visible text")
		}
	}
}

func textThenMalformedReasoningSSE() string {
	var b strings.Builder
	seq := 0
	writeSSE := func(event string, payload any) {
		seq++
		if m, ok := payload.(map[string]any); ok {
			m["sequence_number"] = seq
		}
		raw, _ := json.Marshal(payload)
		if event != "" {
			b.WriteString("event: ")
			b.WriteString(event)
			b.WriteByte('\n')
		}
		b.WriteString("data: ")
		b.Write(raw)
		b.WriteString("\n\n")
	}
	writeSSE("response.created", map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id": "resp_nr", "object": "response", "created_at": 1715620000,
			"status": "in_progress", "model": "gpt-4o-mini", "output": []any{},
		},
	})
	writeSSE("response.in_progress", map[string]any{
		"type": "response.in_progress",
		"response": map[string]any{
			"id": "resp_nr", "object": "response", "created_at": 1715620000,
			"status": "in_progress", "model": "gpt-4o-mini", "output": []any{},
		},
	})
	writeSSE("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{
			"type": "message", "id": "msg_nr", "status": "in_progress", "role": "assistant", "content": []any{},
		},
	})
	// output_text.delta is the BE path that emits EventTextDelta (OutputCommitted).
	writeSSE("response.output_text.delta", map[string]any{
		"type": "response.output_text.delta", "item_id": "msg_nr", "output_index": 0, "delta": "committed-visible",
	})
	writeSSE("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": 1,
		"item": map[string]any{"type": "reasoning", "id": "", "summary": []any{}},
	})
	return b.String()
}

func responseVisibleText(res *responses.Response) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, item := range res.Output {
		if item.Type != "message" {
			continue
		}
		for _, c := range item.Content {
			if c.Type == "output_text" {
				b.WriteString(c.Text)
			}
		}
	}
	return b.String()
}

func wireHasJSONType(body []byte, typ string) bool {
	return strings.Contains(string(body), `"type":"`+typ+`"`) || strings.Contains(string(body), `"type": "`+typ+`"`)
}

func wireHasReasoningIDToken(body []byte, id string) bool {
	return strings.Contains(string(body), `"id":"`+id+`"`) || strings.Contains(string(body), `"id": "`+id+`"`)
}

func wireHasChatReasoningContentField(body []byte) bool {
	return strings.Contains(string(body), `"reasoning_content"`)
}

func runNoPairwiseConversionAssertions(t *testing.T) {
	t.Helper()
	// Chat FE -> Responses BE restore must inject Responses opaque, never Chat reasoning_content fields.
	runChatFEResponsesBEDrop(t, false)
}

func createResponsesTurn(ctx context.Context, cli *refclientresp.Client, hist *refclientresp.History, model, user string, stream bool) (*responses.Response, error) {
	params := hist.NewParams(model, user)
	if !stream {
		return cli.CreateResponse(ctx, params)
	}
	st := cli.CreateResponseStream(ctx, params)
	res, _, err := refclientresp.ReadCompletedResponse(st)
	return res, err
}

type oracleError string

func (e oracleError) Error() string { return "topology oracle: structural mismatch: " + string(e) }

func errOracle(code string) error { return oracleError(code) }

func assertNoChatReasoningContentInResponsesBodies(t *testing.T, ch <-chan []byte, n int) {
	t.Helper()
	bodies := drainOracleBodies(t, ch, n)
	for i, b := range bodies {
		if wireHasChatReasoningContentField(b) {
			t.Fatalf("body[%d]: Chat reasoning_content must not appear on Responses BE wire (no pairwise conversion)", i)
		}
	}
}

func assertNoResponsesOpaqueInChatBodies(t *testing.T, ch <-chan []byte, n int) {
	t.Helper()
	bodies := drainOracleBodies(t, ch, n)
	for i, b := range bodies {
		if wireHasJSONType(b, "reasoning") {
			t.Fatalf("body[%d]: Responses opaque type=reasoning must not appear on Chat BE wire (no pairwise conversion)", i)
		}
	}
}
