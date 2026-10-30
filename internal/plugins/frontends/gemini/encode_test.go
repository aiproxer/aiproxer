package gemini_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestWriteNonStreamJSON_textFromStream(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	call.Extensions = map[string]json.RawMessage{
		"gemini.model": json.RawMessage(`"gemini-2.0-flash"`),
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello-out"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteNonStreamJSON(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	cands, _ := body["candidates"].([]any)
	if len(cands) < 1 {
		t.Fatalf("candidates: %v", body)
	}
	c0 := testkit.MustMapStringAny(t, cands[0])
	content := testkit.MustMapStringAny(t, c0["content"])
	if content["role"] != "model" {
		t.Fatalf("role: %v", content["role"])
	}
	parts := testkit.MustSliceAny(t, content["parts"])
	p0 := testkit.MustMapStringAny(t, parts[0])
	if p0["text"] != "hello-out" {
		t.Fatalf("text: %v", p0["text"])
	}
}

func TestWriteNonStreamJSON_usageFromCollect(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	call.Extensions = map[string]json.RawMessage{
		"gemini.model": json.RawMessage(`"gemini-2.0-flash"`),
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventUsageDelta, InputTokens: 10, OutputTokens: 0},
		{Kind: aipapi.EventTextDelta, Delta: "out"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 0, OutputTokens: 5},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteNonStreamJSON(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		UsageMetadata *struct {
			Prompt     int `json:"promptTokenCount"`
			Candidates int `json:"candidatesTokenCount"`
			Total      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.UsageMetadata == nil {
		t.Fatal("usageMetadata is nil")
	}
	if body.UsageMetadata.Prompt != 10 || body.UsageMetadata.Candidates != 5 || body.UsageMetadata.Total != 15 {
		t.Fatalf("usageMetadata = %+v", *body.UsageMetadata)
	}
}

func TestWriteNonStreamJSONUsesClientVisibleScopedUsage(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	call.Extensions = map[string]json.RawMessage{
		"gemini.model": json.RawMessage(`"gemini-2.0-flash"`),
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{
			{InputTokens: 100, OutputTokens: 50, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable}},
			{InputTokens: 10, OutputTokens: 5, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible}},
		}},
		{Kind: aipapi.EventTextDelta, Delta: "out"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteNonStreamJSON(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		UsageMetadata *struct {
			Prompt     int `json:"promptTokenCount"`
			Candidates int `json:"candidatesTokenCount"`
			Total      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.UsageMetadata == nil {
		t.Fatal("usageMetadata is nil")
	}
	if body.UsageMetadata.Prompt != 10 || body.UsageMetadata.Candidates != 5 || body.UsageMetadata.Total != 15 {
		t.Fatalf("usageMetadata = %+v, want client-visible 10/5/15", *body.UsageMetadata)
	}
}

func TestWriteStreamSSE_dataLine(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "Z"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type %q", ct)
	}
	s := rec.Body.String()
	if !strings.Contains(s, "data: ") {
		t.Fatalf("body: %q", s)
	}
	if !strings.Contains(s, `"candidates"`) || !strings.Contains(s, `"Z"`) {
		t.Fatalf("body: %q", s)
	}
}

func TestWriteStreamSSE_incrementalTextDeltas(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
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
	rec := httptest.NewRecorder()
	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var texts []string
	var lastIn, lastOut int
	for _, fr := range frames {
		if fr.Data == "" || fr.Data == "[DONE]" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &body); err != nil {
			t.Fatal(err)
		}
		if u, ok := body["usageMetadata"].(map[string]any); ok {
			if x, ok := u["promptTokenCount"].(float64); ok {
				lastIn = int(x)
			}
			if x, ok := u["candidatesTokenCount"].(float64); ok {
				lastOut = int(x)
			}
		}
		cands, _ := body["candidates"].([]any)
		if len(cands) < 1 {
			continue
		}
		c0 := testkit.MustMapStringAny(t, cands[0])
		content := testkit.MustMapStringAny(t, c0["content"])
		parts := testkit.MustSliceAny(t, content["parts"])
		// The terminal candidate carries finishReason only, with no parts.
		if len(parts) < 1 {
			continue
		}
		p0 := testkit.MustMapStringAny(t, parts[0])
		txt, _ := p0["text"].(string)
		if txt != "" {
			texts = append(texts, txt)
		}
	}
	if len(texts) != 3 || texts[0] != "hel" || texts[1] != "lo" || texts[2] != " world" {
		t.Fatalf("texts %#v", texts)
	}
	if lastIn != 7 || lastOut != 3 {
		t.Fatalf("usageMetadata got in=%d out=%d", lastIn, lastOut)
	}
}

func TestWriteStreamSSEUsesClientVisibleScopedUsage(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{
			{InputTokens: 100, OutputTokens: 50, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable}},
			{InputTokens: 10, OutputTokens: 5, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible}},
		}},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()

	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"promptTokenCount":10`) || !strings.Contains(body, `"candidatesTokenCount":5`) || !strings.Contains(body, `"totalTokenCount":15`) {
		t.Fatalf("stream body %q does not contain client-visible usage", body)
	}
	if strings.Contains(body, `"promptTokenCount":100`) || strings.Contains(body, `"candidatesTokenCount":50`) {
		t.Fatalf("stream body %q contains provider usage", body)
	}
}

func TestWriteStreamSSE_functionCallChunk(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "pre"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "fc_1", ToolName: "compute"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "fc_1", Delta: `{"n":`},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "fc_1", Delta: `42}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "fc_1"},
		{Kind: aipapi.EventTextDelta, Delta: "post"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var fcSeen bool
	for _, fr := range frames {
		if fr.Data == "" || fr.Data == "[DONE]" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &body); err != nil {
			t.Fatal(err)
		}
		cands, _ := body["candidates"].([]any)
		if len(cands) == 0 {
			continue
		}
		c0 := testkit.MustMapStringAny(t, cands[0])
		content := testkit.MustMapStringAny(t, c0["content"])
		parts := testkit.MustSliceAny(t, content["parts"])
		for _, p := range parts {
			pm := testkit.MustMapStringAny(t, p)
			if fc, ok := pm["functionCall"].(map[string]any); ok {
				fcSeen = true
				if fc["name"] != "compute" {
					t.Fatalf("functionCall name: %v", fc["name"])
				}
				args, _ := fc["args"].(map[string]any)
				if args["n"] != float64(42) {
					t.Fatalf("functionCall args: %v", args)
				}
			}
		}
	}
	if !fcSeen {
		t.Fatal("expected functionCall in stream chunks")
	}
}

