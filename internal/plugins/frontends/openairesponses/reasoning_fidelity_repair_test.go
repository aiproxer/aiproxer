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

func TestDecodeCreate_reasoningEmptySummaryAllowed(t *testing.T) {
	t.Parallel()
	body := `{"model":"gpt-4o-mini","input":[{"type":"reasoning","id":"rs_empty","summary":[]}]}`
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:m"})
	if err != nil {
		t.Fatalf("empty summary exact item must decode: %v", err)
	}
	part := findReasoningPart(t, d.Call)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(part.Opaque, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["id"]) != `"rs_empty"` {
		t.Fatalf("id=%s", raw["id"])
	}
	if string(raw["summary"]) != "[]" {
		t.Fatalf("summary=%s", raw["summary"])
	}
	if _, ok := raw["content"]; ok {
		t.Fatal("content must remain absent")
	}
	if _, ok := raw["encrypted_content"]; ok {
		t.Fatal("encrypted_content must remain absent")
	}
}

func TestWriteNonStreamJSON_exactStatusPresencePreserved(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		check func(t *testing.T, item map[string]any)
	}{
		{
			name: "absent",
			check: func(t *testing.T, item map[string]any) {
				t.Helper()
				if _, ok := item["status"]; ok {
					t.Fatalf("status must stay absent: %v", item)
				}
			},
		},
		{
			name: "in_progress",
			check: func(t *testing.T, item map[string]any) {
				t.Helper()
				if item["status"] != "in_progress" {
					t.Fatalf("status=%v", item["status"])
				}
			},
		},
		{
			name: "completed",
			check: func(t *testing.T, item map[string]any) {
				t.Helper()
				if item["status"] != "completed" {
					t.Fatalf("status=%v", item["status"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opaque json.RawMessage
			switch tc.name {
			case "absent":
				opaque = json.RawMessage(`{"id":"rs_st_absent","type":"reasoning","summary":[{"type":"summary_text","text":"plan"}]}`)
			case "in_progress":
				opaque = json.RawMessage(`{"id":"rs_st_in_progress","type":"reasoning","summary":[{"type":"summary_text","text":"plan"}],"status":"in_progress"}`)
			default:
				opaque = json.RawMessage(`{"id":"rs_st_completed","type":"reasoning","summary":[{"type":"summary_text","text":"plan"}],"status":"completed"}`)
			}
			es := aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
					Opaque:  opaque,
				}},
				{Kind: aipapi.EventResponseFinished},
			})
			rec := httptest.NewRecorder()
			if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "r", CreatedAt: 1}); err != nil {
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
			item, _ := out[0].(map[string]any)
			tc.check(t, item)
		})
	}
}

func TestWriteStreamSSE_exactStatusPresencePreserved(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		opaque string
		check  func(t *testing.T, added, done map[string]any)
	}{
		{
			name:   "absent",
			opaque: `{"id":"rs_abs","type":"reasoning","summary":[{"type":"summary_text","text":"s"}]}`,
			check: func(t *testing.T, added, done map[string]any) {
				t.Helper()
				if _, ok := added["status"]; ok {
					t.Fatalf("added status must stay absent: %v", added)
				}
				if _, ok := done["status"]; ok {
					t.Fatalf("done status must stay absent: %v", done)
				}
			},
		},
		{
			name:   "in_progress",
			opaque: `{"id":"rs_ip","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"status":"in_progress"}`,
			check: func(t *testing.T, added, done map[string]any) {
				t.Helper()
				if added["status"] != "in_progress" || done["status"] != "in_progress" {
					t.Fatalf("added=%v done=%v", added, done)
				}
			},
		},
		{
			name:   "completed",
			opaque: `{"id":"rs_done","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"status":"completed"}`,
			check: func(t *testing.T, added, done map[string]any) {
				t.Helper()
				if added["status"] != "completed" || done["status"] != "completed" {
					t.Fatalf("added=%v done=%v", added, done)
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
				{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
					Opaque:  json.RawMessage(tc.opaque),
				}},
				{Kind: aipapi.EventResponseFinished},
			})
			rec := httptest.NewRecorder()
			if err := openairesponses.WriteStreamSSE(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "r", CreatedAt: 1}); err != nil {
				t.Fatal(err)
			}
			_, _, added, done := parseSSETypesAndCompleted(t, rec)
			if len(added) != 1 || len(done) != 1 {
				t.Fatalf("added=%d done=%d", len(added), len(done))
			}
			tc.check(t, added[0], done[0])
		})
	}
}

