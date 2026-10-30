package openairesponses_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func exactResponsesOpaque(t *testing.T, id string, extra string) json.RawMessage {
	t.Helper()
	raw := `{"id":"` + id + `","type":"reasoning","summary":[{"type":"summary_text","text":"plan"}],"status":"completed"` + extra + `}`
	return json.RawMessage(raw)
}

func exactResponsesPart(t *testing.T, id string, extra string) *aipapi.ReasoningPart {
	t.Helper()
	return &aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
		Text:    "plan",
		Opaque:  exactResponsesOpaque(t, id, extra),
	}
}

func fidelityCall(t *testing.T) *aipapi.Call {
	t.Helper()
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:y"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
}

func parseSSETypesAndCompleted(t *testing.T, rec *httptest.ResponseRecorder) (seq []string, completed []map[string]any, addedItems []map[string]any, doneItems []map[string]any) {
	t.Helper()
	frames := testkit.ParseRecorderSSE(rec)
	for _, fr := range frames {
		if fr.Data == "" || fr.Data == "[DONE]" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		typ, _ := v["type"].(string)
		if typ == "" {
			continue
		}
		seq = append(seq, typ)
		switch typ {
		case "response.output_item.added":
			if item, _ := v["item"].(map[string]any); item != nil && item["type"] == "reasoning" {
				addedItems = append(addedItems, item)
			}
		case "response.output_item.done":
			if item, _ := v["item"].(map[string]any); item != nil && item["type"] == "reasoning" {
				doneItems = append(doneItems, item)
			}
		case "response.completed":
			resp, _ := v["response"].(map[string]any)
			out, _ := resp["output"].([]any)
			for _, o := range out {
				if m, _ := o.(map[string]any); m != nil {
					completed = append(completed, m)
				}
			}
		}
	}
	return seq, completed, addedItems, doneItems
}

func TestWriteStreamSSE_exactReasoningPart_emitsLegalItemNoSynthesis(t *testing.T) {
	t.Parallel()
	part := exactResponsesPart(t, "rs_exact_1", `,"content":[{"type":"reasoning_text","text":"c"}],"encrypted_content":"enc"`)
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: part},
		{Kind: aipapi.EventTextDelta, Delta: "answer"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	opts := openairesponses.EncodeOptions{ResponseID: "resp_exact", MessageID: "msg_exact", CreatedAt: 1715620000}
	if err := openairesponses.WriteStreamSSE(t.Context(), rec, fidelityCall(t), es, opts); err != nil {
		t.Fatalf("stream exact part: %v", err)
	}
	seq, completed, added, done := parseSSETypesAndCompleted(t, rec)
	if strings.Contains(strings.Join(seq, ","), "response.reasoning_summary_text.delta") {
		t.Fatalf("terminal-only exact path must not emit progressive summary deltas: %v", seq)
	}
	if len(added) != 1 || len(done) != 1 {
		t.Fatalf("want one reasoning added/done, got added=%d done=%d seq=%v", len(added), len(done), seq)
	}
	if added[0]["id"] != "rs_exact_1" || done[0]["id"] != "rs_exact_1" {
		t.Fatalf("must use exact id, not synthesize rs_resp_*: added=%v done=%v", added[0], done[0])
	}
	if _, ok := done[0]["summary"]; !ok {
		t.Fatal("done item missing summary")
	}
	if done[0]["encrypted_content"] != "enc" {
		t.Fatalf("encrypted_content=%v want enc", done[0]["encrypted_content"])
	}
	content, _ := done[0]["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content=%v", done[0]["content"])
	}
	var reasonCount int
	for _, o := range completed {
		if o["type"] == "reasoning" {
			reasonCount++
			if o["id"] != "rs_exact_1" {
				t.Fatalf("completed reasoning id=%v", o["id"])
			}
		}
	}
	if reasonCount != 1 {
		t.Fatalf("completed reasoning count=%d output=%v", reasonCount, completed)
	}
}

func TestWriteStreamSSE_exactReasoningPart_encryptedPresenceVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		extra string
		check func(t *testing.T, item map[string]any)
	}{
		{
			name:  "absent",
			extra: "",
			check: func(t *testing.T, item map[string]any) {
				t.Helper()
				if _, ok := item["encrypted_content"]; ok {
					t.Fatalf("encrypted_content must be absent: %v", item)
				}
			},
		},
		{
			name:  "null",
			extra: `,"encrypted_content":null`,
			check: func(t *testing.T, item map[string]any) {
				t.Helper()
				v, ok := item["encrypted_content"]
				if !ok || v != nil {
					t.Fatalf("encrypted_content want JSON null, got ok=%v v=%v", ok, v)
				}
			},
		},
		{
			name:  "empty_string",
			extra: `,"encrypted_content":""`,
			check: func(t *testing.T, item map[string]any) {
				t.Helper()
				if item["encrypted_content"] != "" {
					t.Fatalf("encrypted_content=%v want empty string", item["encrypted_content"])
				}
			},
		},
		{
			name:  "value",
			extra: `,"encrypted_content":"blob"`,
			check: func(t *testing.T, item map[string]any) {
				t.Helper()
				if item["encrypted_content"] != "blob" {
					t.Fatalf("encrypted_content=%v", item["encrypted_content"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_"+tc.name, tc.extra)},
				{Kind: aipapi.EventResponseFinished},
			})
			rec := httptest.NewRecorder()
			err := openairesponses.WriteStreamSSE(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "r", CreatedAt: 1})
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, done := parseSSETypesAndCompleted(t, rec)
			if len(done) != 1 {
				t.Fatalf("done=%d", len(done))
			}
			tc.check(t, done[0])
		})
	}
}