func TestWriteNonStreamJSON_functionCallOutput(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	call.Extensions = map[string]json.RawMessage{
		"gemini.model": json.RawMessage(`"gemini-2.0-flash"`),
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "fc_2", ToolName: "search"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "fc_2", Delta: `{"q":"test"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "fc_2"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteNonStreamJSON(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	cands, _ := body["candidates"].([]any)
	if len(cands) == 0 {
		t.Fatal("no candidates")
	}
	c0 := testkit.MustMapStringAny(t, cands[0])
	content := testkit.MustMapStringAny(t, c0["content"])
	parts := testkit.MustSliceAny(t, content["parts"])
	var fcSeen bool
	for _, p := range parts {
		pm := testkit.MustMapStringAny(t, p)
		if fc, ok := pm["functionCall"].(map[string]any); ok {
			fcSeen = true
			if fc["name"] != "search" {
				t.Fatalf("name: %v", fc["name"])
			}
		}
	}
	if !fcSeen {
		t.Fatalf("missing functionCall: %+v", parts)
	}
}

func TestWriteStreamSSE_thoughtPart(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: aipapi.EventTextDelta, Delta: "ans"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var thoughtSeen, textSeen bool
	for _, fr := range testkit.ParseRecorderSSE(rec) {
		if fr.Data == "" || fr.Data == "[DONE]" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &body); err != nil {
			t.Fatal(err)
		}
		cands, _ := body["candidates"].([]any)
		if len(cands) == 0 {
			continue
		}
		c0 := testkit.MustMapStringAny(t, cands[0])
		content := testkit.MustMapStringAny(t, c0["content"])
		parts := testkit.MustSliceAny(t, content["parts"])
		for _, p := range parts {
			pm := testkit.MustMapStringAny(t, p)
			txt, _ := pm["text"].(string)
			if txt == "plan" {
				if thought, _ := pm["thought"].(bool); thought {
					thoughtSeen = true
				}
			}
			if txt == "ans" {
				if _, ok := pm["thought"]; !ok {
					textSeen = true
				}
			}
		}
	}
	if !thoughtSeen {
		t.Fatal("missing thought part with thought=true")
	}
	if !textSeen {
		t.Fatal("missing normal text part without thought")
	}
}

func TestWriteStreamSSE_reasoningSignatureDeltaNoOp(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: aipapi.EventReasoningSignatureDelta, Signature: "sig"},
		{Kind: aipapi.EventTextDelta, Delta: "ans"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "signature") {
		t.Fatalf("stream body should not contain signature, got %q", body)
	}
	if !strings.Contains(body, "ans") {
		t.Fatalf("stream body should still contain text output, got %q", body)
	}
}

func TestWriteStreamSSE_usageDetails_defaultOmitsAipExtensions(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 5},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var usage map[string]any
	for _, fr := range testkit.ParseRecorderSSE(rec) {
		if fr.Data == "" || fr.Data == "[DONE]" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &body); err != nil {
			t.Fatal(err)
		}
		if u, ok := body["usageMetadata"].(map[string]any); ok {
			usage = u
		}
	}
	if usage == nil {
		t.Fatal("missing usageMetadata frame")
	}
	if usage["promptTokenCount"] != float64(100) || usage["candidatesTokenCount"] != float64(20) {
		t.Fatalf("token counts: %+v", usage)
	}
	if usage["cachedContentTokenCount"] != float64(30) {
		t.Fatalf("cachedContentTokenCount: %+v", usage)
	}
	for _, key := range []string{"xAipCostNanoUnits", "xAipCurrency", "xAipCostSource", "xAipUncachedTokens", "xAipCacheWriteTokens"} {
		if _, ok := usage[key]; ok {
			t.Fatalf("unexpected %q in default stream usageMetadata: %+v", key, usage)
		}
	}
}

func TestWriteStreamSSE_usageDetails_exposesAipExtensionsWhenConfigured(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 5, CostNanoUnits: 12345, Currency: "USD", CostSource: "provider"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	opts := gemini.EncodeOptions{ExposeAipUsageExtensions: true}
	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var usage map[string]any
	for _, fr := range testkit.ParseRecorderSSE(rec) {
		if fr.Data == "" || fr.Data == "[DONE]" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &body); err != nil {
			t.Fatal(err)
		}
		if u, ok := body["usageMetadata"].(map[string]any); ok {
			usage = u
		}
	}
	if usage == nil {
		t.Fatal("missing usageMetadata frame")
	}
	if usage["cachedContentTokenCount"] != float64(30) {
		t.Fatalf("cachedContentTokenCount: %+v", usage)
	}
	if usage["xAipCostNanoUnits"] != float64(12345) || usage["xAipCurrency"] != "USD" || usage["xAipCostSource"] != "provider" {
		t.Fatalf("cost extensions: %+v", usage)
	}
	if usage["xAipUncachedTokens"] != float64(70) || usage["xAipCacheWriteTokens"] != float64(5) {
		t.Fatalf("aip cache extensions: %+v", usage)
	}
}

func TestWriteNonStreamJSON_usageDetails_defaultOmitsAipExtensions(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	call.Extensions = map[string]json.RawMessage{
		"gemini.model": json.RawMessage(`"gemini-2.0-flash"`),
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventUsageDelta, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 5},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteNonStreamJSON(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	usage := testkit.MustMapStringAny(t, raw["usageMetadata"])
	if usage["promptTokenCount"] != float64(100) || usage["candidatesTokenCount"] != float64(20) {
		t.Fatalf("token counts: %+v", usage)
	}
	if usage["cachedContentTokenCount"] != float64(30) {
		t.Fatalf("cachedContentTokenCount: %+v", usage)
	}
	for _, key := range []string{"xAipCostNanoUnits", "xAipCurrency", "xAipCostSource", "xAipUncachedTokens", "xAipCacheWriteTokens"} {
		if _, ok := usage[key]; ok {
			t.Fatalf("unexpected %q in default usageMetadata: %+v", key, usage)
		}
	}
}

func TestWriteNonStreamJSON_usageDetails_exposesAipExtensionsWhenConfigured(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	call.Extensions = map[string]json.RawMessage{
		"gemini.model": json.RawMessage(`"gemini-2.0-flash"`),
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventUsageDelta, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 5, CostNanoUnits: 12345, Currency: "USD", CostSource: "provider"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	opts := gemini.EncodeOptions{ExposeAipUsageExtensions: true}
	if err := gemini.WriteNonStreamJSON(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	usage := testkit.MustMapStringAny(t, raw["usageMetadata"])
	if usage["cachedContentTokenCount"] != float64(30) {
		t.Fatalf("cachedContentTokenCount: %+v", usage)
	}
	if usage["xAipCostNanoUnits"] != float64(12345) || usage["xAipCurrency"] != "USD" || usage["xAipCostSource"] != "provider" {
		t.Fatalf("cost extensions: %+v", usage)
	}
	if usage["xAipUncachedTokens"] != float64(70) || usage["xAipCacheWriteTokens"] != float64(5) {
		t.Fatalf("aip cache extensions: %+v", usage)
	}
}
