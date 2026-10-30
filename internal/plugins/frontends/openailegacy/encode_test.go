package openailegacy_test

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestWriteNonStreamJSON_matchesGolden(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "golden-ok"},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:y"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	opts := openailegacy.EncodeOptions{
		CompletionID: "chatcmpl_encode_golden",
		CreatedAt:    1715620000,
	}
	if err := openailegacy.WriteNonStreamJSON(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	want := readGolden(t, "chat_completion_nonstream_expected.json")
	assertJSONEqual(t, want, rec.Body.Bytes())
}

func TestWriteNonStreamJSONUsesClientVisibleScopedUsage(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{
			{InputTokens: 100, OutputTokens: 50, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable}},
			{InputTokens: 10, OutputTokens: 5, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible}},
		}},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()

	call := &aipapi.Call{Extensions: mustModelExt(t, "gpt-4o-mini")}
	if err := openailegacy.WriteNonStreamJSON(t.Context(), rec, call, es, openailegacy.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Usage == nil {
		t.Fatal("usage is nil")
	}
	if got.Usage.PromptTokens != 10 || got.Usage.CompletionTokens != 5 || got.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v, want client-visible 10/5/15", *got.Usage)
	}
}

func TestWriteNonStreamJSON_defaultsAreDeterministic(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "stable"},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:y"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	if err := openailegacy.WriteNonStreamJSON(t.Context(), rec, call, es, openailegacy.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	wantID := "chatcmpl_" + diag.StableCallToken(call)
	if v.ID != wantID {
		t.Fatalf("completion id %q, want %q", v.ID, wantID)
	}
	if v.Created != diag.StableUnix(call) {
		t.Fatalf("created %d, want %d", v.Created, diag.StableUnix(call))
	}
	var content string
	if err := json.Unmarshal(v.Choices[0].Message.Content, &content); err != nil {
		t.Fatal(err)
	}
	if len(v.Choices) != 1 || content != "stable" {
		t.Fatalf("choices: %+v content=%q", v.Choices, content)
	}
}

func TestWriteNonStreamJSON_usageFromCollect(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventUsageDelta, InputTokens: 4, OutputTokens: 0},
		{Kind: aipapi.EventTextDelta, Delta: "x"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 0, OutputTokens: 1},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:y"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	opts := openailegacy.EncodeOptions{CompletionID: "chatcmpl_usage_ns", CreatedAt: 1715620000}
	if err := openailegacy.WriteNonStreamJSON(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Usage *struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Total      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Usage == nil || v.Usage.Prompt != 4 || v.Usage.Completion != 1 || v.Usage.Total != 5 {
		t.Fatalf("usage %+v", v.Usage)
	}
}

func TestWriteNonStreamJSON_usageDetailsWithoutTopLevelTokens(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventUsageDelta, InputTokens: 10, CacheReadTokens: 7, CacheWriteTokens: 2, ReasoningTokens: 3, CostNanoUnits: 99, Currency: "USD", CostSource: "estimated"},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{Extensions: mustModelExt(t, "gpt-4o-mini")}
	rec := httptest.NewRecorder()
	if err := openailegacy.WriteNonStreamJSON(t.Context(), rec, call, es, openailegacy.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Usage *struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails struct {
				CachedTokens   int `json:"cached_tokens"`
				UncachedTokens int `json:"x_aip_uncached_tokens"`
				CacheWrite     int `json:"x_aip_cache_write_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Usage == nil {
		t.Fatal("usage is nil")
	}
	if v.Usage.PromptTokens != 10 || v.Usage.CompletionTokens != 0 || v.Usage.TotalTokens != 10 {
		t.Fatalf("top-level usage = %+v", *v.Usage)
	}
	if v.Usage.PromptTokensDetails.CachedTokens != 7 || v.Usage.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("usage details = %+v", *v.Usage)
	}
	if v.Usage.PromptTokensDetails.UncachedTokens != 0 || v.Usage.PromptTokensDetails.CacheWrite != 0 {
		t.Fatalf("x_aip details should be hidden by default: %+v", *v.Usage)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	usage, ok := raw["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage raw = %#v", raw["usage"])
	}
	for _, key := range []string{"x_aip_cost_nano_units", "x_aip_currency", "x_aip_cost_source"} {
		if _, ok := usage[key]; ok {
			t.Fatalf("%s should be hidden by default: %#v", key, usage)
		}
	}
}

func TestWriteNonStreamJSON_usageExtensionsWhenEnabled(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventUsageDelta, InputTokens: 10, CacheReadTokens: 7, CacheWriteTokens: 2, CostNanoUnits: 99, Currency: "USD", CostSource: "estimated"},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{Extensions: mustModelExt(t, "gpt-4o-mini")}
	rec := httptest.NewRecorder()
	if err := openailegacy.WriteNonStreamJSON(t.Context(), rec, call, es, openailegacy.EncodeOptions{ExposeAipUsageExtensions: true}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Usage *struct {
			CostNanoUnits       int64  `json:"x_aip_cost_nano_units"`
			Currency            string `json:"x_aip_currency"`
			CostSource          string `json:"x_aip_cost_source"`
			PromptTokensDetails struct {
				UncachedTokens int `json:"x_aip_uncached_tokens"`
				CacheWrite     int `json:"x_aip_cache_write_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Usage == nil || v.Usage.CostNanoUnits != 99 || v.Usage.Currency != "USD" || v.Usage.CostSource != "estimated" {
		t.Fatalf("cost extensions = %+v", v.Usage)
	}
	if v.Usage.PromptTokensDetails.UncachedTokens != 3 || v.Usage.PromptTokensDetails.CacheWrite != 2 {
		t.Fatalf("cache extensions = %+v", v.Usage.PromptTokensDetails)
	}
}

