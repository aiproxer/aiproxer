package openairesponses_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/refclient/openairesponses"
	"github.com/openai/openai-go/v3/responses"
)

func TestHistory_ObservedReasoning_defensiveRawCopy(t *testing.T) {
	t.Parallel()
	turn := refbackend.ScriptedTurn{
		ResponseID: "resp_copy",
		Reasoning: []refbackend.ReasoningOutputItem{{
			Label: "c", ID: "rs_copy", Summary: []refbackend.TextPart{},
			EncryptedRaw: json.RawMessage(`"enc"`),
		}},
	}
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		Responder:          refbackend.ScriptedResponder([]refbackend.ScriptedTurn{turn}),
	}))
	t.Cleanup(srv.Close)
	cli := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	hist := openairesponses.NewHistory(openairesponses.PreserveReasoning)
	res, err := cli.CreateResponse(context.Background(), hist.NewParams("gpt-4o-mini", "u"))
	if err != nil {
		t.Fatal(err)
	}
	if err := hist.ObserveResponse(res); err != nil {
		t.Fatal(err)
	}
	got := hist.ObservedReasoning()
	if len(got) != 1 || len(got[0].RawItem) == 0 {
		t.Fatalf("observed=%+v", got)
	}
	got[0].RawItem[0] = 'X'
	again := hist.ObservedReasoning()
	if again[0].RawItem[0] == 'X' {
		t.Fatal("ObservedReasoning must defensive-copy RawItem")
	}
}

func TestHistory_preserveOutputOrder_interleaved(t *testing.T) {
	t.Parallel()
	turn := refbackend.ScriptedTurn{
		ResponseID: "resp_ord",
		Parts: []refbackend.ScriptedPart{
			{Reasoning: &refbackend.ReasoningOutputItem{
				Label: "a", ID: "rs_a", Summary: []refbackend.TextPart{{Type: "summary_text", Text: "a"}},
			}},
			{Message: "mid"},
			{Tool: &refbackend.ToolCall{ID: "call_1", Name: "lookup", Arguments: `{}`}},
			{Reasoning: &refbackend.ReasoningOutputItem{
				Label: "b", ID: "rs_b", Summary: []refbackend.TextPart{}, EncryptedRaw: json.RawMessage("null"),
			}},
		},
	}
	ledger := refbackend.NewOracleLedger(
		refbackend.ExpectNoReasoningInput(),
		refbackend.ExpectInputItems([]refbackend.InputItemExpect{
			{Kind: "reasoning", Label: "a", Reasoning: &refbackend.ReasoningInputExpect{
				Label: "a", ID: "rs_a", SummaryLen: 1, Encrypted: refbackend.EncryptedAbsent,
			}},
			{Kind: "message", Label: "msg"},
			{Kind: "function_call", Label: "tool", ToolName: "lookup"},
			{Kind: "reasoning", Label: "b", Reasoning: &refbackend.ReasoningInputExpect{
				Label: "b", ID: "rs_b", SummaryLen: 0, Encrypted: refbackend.EncryptedNull,
			}},
			{Kind: "message", Label: "user"},
		}),
	)
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		OnRequestBody:      ledger.Hook(),
		Responder:          refbackend.ScriptedResponder([]refbackend.ScriptedTurn{turn, {ResponseID: "r2", VisibleText: "ok"}}),
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
		t.Fatalf("order preserve: %v", err)
	}
}

func TestHistory_streamPreservePresenceTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		enc        json.RawMessage
		want       refbackend.EncryptedPresence
		content    []refbackend.TextPart
		hasContent bool
		contentLen int
	}{
		{name: "enc_absent", enc: nil, want: refbackend.EncryptedAbsent},
		{name: "enc_null", enc: json.RawMessage("null"), want: refbackend.EncryptedNull},
		{name: "enc_empty", enc: json.RawMessage(`""`), want: refbackend.EncryptedEmpty},
		{name: "enc_value", enc: json.RawMessage(`"blob"`), want: refbackend.EncryptedValue},
		{name: "content_empty", enc: nil, want: refbackend.EncryptedAbsent, content: []refbackend.TextPart{}, hasContent: true, contentLen: 0},
		{name: "content_value", enc: nil, want: refbackend.EncryptedAbsent, content: []refbackend.TextPart{{Type: "reasoning_text", Text: "c"}}, hasContent: true, contentLen: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			item := refbackend.ReasoningOutputItem{
				Label: tc.name, ID: "rs_" + tc.name,
				Summary:      []refbackend.TextPart{{Type: "summary_text", Text: "s"}},
				EncryptedRaw: tc.enc,
			}
			if tc.content != nil || tc.hasContent {
				item.Content = tc.content
				if item.Content == nil {
					item.Content = []refbackend.TextPart{}
				}
			}
			turn := refbackend.ScriptedTurn{ResponseID: "resp_" + tc.name, Reasoning: []refbackend.ReasoningOutputItem{item}}
			expect := refbackend.ReasoningInputExpect{
				Label: tc.name, ID: "rs_" + tc.name, SummaryLen: 1,
				Encrypted: tc.want, HasContent: tc.hasContent, ContentLen: tc.contentLen,
			}
			ledger := refbackend.NewOracleLedger(
				refbackend.ExpectNoReasoningInput(),
				refbackend.ExpectReasoningInput([]refbackend.ReasoningInputExpect{expect}),
			)
			srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
				AllowMissingBearer: true,
				OnRequestBody:      ledger.Hook(),
				Responder:          refbackend.ScriptedResponder([]refbackend.ScriptedTurn{turn, {ResponseID: "r2", VisibleText: "x"}}),
			}))
			t.Cleanup(srv.Close)
			cli := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
			hist := openairesponses.NewHistory(openairesponses.PreserveReasoning)
			stream := cli.CreateResponseStream(context.Background(), hist.NewParams("gpt-4o-mini", "u1"))
			completed, stats, err := openairesponses.ReadCompletedResponse(stream)
			if err != nil {
				t.Fatal(err)
			}
			if completed == nil {
				t.Fatal("nil completed")
			}
			if tc.hasContent && tc.contentLen > 0 && stats.ReasoningTextDone < 1 {
				t.Fatalf("SDK stream must see reasoning_text.done, stats=%+v", stats)
			}
			if err := hist.ObserveResponse(completed); err != nil {
				t.Fatal(err)
			}
			params := hist.NewParams("gpt-4o-mini", "u2")
			// Prove marshaled request JSON preserves presence before SDK send.
			if err := assertParamsReasoningPresence(t, params, expect); err != nil {
				t.Fatal(err)
			}
			_, err = cli.CreateResponse(context.Background(), params)
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.Err(); err != nil {
				t.Fatalf("stream preserve %s: %v", tc.name, err)
			}
		})
	}
}

func assertParamsReasoningPresence(t *testing.T, params responses.ResponseNewParams, want refbackend.ReasoningInputExpect) error {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return refbackend.CheckReasoningInput(raw, []refbackend.ReasoningInputExpect{want})
}
