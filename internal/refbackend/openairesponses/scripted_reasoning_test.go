package openairesponses_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
)

func TestScriptedResponder_reasoningOnly_nonstreamAndSSE(t *testing.T) {
	t.Parallel()
	turn := refbackend.ScriptedTurn{
		ResponseID: "resp_script_1",
		Reasoning: []refbackend.ReasoningOutputItem{{
			Label:        "enc_value",
			ID:           "rs_script_1",
			Summary:      []refbackend.TextPart{{Type: "summary_text", Text: "plan"}},
			Content:      []refbackend.TextPart{{Type: "reasoning_text", Text: "body"}},
			EncryptedRaw: json.RawMessage(`"enc-fixture"`),
			Status:       "completed",
		}},
	}
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		Responder:          refbackend.ScriptedResponder([]refbackend.ScriptedTurn{turn, turn}),
	}))
	t.Cleanup(srv.Close)

	// nonstream
	resp, body := postResponses(t, srv.URL, `{"model":"gpt-4o-mini","input":"u1"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		t.Fatal(err)
	}
	out, _ := root["output"].([]any)
	if len(out) != 1 {
		t.Fatalf("nonstream output len=%d body=%s", len(out), body)
	}
	item, _ := out[0].(map[string]any)
	if item["type"] != "reasoning" || item["id"] != "rs_script_1" {
		t.Fatalf("item=%v", item)
	}
	if item["encrypted_content"] != "enc-fixture" {
		t.Fatalf("encrypted=%v", item["encrypted_content"])
	}

	// stream
	resp, body = postResponses(t, srv.URL, `{"model":"gpt-4o-mini","stream":true,"input":"u2"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("stream status=%d", resp.StatusCode)
	}
	if !strings.Contains(body, `"type":"response.completed"`) && !strings.Contains(body, `"type": "response.completed"`) {
		t.Fatalf("missing completed: %s", body)
	}
	if !strings.Contains(body, `"rs_script_1"`) {
		t.Fatalf("missing reasoning id in SSE: %s", body)
	}
	if strings.Contains(body, "response.reasoning_summary_text.delta") {
		t.Fatal("terminal script must not emit progressive summary deltas")
	}
}

func TestScriptedResponder_encryptedPresenceVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		raw   json.RawMessage
		check func(t *testing.T, item map[string]any)
	}{
		{name: "absent", raw: nil, check: func(t *testing.T, item map[string]any) {
			t.Helper()
			if _, ok := item["encrypted_content"]; ok {
				t.Fatalf("want absent: %v", item)
			}
		}},
		{name: "null", raw: json.RawMessage("null"), check: func(t *testing.T, item map[string]any) {
			t.Helper()
			v, ok := item["encrypted_content"]
			if !ok || v != nil {
				t.Fatalf("want null: ok=%v v=%v", ok, v)
			}
		}},
		{name: "empty", raw: json.RawMessage(`""`), check: func(t *testing.T, item map[string]any) {
			t.Helper()
			if item["encrypted_content"] != "" {
				t.Fatalf("want empty string: %v", item["encrypted_content"])
			}
		}},
		{name: "value", raw: json.RawMessage(`"blob"`), check: func(t *testing.T, item map[string]any) {
			t.Helper()
			if item["encrypted_content"] != "blob" {
				t.Fatalf("want blob: %v", item["encrypted_content"])
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			turn := refbackend.ScriptedTurn{
				ResponseID: "resp_" + tc.name,
				Reasoning: []refbackend.ReasoningOutputItem{{
					Label:        "enc_" + tc.name,
					ID:           "rs_" + tc.name,
					Summary:      []refbackend.TextPart{},
					EncryptedRaw: tc.raw,
				}},
			}
			srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
				AllowMissingBearer: true,
				Responder:          refbackend.ScriptedResponder([]refbackend.ScriptedTurn{turn}),
			}))
			t.Cleanup(srv.Close)
			_, body := postResponses(t, srv.URL, `{"model":"m","input":"x"}`)
			var root map[string]any
			if err := json.Unmarshal([]byte(body), &root); err != nil {
				t.Fatal(err)
			}
			out, _ := root["output"].([]any)
			item, _ := out[0].(map[string]any)
			tc.check(t, item)
		})
	}
}

func TestOracle_rawRequestReasoningPresenceAndOrder(t *testing.T) {
	t.Parallel()
	want := []refbackend.ReasoningInputExpect{
		{Label: "a", ID: "rs_a", SummaryLen: 1, Encrypted: refbackend.EncryptedAbsent},
		{Label: "b", ID: "rs_b", SummaryLen: 0, Encrypted: refbackend.EncryptedNull},
	}
	body := []byte(`{"model":"m","input":[
		{"type":"reasoning","id":"rs_a","summary":[{"type":"summary_text","text":"SECRET_A"}]},
		{"type":"message","role":"user","content":"hi"},
		{"type":"reasoning","id":"rs_b","summary":[],"encrypted_content":null}
	]}`)
	if err := refbackend.CheckReasoningInput(body, want); err != nil {
		t.Fatalf("oracle: %v", err)
	}
	bad := []byte(`{"model":"m","input":[
		{"type":"reasoning","id":"rs_a","summary":[{"type":"summary_text","text":"SECRET_A"}],"encrypted_content":null},
		{"type":"reasoning","id":"rs_b","summary":[],"encrypted_content":null}
	]}`)
	err := refbackend.CheckReasoningInput(bad, want)
	if err == nil {
		t.Fatal("expected mismatch")
	}
	msg := err.Error()
	if strings.Contains(msg, "SECRET") {
		t.Fatalf("content-safe oracle required: %q", msg)
	}
	if strings.Contains(msg, "rs_a") || strings.Contains(msg, "rs_b") {
		t.Fatalf("must not leak reasoning ids: %q", msg)
	}
	if !strings.Contains(msg, "label=a") {
		t.Fatalf("must include fixture label: %q", msg)
	}
}

func TestOracleLedger_contentSafeMismatch(t *testing.T) {
	t.Parallel()
	ledger := refbackend.NewOracleLedger(
		refbackend.ExpectNoReasoningInput(),
		refbackend.ExpectReasoningInput([]refbackend.ReasoningInputExpect{{
			Label: "keep", ID: "rs_keep", SummaryLen: 1, Encrypted: refbackend.EncryptedValue,
		}}),
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
	postResponses(t, srv.URL, `{"model":"m","input":"u1"}`)
	postResponses(t, srv.URL, `{"model":"m","input":[{"type":"message","role":"user","content":"u2"}]}`)
	err := ledger.Err()
	if err == nil {
		t.Fatal("expected second-request mismatch")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("leak: %v", err)
	}
}

func postResponses(t *testing.T, base, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}