// A producer that declares exclusive input semantics reports its input
// without the cache reads it also reports. The OpenAI prompt count contains
// the cached subset, so the prompt is never smaller than the cached tokens.
func TestWriteUsage_ExclusiveInputPreservesCounters(t *testing.T) {
	t.Parallel()
	for _, streaming := range []bool{false, true} {
		rec := httptest.NewRecorder()
		es := aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventUsageDelta, InputTokens: 300, CacheReadTokens: 600, Accounting: aipapi.UsageAccountingMetadata{InputTokensExcludeCacheRead: true}},
			{Kind: aipapi.EventResponseFinished},
		})
		encode := openailegacy.WriteNonStreamJSON
		if streaming {
			encode = openailegacy.WriteStreamSSE
		}
		if err := encode(t.Context(), rec, &aipapi.Call{}, es, openailegacy.EncodeOptions{ExposeAipUsageExtensions: true}); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"prompt_tokens":900`, `"total_tokens":900`, `"cached_tokens":600`, `"x_aip_uncached_tokens":300`} {
			if !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("stream=%v missing %s in %s", streaming, want, rec.Body.String())
			}
		}
	}
}

func TestWriteErrorJSON_shape(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	if err := openailegacy.WriteErrorJSON(rec, 400, "bad", "invalid_request_error", "empty"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	errObj, ok := v["error"].(map[string]any)
	if !ok {
		t.Fatalf("body %s", rec.Body.String())
	}
	if errObj["message"] != "bad" {
		t.Fatal(errObj)
	}
}

func TestWriteStreamSSE_roleContentDone(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "stream-ok"},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:y"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	opts := openailegacy.EncodeOptions{CompletionID: "chatcmpl_stream_ut", CreatedAt: 1715620000}
	if err := openailegacy.WriteStreamSSE(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if !strings.Contains(s, "chat.completion.chunk") {
		t.Fatalf("missing chunk object: %q", s)
	}
	if !strings.Contains(s, "stream-ok") {
		t.Fatalf("missing text: %q", s)
	}
	if !strings.Contains(s, "[DONE]") {
		t.Fatalf("missing done: %q", s)
	}
}

func TestWriteStreamSSE_incrementalTextDeltas(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventUsageDelta, InputTokens: 7, OutputTokens: 0},
		{Kind: aipapi.EventTextDelta, Delta: "hel"},
		{Kind: aipapi.EventTextDelta, Delta: "lo"},
		{Kind: aipapi.EventTextDelta, Delta: " world"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 0, OutputTokens: 3},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:y"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	opts := openailegacy.EncodeOptions{CompletionID: "chatcmpl_stream_incr", CreatedAt: 1715620000}
	if err := openailegacy.WriteStreamSSE(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var contentChunks []string
	var lastUsage struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
	}
	for _, fr := range frames {
		if fr.Data == "[DONE]" {
			continue
		}
		var v struct {
			Object  string `json:"object"`
			Choices []struct {
				Delta *struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		if v.Object != "chat.completion.chunk" {
			continue
		}
		if len(v.Choices) != 1 || v.Choices[0].Delta == nil {
			continue
		}
		d := v.Choices[0].Delta
		if d.Role != "" {
			continue
		}
		if v.Choices[0].FinishReason != nil {
			if v.Usage != nil {
				lastUsage.Prompt = v.Usage.Prompt
				lastUsage.Completion = v.Usage.Completion
			}
			continue
		}
		if d.Content != "" {
			contentChunks = append(contentChunks, d.Content)
		}
	}
	if len(contentChunks) != 3 || contentChunks[0] != "hel" || contentChunks[1] != "lo" || contentChunks[2] != " world" {
		t.Fatalf("content chunks: %#v", contentChunks)
	}
	if lastUsage.Prompt != 7 || lastUsage.Completion != 3 {
		t.Fatalf("usage got %+v", lastUsage)
	}
}

func TestWriteNonStreamJSON_toolCalls(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "fn1"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"a":1}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:y"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	if err := openailegacy.WriteNonStreamJSON(t.Context(), rec, call, es, openailegacy.EncodeOptions{CreatedAt: 1715620000}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   json.RawMessage `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Choices) != 1 || v.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("choice: %+v", v.Choices)
	}
	tc := v.Choices[0].Message.ToolCalls
	if len(tc) != 1 || tc[0].ID != "call_1" || tc[0].Function.Name != "fn1" || tc[0].Function.Arguments != `{"a":1}` {
		t.Fatalf("tool_calls %+v", tc)
	}
	var msgContent string
	if err := json.Unmarshal(v.Choices[0].Message.Content, &msgContent); err != nil {
		t.Fatal(err)
	}
	if msgContent != "hi" {
		t.Fatalf("content %q", msgContent)
	}
}

