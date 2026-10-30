package openairesponses_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
)

func TestScriptedTurn_contentDefaultTypeReasoningText(t *testing.T) {
	t.Parallel()
	turn := refbackend.ScriptedTurn{
		ResponseID: "resp_ctype",
		Reasoning: []refbackend.ReasoningOutputItem{{
			Label:   "c",
			ID:      "rs_ctype",
			Summary: []refbackend.TextPart{},
			Content: []refbackend.TextPart{{Text: "body-only"}}, // Type omitted
		}},
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(refbackend.ScriptedTurnJSON(turn)), &root); err != nil {
		t.Fatal(err)
	}
	out, _ := root["output"].([]any)
	item, _ := out[0].(map[string]any)
	content, _ := item["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content=%v", content)
	}
	el, _ := content[0].(map[string]any)
	if el["type"] != "reasoning_text" {
		t.Fatalf("content element type=%v want reasoning_text", el["type"])
	}
}

func TestScriptedTurnSSE_emitsReasoningTextDone(t *testing.T) {
	t.Parallel()
	turn := refbackend.ScriptedTurn{
		ResponseID: "resp_rtd",
		Reasoning: []refbackend.ReasoningOutputItem{{
			Label:   "rtd",
			ID:      "rs_rtd",
			Summary: []refbackend.TextPart{{Type: "summary_text", Text: "s"}},
			Content: []refbackend.TextPart{{Type: "reasoning_text", Text: "c"}},
		}},
	}
	sse := refbackend.ScriptedTurnSSE(turn)
	if !strings.Contains(sse, `"type":"response.reasoning_text.done"`) && !strings.Contains(sse, `"type": "response.reasoning_text.done"`) {
		t.Fatalf("missing reasoning_text.done in SSE")
	}
}

func TestOracle_inputOrderAndSafeIDs(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"m","input":[
		{"type":"reasoning","id":"rs_SECRET_A","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"encrypted_content":"SECRET_ENC"},
		{"type":"message","role":"assistant","content":"visible"},
		{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"SECRET_ARG\"}"},
		{"type":"reasoning","id":"rs_SECRET_B","summary":[],"content":[],"encrypted_content":null}
	]}`)
	want := []refbackend.InputItemExpect{
		{Kind: "reasoning", Label: "a", Reasoning: &refbackend.ReasoningInputExpect{
			Label: "a", ID: "rs_SECRET_A", SummaryLen: 1, Encrypted: refbackend.EncryptedValue,
		}},
		{Kind: "message", Label: "msg"},
		{Kind: "function_call", Label: "tool", ToolName: "lookup"},
		{Kind: "reasoning", Label: "b", Reasoning: &refbackend.ReasoningInputExpect{
			Label: "b", ID: "rs_SECRET_B", SummaryLen: 0, HasContent: true, ContentLen: 0, Encrypted: refbackend.EncryptedNull,
		}},
	}
	if err := refbackend.CheckInputItems(body, want); err != nil {
		t.Fatalf("order oracle: %v", err)
	}
	bad := []byte(`{"model":"m","input":[
		{"type":"message","role":"assistant","content":"visible"},
		{"type":"reasoning","id":"rs_SECRET_A","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"encrypted_content":"SECRET_ENC"},
		{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"SECRET_ARG\"}"},
		{"type":"reasoning","id":"rs_SECRET_B","summary":[],"content":[],"encrypted_content":null}
	]}`)
	err := refbackend.CheckInputItems(bad, want)
	if err == nil {
		t.Fatal("expected order mismatch")
	}
	msg := err.Error()
	for _, leak := range []string{"SECRET", "rs_SECRET", `{"q"`} {
		if strings.Contains(msg, leak) {
			t.Fatalf("oracle leak %q in %q", leak, msg)
		}
	}
	if !strings.Contains(msg, "label=a") {
		t.Fatalf("want fixture label in error: %q", msg)
	}
}

func TestOracle_contentPresenceVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want refbackend.ReasoningInputExpect
	}{
		{
			name: "content_absent",
			body: `{"input":[{"type":"reasoning","id":"rs_x","summary":[]}]}`,
			want: refbackend.ReasoningInputExpect{Label: "x", ID: "rs_x", SummaryLen: 0, Encrypted: refbackend.EncryptedAbsent},
		},
		{
			name: "content_empty",
			body: `{"input":[{"type":"reasoning","id":"rs_x","summary":[],"content":[]}]}`,
			want: refbackend.ReasoningInputExpect{Label: "x", ID: "rs_x", SummaryLen: 0, HasContent: true, ContentLen: 0, Encrypted: refbackend.EncryptedAbsent},
		},
		{
			name: "content_value",
			body: `{"input":[{"type":"reasoning","id":"rs_x","summary":[],"content":[{"type":"reasoning_text","text":"SECRET"}]}]}`,
			want: refbackend.ReasoningInputExpect{Label: "x", ID: "rs_x", SummaryLen: 0, HasContent: true, ContentLen: 1, Encrypted: refbackend.EncryptedAbsent},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := refbackend.CheckReasoningInput([]byte(tc.body), []refbackend.ReasoningInputExpect{tc.want}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestScriptedResponder_exhaustedAndLedgerComplete(t *testing.T) {
	t.Parallel()
	ledger := refbackend.NewOracleLedger(
		refbackend.ExpectNoReasoningInput(),
		refbackend.ExpectNoReasoningInput(),
	)
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		OnRequestBody:      ledger.Hook(),
		Responder: refbackend.ScriptedResponder([]refbackend.ScriptedTurn{
			{ResponseID: "r1", VisibleText: "a"},
			{ResponseID: "r2", VisibleText: "b"},
		}),
	}))
	t.Cleanup(srv.Close)
	postResponses(t, srv.URL, `{"model":"m","input":"1"}`)
	postResponses(t, srv.URL, `{"model":"m","input":"2"}`)
	resp, body := postResponses(t, srv.URL, `{"model":"m","input":"3"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("exhausted status=%d body=%s", resp.StatusCode, body)
	}
	if ledger.Count() != 3 {
		t.Fatalf("ledger count=%d want 3", ledger.Count())
	}
	if err := ledger.Err(); err == nil || !strings.Contains(err.Error(), "unexpected_request") {
		t.Fatalf("ledger err=%v", err)
	}
}

func TestScriptedResponder_concurrentDeterministic(t *testing.T) {
	t.Parallel()
	const n = 32
	turns := make([]refbackend.ScriptedTurn, n)
	for i := range turns {
		turns[i] = refbackend.ScriptedTurn{ResponseID: "r", VisibleText: "x"}
	}
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		Responder:          refbackend.ScriptedResponder(turns),
	}))
	t.Cleanup(srv.Close)
	var wg sync.WaitGroup
	var fail sync.Map
	for i := range n {
		wg.Go(func() {
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"m","input":"u"}`))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil || resp.StatusCode != 200 {
				fail.Store(i, true)
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
		})
	}
	wg.Wait()
	fail.Range(func(k, _ any) bool {
		t.Fatalf("concurrent request failed index=%v", k)
		return false
	})
	resp, _ := postResponses(t, srv.URL, `{"model":"m","input":"extra"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected exhausted after %d, status=%d", n, resp.StatusCode)
	}
}