func TestWriteStreamSSE_exactSummaryNotDuplicated(t *testing.T) {
	t.Parallel()
	opaque := json.RawMessage(`{"id":"rs_dedupe","type":"reasoning","summary":[{"type":"summary_text","text":"only-once"}],"content":[{"type":"reasoning_text","text":"c"}],"encrypted_content":"e"}`)
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  opaque,
		}},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteStreamSSE(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "r", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var addedSummary []any
	var partTexts []string
	var doneSummary []any
	var doneContent []any
	var doneEnc any
	for _, fr := range frames {
		if fr.Data == "" || fr.Data == "[DONE]" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		switch v["type"] {
		case "response.output_item.added":
			item, _ := v["item"].(map[string]any)
			addedSummary, _ = item["summary"].([]any)
			if _, ok := item["content"]; ok {
				t.Fatal("added item must not carry full content (done owns exact content)")
			}
			if _, ok := item["encrypted_content"]; ok {
				t.Fatal("added item must not carry encrypted_content")
			}
		case "response.reasoning_summary_text.done":
			if s, ok := v["text"].(string); ok {
				partTexts = append(partTexts, s)
			}
		case "response.output_item.done":
			item, _ := v["item"].(map[string]any)
			doneSummary, _ = item["summary"].([]any)
			doneContent, _ = item["content"].([]any)
			doneEnc = item["encrypted_content"]
		}
	}
	if len(addedSummary) != 0 {
		t.Fatalf("added summary must be empty shell, got %v", addedSummary)
	}
	if len(partTexts) != 1 || partTexts[0] != "only-once" {
		t.Fatalf("summary part texts=%v", partTexts)
	}
	if len(doneSummary) != 1 {
		t.Fatalf("done summary=%v", doneSummary)
	}
	// Reconstruct: empty added + part events => one summary; done exact must match once (not 2x).
	reconCount := len(addedSummary) + len(partTexts)
	if reconCount != 1 || len(doneSummary) != 1 {
		t.Fatalf("summary duplication: recon=%d done=%d", reconCount, len(doneSummary))
	}
	if len(doneContent) != 1 || doneEnc != "e" {
		t.Fatalf("done must carry exact content/encrypted: content=%v enc=%v", doneContent, doneEnc)
	}
}

func TestWriteNonStreamJSON_preservesCanonicalEmissionOrder(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_a","type":"reasoning","summary":[{"type":"summary_text","text":"a"}]}`),
		}},
		{Kind: aipapi.EventTextDelta, Delta: "mid"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "lookup"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_b","type":"reasoning","summary":[{"type":"summary_text","text":"b"}]}`),
		}},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_ord", MessageID: "msg_ord", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	out, _ := root["output"].([]any)
	var types []string
	var ids []string
	for _, o := range out {
		m, ok := o.(map[string]any)
		if !ok {
			t.Fatalf("output entry type %T", o)
		}
		typ, ok := m["type"].(string)
		if !ok {
			t.Fatalf("type field %T", m["type"])
		}
		types = append(types, typ)
		if id, ok := m["id"].(string); ok {
			ids = append(ids, id)
		} else if id, ok := m["call_id"].(string); ok {
			ids = append(ids, id)
		}
	}
	want := []string{"reasoning", "message", "function_call", "reasoning"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("nonstream order got %v want %v ids=%v", types, want, ids)
	}
	if ids[0] != "rs_a" || ids[3] != "rs_b" {
		t.Fatalf("ids=%v", ids)
	}
}

func TestWriteNonStreamJSON_textReasoningText_oneMessageSlot(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello "},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_mid","type":"reasoning","summary":[{"type":"summary_text","text":"think"}]}`),
		}},
		{Kind: aipapi.EventTextDelta, Delta: "world"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_tr", MessageID: "msg_tr", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	out, _ := root["output"].([]any)
	var types []string
	for _, o := range out {
		m, ok := o.(map[string]any)
		if !ok {
			t.Fatalf("output entry type %T", o)
		}
		typ, ok := m["type"].(string)
		if !ok {
			t.Fatalf("type field %T", m["type"])
		}
		types = append(types, typ)
	}
	// First message event anchors one message slot; later text joins it.
	want := []string{"message", "reasoning"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v body=%s", types, want, rec.Body.String())
	}
	msg, _ := out[0].(map[string]any)
	content, _ := json.Marshal(msg["content"])
	if !strings.Contains(string(content), "hello world") {
		t.Fatalf("message text must aggregate both deltas: %s", content)
	}
}

func TestWriteStreamSSE_completedOutputNoNilHoles(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_h","type":"reasoning","summary":[{"type":"summary_text","text":"h"}]}`),
		}},
		{Kind: aipapi.EventTextDelta, Delta: "t"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_h", ToolName: "f"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_h", Delta: `{}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_h"},
		{Kind: aipapi.EventReasoningDelta, Delta: "chat"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteStreamSSE(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_h", MessageID: "msg_h", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	_, completed, _, _ := parseSSETypesAndCompleted(t, rec)
	if len(completed) == 0 {
		t.Fatal("empty completed output")
	}
	for i, o := range completed {
		if o == nil {
			t.Fatalf("nil hole at %d", i)
		}
		typ, _ := o["type"].(string)
		if typ == "" {
			t.Fatalf("zero/empty type hole at %d: %v", i, o)
		}
	}
}

func TestWriteNonStreamJSON_wrongDialectExactDoesNotFallbackToText(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
			Text:    "chat-only-text",
			Opaque:  json.RawMessage(`{"id":"rs_chat"}`),
		}},
		{Kind: aipapi.EventTextDelta, Delta: "ans"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, fidelityCall(t), es, openairesponses.EncodeOptions{ResponseID: "resp_xd", MessageID: "msg_xd", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	out, _ := root["output"].([]any)
	for _, o := range out {
		m, _ := o.(map[string]any)
		if m["type"] == "reasoning" {
			t.Fatalf("wrong-dialect exact must not become Responses reasoning or text fallback: %v", m)
		}
	}
}