func TestWriteStreamSSE_exactReasoningPart_noDuplicateWithTextDelta(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "chat-think"},
		{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_exact_dup", "")},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteStreamSSE(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_dup", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	_, completed, _, done := parseSSETypesAndCompleted(t, rec)
	if len(done) != 2 {
		t.Fatalf("uncorrelated chat delta + exact part => two legal reasoning items, got done=%d completed=%v", len(done), completed)
	}
	id0, ok0 := done[0]["id"].(string)
	id1, ok1 := done[1]["id"].(string)
	if !ok0 || !ok1 {
		t.Fatalf("done ids not strings: %v %v", done[0]["id"], done[1]["id"])
	}
	ids := []string{id0, id1}
	if ids[0] == ids[1] {
		t.Fatalf("presentation and exact items must not share id: %v", ids)
	}
	if ids[1] != "rs_exact_dup" && ids[0] != "rs_exact_dup" {
		t.Fatalf("exact id missing: %v", ids)
	}
	synthetic := 0
	for _, id := range ids {
		if strings.HasPrefix(id, "rs_resp_") {
			synthetic++
		}
	}
	if synthetic != 1 {
		t.Fatalf("want exactly one synthetic presentation id, got %v", ids)
	}
}

func TestWriteStreamSSE_exactReasoningPart_wrongDialectSkipped(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
			Text:    "chat-reason",
			Opaque:  json.RawMessage(`{"id":"rs_chat"}`),
		}},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteStreamSSE(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_xd", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	_, completed, _, done := parseSSETypesAndCompleted(t, rec)
	if len(done) != 0 {
		t.Fatalf("cross-dialect exact part must not convert to Responses reasoning item: %v", done)
	}
	for _, o := range completed {
		if o["type"] == "reasoning" {
			t.Fatalf("must not emit fake Responses reasoning: %v", o)
		}
	}
}

func TestWriteStreamSSE_exactReasoningPart_invalidOpaqueContentSafe(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_bad","type":"reasoning","summary":[],"unknown":1}`),
		}},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	err := openairesponses.WriteStreamSSE(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_bad", CreatedAt: 1})
	if err == nil {
		t.Fatal("expected content-safe failure for invalid exact opaque")
	}
	msg := err.Error()
	if strings.Contains(msg, "rs_bad") || strings.Contains(msg, `"summary"`) || strings.Contains(msg, "unknown\":1") {
		t.Fatalf("error must not leak opaque values: %q", msg)
	}
}

