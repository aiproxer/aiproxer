package stdhttp_test

import (
	"context"
	"testing"

	refchat "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

// TestReasoningPreservationHTTP_DefaultOnInjection proves standard BuildHost
// injection (absent feature row) activates only via builtin catalog matchers, and
// that explicit enabled:false remains inert for an otherwise eligible model.
func TestReasoningPreservationHTTP_DefaultOnInjection(t *testing.T) {
	t.Run("absent_row_matched_moonshot_restores", func(t *testing.T) {
		t.Parallel()
		runChatDefaultOnDropScenario(t, rpChatStackOpts{
			FeatureRow:        rpFeatureRowExplicit,
			Action:            "restore",
			UseBuiltinCatalog: true,
			Model:             rpE2EModel,
		}, true)
	})

	t.Run("absent_row_gpt_automatic_boundary", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name          string
			model         string
			expectRestore bool
		}{
			{name: "gpt_5_5_restores", model: "gpt-5.5", expectRestore: true},
			{name: "gpt_5_6_inert", model: "gpt-5.6", expectRestore: false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				runChatDefaultOnDropScenario(t, rpChatStackOpts{
					FeatureRow:        rpFeatureRowExplicit,
					Action:            "restore",
					UseBuiltinCatalog: true,
					Model:             tc.model,
				}, tc.expectRestore)
			})
		}
	})

	t.Run("absent_row_unmatched_family_inert", func(t *testing.T) {
		t.Parallel()
		runChatDefaultOnDropScenario(t, rpChatStackOpts{
			FeatureRow:        rpFeatureRowExplicit,
			Action:            "restore",
			UseBuiltinCatalog: true,
			Model:             "claude-3-5-haiku",
		}, false)
	})

	t.Run("explicit_enabled_false_eligible_no_restore", func(t *testing.T) {
		t.Parallel()
		runChatDefaultOnDropScenario(t, rpChatStackOpts{
			FeatureRow: rpFeatureRowExplicit,
			Action:     "disabled",
			Model:      rpE2EModel,
		}, false)
	})
}

func runChatDefaultOnDropScenario(t *testing.T, opts rpChatStackOpts, expectRestore bool) {
	t.Helper()
	model := opts.Model
	if model == "" {
		model = rpE2EModel
	}
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   201,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "answer-one",
			Reasoning:   chatReasoning("think-one"),
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
		validators = chatRestoreValidators(plan, 2, nil, rpHarnessMaxTurnsPerSession)
	} else {
		validators = chatNoRestoreValidators(plan, 2)
	}
	stack := startReasoningPreservationChatStackOpts(t, opts, scripted, validators...)
	cli := &reasoninge2e.ChatWireClient{
		BaseURL:    stack.proxyURL + "/v1",
		APIKey:     rpE2EFakeKey,
		HTTPClient: stack.proxy.Client(),
		Model:      model,
	}
	emu := reasoninge2e.NewClientEmulator(plan)
	ctx := context.Background()

	messages, err := emu.MaterializeChatRequest("ask-1")
	if err != nil {
		t.Fatal(err)
	}
	resp1, err := cli.PostChatCompletion(ctx, false, messages, nil)
	requireHTTPOK(t, resp1.Status, resp1.RawBody)
	if err != nil {
		t.Fatal(err)
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
	resp2, err := cli.PostChatCompletion(ctx, false, messages, nil)
	requireHTTPOK(t, resp2.Status, resp2.RawBody)
	if err != nil {
		t.Fatal(err)
	}
	requireLedgerOK(t, stack)
	if stack.ledger.Count() != 2 {
		t.Fatalf("oracle request_count=%d want=2", stack.ledger.Count())
	}
}