func TestWriteStreamSSE_toolCallDelta(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "w"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"x":true}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1"},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "x:y"},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	if err := openailegacy.WriteStreamSSE(t.Context(), rec, call, es, openailegacy.EncodeOptions{CompletionID: "cc_tool", CreatedAt: 1715620000}); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if !strings.Contains(s, `"tool_calls"`) || !strings.Contains(s, `"w"`) || !strings.Contains(s, "tool_calls") {
		t.Fatalf("body: %s", s)
	}
}

func TestWriteStreamSSE_reasoningAndTextDeltas(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "think"},
		{Kind: aipapi.EventTextDelta, Delta: "ans"},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:y"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	opts := openailegacy.EncodeOptions{CompletionID: "chatcmpl_reason_ut", CreatedAt: 1715620000}
	if err := openailegacy.WriteStreamSSE(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var reasoningChunks, contentChunks []string
	sawDone := false
	for _, fr := range frames {
		if fr.Data == "[DONE]" {
			sawDone = true
			continue
		}
		var v struct {
			Choices []struct {
				Delta *struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		if len(v.Choices) == 0 || v.Choices[0].Delta == nil {
			continue
		}
		d := v.Choices[0].Delta
		if d.ReasoningContent != "" || d.Reasoning != "" {
			reasoningChunks = append(reasoningChunks, d.ReasoningContent)
		}
		if d.Content != "" {
			contentChunks = append(contentChunks, d.Content)
		}
	}
	if !sawDone {
		t.Fatal("missing [DONE]")
	}
	if len(reasoningChunks) != 1 || reasoningChunks[0] != "think" {
		t.Fatalf("reasoning chunks: %#v", reasoningChunks)
	}
	if len(contentChunks) != 1 || contentChunks[0] != "ans" {
		t.Fatalf("content chunks: %#v", contentChunks)
	}
	s := rec.Body.String()
	if !strings.Contains(s, `"reasoning_content":"think"`) || !strings.Contains(s, `"reasoning":"think"`) {
		t.Fatalf("missing reasoning fields on wire: %q", s)
	}
	if !strings.Contains(s, `"content":"ans"`) {
		t.Fatalf("missing content on wire: %q", s)
	}
}

func TestWriteStreamSSE_reasoningSignatureDeltaNoOp(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "think"},
		{Kind: aipapi.EventReasoningSignatureDelta, Signature: "sig"},
		{Kind: aipapi.EventTextDelta, Delta: "ans"},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "x:y"},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	opts := openailegacy.EncodeOptions{CompletionID: "chatcmpl_signoop", CreatedAt: 1715620000}
	if err := openailegacy.WriteStreamSSE(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if strings.Contains(s, "signature") {
		t.Fatalf("stream body should not contain signature, got %q", s)
	}
	if !strings.Contains(s, `"reasoning_content":"think"`) || !strings.Contains(s, `"content":"ans"`) {
		t.Fatalf("reasoning and text output should be unchanged, got %q", s)
	}
}

func TestWriteNonStreamJSON_assistantMediaContentArray(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
		{Kind: aipapi.EventAssistantImageRef, AssistantRef: "https://img.example/x.png"},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "x:y"},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	if err := openailegacy.WriteNonStreamJSON(t.Context(), rec, call, es, openailegacy.EncodeOptions{CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	var parts []map[string]any
	if err := json.Unmarshal(v.Choices[0].Message.Content, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0]["type"] != "text" || parts[0]["text"] != "hi" {
		t.Fatalf("parts: %#v", parts)
	}
	if parts[1]["type"] != "image_url" {
		t.Fatalf("want image_url part: %#v", parts[1])
	}
}

func mustModelExt(tb testing.TB, model string) map[string]json.RawMessage {
	tb.Helper()
	raw, err := json.Marshal(model)
	if err != nil {
		tb.Fatal(err)
	}
	return map[string]json.RawMessage{"openailegacy.model": raw}
}

func assertJSONEqual(t *testing.T, wantJSON, gotJSON []byte) {
	t.Helper()
	var want, got any
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotJSON, &got); err != nil {
		t.Fatalf("got unmarshal: %v body=%s", err, string(gotJSON))
	}
	if !bytes.Equal(mustNormJSON(t, want), mustNormJSON(t, got)) {
		t.Fatalf("json mismatch\nwant: %s\ngot:  %s", string(wantJSON), string(gotJSON))
	}
}

func mustNormJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