func TestWriteStreamSSE_exactReasoningPart_multipleWithNeighborOrdering(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_a", "")},
		{Kind: aipapi.EventTextDelta, Delta: "mid"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "lookup"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_b", "")},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteStreamSSE(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_ord", MessageID: "msg_ord", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	_, completed, _, _ := parseSSETypesAndCompleted(t, rec)
	var types []string
	for _, o := range completed {
		typ, ok := o["type"].(string)
		if !ok {
			t.Fatalf("type field %T in %v", o["type"], o)
		}
		types = append(types, typ)
	}
	want := []string{"reasoning", "message", "function_call", "reasoning"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("stream ordering got %v want %v", types, want)
	}
	if completed[0]["id"] != "rs_a" || completed[3]["id"] != "rs_b" {
		t.Fatalf("ids: %v %v", completed[0]["id"], completed[3]["id"])
	}
}

func TestWriteNonStreamJSON_exactReasoningParts(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_ns", `,"encrypted_content":null`)},
		{Kind: aipapi.EventTextDelta, Delta: "ans"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_ns", MessageID: "msg_ns", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	out, _ := root["output"].([]any)
	if len(out) < 2 {
		t.Fatalf("output=%v", out)
	}
	r0, _ := out[0].(map[string]any)
	if r0["type"] != "reasoning" || r0["id"] != "rs_ns" {
		t.Fatalf("exact nonstream item=%v", r0)
	}
	if _, ok := r0["encrypted_content"]; !ok || r0["encrypted_content"] != nil {
		t.Fatalf("encrypted_content null presence: %v", r0)
	}
	if r0["id"] == "rs_resp_ns" {
		t.Fatal("must not synthesize nonstream reasoning id when exact parts exist")
	}
}

func TestWriteNonStreamJSON_exactPartsPreferOverTextReasoning(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "lossy"},
		{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_pref", "")},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_pref", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	out, _ := root["output"].([]any)
	var reasoning []map[string]any
	for _, o := range out {
		m, _ := o.(map[string]any)
		if m["type"] == "reasoning" {
			reasoning = append(reasoning, m)
		}
	}
	if len(reasoning) != 1 {
		t.Fatalf("when exact parts exist, do not also emit text-collector reasoning; got %v", reasoning)
	}
	if reasoning[0]["id"] != "rs_pref" {
		t.Fatalf("id=%v", reasoning[0]["id"])
	}
	sum, _ := reasoning[0]["summary"].([]any)
	if len(sum) != 1 {
		t.Fatalf("summary=%v", sum)
	}
	sm, _ := sum[0].(map[string]any)
	if sm["text"] == "lossy" {
		t.Fatal("exact item must not fall back to Collected.Reasoning text")
	}
}

func TestWriteNonStreamJSON_textOnlyReasoningRegression(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "only-text"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_txt", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	out, _ := root["output"].([]any)
	if len(out) != 1 {
		t.Fatalf("output=%v", out)
	}
	r0, _ := out[0].(map[string]any)
	if r0["type"] != "reasoning" || r0["id"] != "rs_resp_txt" {
		t.Fatalf("text-only presentation regression: %v", r0)
	}
	if _, ok := r0["encrypted_content"]; ok {
		t.Fatal("text-only synthesis must not claim exact encrypted_content")
	}
}

func TestDecodeCreate_reasoningInput_presenceAndInvalid(t *testing.T) {
	t.Parallel()
	t.Run("null_encrypted", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o-mini","input":[{"type":"reasoning","id":"r_null","summary":[{"type":"summary_text","text":"s"}],"encrypted_content":null}]}`
		d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:m"})
		if err != nil {
			t.Fatal(err)
		}
		part := findReasoningPart(t, d.Call)
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(part.Opaque, &raw); err != nil {
			t.Fatal(err)
		}
		enc, ok := raw["encrypted_content"]
		if !ok || string(enc) != "null" {
			t.Fatalf("opaque encrypted_content presence=%v value=%s", ok, enc)
		}
	})
	t.Run("absent_encrypted", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o-mini","input":[{"type":"reasoning","id":"r_abs","summary":[{"type":"summary_text","text":"s"}]}]}`
		d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:m"})
		if err != nil {
			t.Fatal(err)
		}
		part := findReasoningPart(t, d.Call)
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(part.Opaque, &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["encrypted_content"]; ok {
			t.Fatalf("encrypted_content must be absent: %v", raw)
		}
	})
	t.Run("unknown_field", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o-mini","input":[{"type":"reasoning","id":"r_bad","summary":[{"type":"summary_text","text":"s"}],"extra":1}]}`
		_, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:m"})
		if err == nil {
			t.Fatal("expected reject")
		}
		if strings.Contains(err.Error(), "r_bad") || strings.Contains(err.Error(), `"extra"`) {
			t.Fatalf("content-safe error required: %q", err)
		}
	})
}

func TestDecodeEncodeRoundTrip_reasoningItemJSON(t *testing.T) {
	t.Parallel()
	inItem := `{"type":"reasoning","id":"rs_rt","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"c"}],"encrypted_content":"e","status":"completed"}`
	body := `{"model":"gpt-4o-mini","input":[` + inItem + `]}`
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:m"})
	if err != nil {
		t.Fatal(err)
	}
	part := findReasoningPart(t, d.Call)
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: part},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_rt", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	out, _ := root["output"].([]any)
	got, _ := out[0].(map[string]any)
	if got["id"] != "rs_rt" || got["type"] != "reasoning" || got["status"] != "completed" || got["encrypted_content"] != "e" {
		t.Fatalf("roundtrip fields: %v", got)
	}
	sum, _ := json.Marshal(got["summary"])
	content, _ := json.Marshal(got["content"])
	if !strings.Contains(string(sum), `"text":"s"`) || !strings.Contains(string(content), `"text":"c"`) {
		t.Fatalf("summary/content roundtrip: sum=%s content=%s", sum, content)
	}
}

func findReasoningPart(t *testing.T, call *aipapi.Call) *aipapi.ReasoningPart {
	t.Helper()
	for _, m := range call.Messages {
		for i := range m.Parts {
			if m.Parts[i].Kind == aipapi.PartReasoning && m.Parts[i].Reasoning != nil {
				return m.Parts[i].Reasoning
			}
		}
	}
	t.Fatal("missing PartReasoning")
	return nil
}
