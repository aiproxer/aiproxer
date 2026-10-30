package openairesponses_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/refclient/openairesponses"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func TestHistory_preserveAndDrop_roundTripPresence(t *testing.T) {
	t.Parallel()
	turn1 := refbackend.ScriptedTurn{
		ResponseID: "resp_h1",
		Reasoning: []refbackend.ReasoningOutputItem{{
			Label:        "null_enc",
			ID:           "rs_hist_1",
			Summary:      []refbackend.TextPart{{Type: "summary_text", Text: "plan"}},
			Content:      []refbackend.TextPart{{Type: "reasoning_text", Text: "c"}},
			EncryptedRaw: json.RawMessage("null"),
			Status:       "completed",
		}},
		VisibleText: "answer-1",
	}
	turn2 := refbackend.ScriptedTurn{ResponseID: "resp_h2", VisibleText: "answer-2"}

	t.Run("preserve", func(t *testing.T) {
		t.Parallel()
		ledger := refbackend.NewOracleLedger(
			refbackend.ExpectNoReasoningInput(),
			refbackend.ExpectReasoningInput([]refbackend.ReasoningInputExpect{{
				Label: "null_enc", ID: "rs_hist_1", SummaryLen: 1, ContentLen: 1,
				Encrypted: refbackend.EncryptedNull, Status: "completed",
			}}),
		)
		srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
			AllowMissingBearer: true,
			OnRequestBody:      ledger.Hook(),
			Responder:          refbackend.ScriptedResponder([]refbackend.ScriptedTurn{turn1, turn2}),
		}))
		t.Cleanup(srv.Close)
		cli := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
		hist := openairesponses.NewHistory(openairesponses.PreserveReasoning)
		res, err := cli.CreateResponse(context.Background(), hist.NewParams("gpt-4o-mini", "u1"))
		if err != nil {
			t.Fatal(err)
		}
		if err := hist.ObserveResponse(res); err != nil {
			t.Fatal(err)
		}
		_, err = cli.CreateResponse(context.Background(), hist.NewParams("gpt-4o-mini", "u2"))
		if err != nil {
			t.Fatal(err)
		}
		if err := ledger.Err(); err != nil {
			t.Fatalf("preserve oracle: %v", err)
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
			Responder:          refbackend.ScriptedResponder([]refbackend.ScriptedTurn{turn1, turn2}),
		}))
		t.Cleanup(srv.Close)
		cli := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
		hist := openairesponses.NewHistory(openairesponses.DropReasoning)
		res, err := cli.CreateResponse(context.Background(), hist.NewParams("gpt-4o-mini", "u1"))
		if err != nil {
			t.Fatal(err)
		}
		if err := hist.ObserveResponse(res); err != nil {
			t.Fatal(err)
		}
		params := hist.NewParams("gpt-4o-mini", "u2")
		_, err = cli.CreateResponse(context.Background(), params)
		if err != nil {
			t.Fatal(err)
		}
		if err := ledger.Err(); err != nil {
			t.Fatalf("drop oracle: %v", err)
		}
		// Ensure materializer itself did not attach reasoning params.
		raw, _ := json.Marshal(params)
		if strings.Contains(string(raw), `"type":"reasoning"`) || strings.Contains(string(raw), "rs_hist_1") {
			t.Fatalf("drop policy leaked reasoning into params: %s", raw)
		}
	})
}

func TestHistory_observeStreamCompleted(t *testing.T) {
	t.Parallel()
	turn := refbackend.ScriptedTurn{
		ResponseID: "resp_stream_h",
		Reasoning: []refbackend.ReasoningOutputItem{{
			Label:        "stream_item",
			ID:           "rs_stream_1",
			Summary:      []refbackend.TextPart{{Type: "summary_text", Text: "s"}},
			EncryptedRaw: json.RawMessage(`""`),
		}},
		VisibleText: "ok",
	}
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		Responder:          refbackend.ScriptedResponder([]refbackend.ScriptedTurn{turn}),
	}))
	t.Cleanup(srv.Close)
	cli := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	hist := openairesponses.NewHistory(openairesponses.PreserveReasoning)
	stream := cli.CreateResponseStream(context.Background(), hist.NewParams("gpt-4o-mini", "u"))
	var completed *responses.Response
	for stream.Next() {
		ev := stream.Current()
		if ev.Type == "response.completed" {
			completed = &ev.Response
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if completed == nil {
		t.Fatal("missing completed")
	}
	if err := hist.ObserveResponse(completed); err != nil {
		t.Fatal(err)
	}
	items := hist.ObservedReasoning()
	if len(items) != 1 || items[0].ID != "rs_stream_1" {
		t.Fatalf("observed=%+v", items)
	}
	if items[0].Encrypted != openairesponses.EncryptedEmpty {
		t.Fatalf("encrypted presence=%v", items[0].Encrypted)
	}
	_ = shared.ResponsesModel("gpt-4o-mini")
}
