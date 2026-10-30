package reasoninge2e_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/refclient/openairesponses"
	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

func TestResponsesHarness_multiTurnPreserveAndDrop(t *testing.T) {
	t.Parallel()
	turns := []refbackend.ScriptedTurn{
		{
			ResponseID: "resp_e2e_1",
			Reasoning: []refbackend.ReasoningOutputItem{
				{
					Label:        "first",
					ID:           "rs_e2e_a",
					Summary:      []refbackend.TextPart{{Type: "summary_text", Text: "a"}},
					EncryptedRaw: json.RawMessage(`"enc-a"`),
				},
				{
					Label:        "second",
					ID:           "rs_e2e_b",
					Summary:      []refbackend.TextPart{},
					EncryptedRaw: json.RawMessage("null"),
				},
			},
			VisibleText: "visible-1",
			Tool: &refbackend.ToolCall{
				ID: "call_e2e", Name: "lookup", Arguments: `{"q":1}`,
			},
		},
		{ResponseID: "resp_e2e_2", VisibleText: "visible-2"},
	}

	t.Run("preserve_multi_reasoning_and_tool", func(t *testing.T) {
		t.Parallel()
		ledger := refbackend.NewOracleLedger(
			refbackend.ExpectNoReasoningInput(),
			refbackend.ExpectReasoningInput([]refbackend.ReasoningInputExpect{
				{Label: "first", ID: "rs_e2e_a", SummaryLen: 1, Encrypted: refbackend.EncryptedValue},
				{Label: "second", ID: "rs_e2e_b", SummaryLen: 0, Encrypted: refbackend.EncryptedNull},
			}),
		)
		srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
			AllowMissingBearer: true,
			OnRequestBody:      ledger.Hook(),
			Responder:          refbackend.ScriptedResponder(turns),
		}))
		t.Cleanup(srv.Close)
		cli := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
		hist := openairesponses.NewHistory(openairesponses.PreserveReasoning)
		res, err := cli.CreateResponse(context.Background(), hist.NewParams("gpt-4o-mini", "turn-1"))
		if err != nil {
			t.Fatal(err)
		}
		if err := hist.ObserveResponse(res); err != nil {
			t.Fatal(err)
		}
		if got := len(hist.ObservedReasoning()); got != 2 {
			t.Fatalf("observed reasoning=%d", got)
		}
		_, err = cli.CreateResponse(context.Background(), hist.NewParams("gpt-4o-mini", "turn-2"))
		if err != nil {
			t.Fatal(err)
		}
		if err := ledger.Err(); err != nil {
			t.Fatalf("preserve e2e: %v", err)
		}
		if err := reasoninge2e.CheckResponsesHistoryIDs(hist.ObservedReasoningIDs(), []string{"rs_e2e_a", "rs_e2e_b"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("drop", func(t *testing.T) {
		t.Parallel()
		ledger := refbackend.NewOracleLedger(
			refbackend.ExpectNoReasoningInput(),
			refbackend.ExpectNoReasoningInput(),
		)
		srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
			AllowMissingBearer: true,
			OnRequestBody:      ledger.Hook(),
			Responder:          refbackend.ScriptedResponder(turns),
		}))
		t.Cleanup(srv.Close)
		cli := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
		hist := openairesponses.NewHistory(openairesponses.DropReasoning)
		res, err := cli.CreateResponse(context.Background(), hist.NewParams("gpt-4o-mini", "turn-1"))
		if err != nil {
			t.Fatal(err)
		}
		if err := hist.ObserveResponse(res); err != nil {
			t.Fatal(err)
		}
		_, err = cli.CreateResponse(context.Background(), hist.NewParams("gpt-4o-mini", "turn-2"))
		if err != nil {
			t.Fatal(err)
		}
		if err := ledger.Err(); err != nil {
			t.Fatalf("drop e2e: %v", err)
		}
	})
}
