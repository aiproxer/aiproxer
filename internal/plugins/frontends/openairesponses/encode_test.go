package openairesponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
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
	opts := openairesponses.EncodeOptions{
		ResponseID: "resp_encode_golden",
		MessageID:  "msg_encode_golden",
		CreatedAt:  1715620000,
	}
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	want := readGolden(t, "response_nonstream_expected.json")
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
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, call, es, openairesponses.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Usage == nil {
		t.Fatal("usage is nil")
	}
	if got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want client-visible 10/5", *got.Usage)
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
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, call, es, openairesponses.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		ID        string `json:"id"`
		CreatedAt int64  `json:"created_at"`
		Output    []struct {
			ID string `json:"id"`
		} `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	wantID := "resp_" + diag.StableCallToken(call)
	if v.ID != wantID {
		t.Fatalf("response id %q, want %q", v.ID, wantID)
	}
	if v.CreatedAt != diag.StableUnix(call) {
		t.Fatalf("created_at %d, want %d", v.CreatedAt, diag.StableUnix(call))
	}
	if len(v.Output) == 0 || v.Output[0].ID != "msg_"+wantID {
		t.Fatalf("message id = %+v", v.Output)
	}
}

func TestWriteNonStreamJSON_usageFromCollect(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventUsageDelta, InputTokens: 9, OutputTokens: 0},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 0, OutputTokens: 2},
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
	opts := openairesponses.EncodeOptions{
		ResponseID: "resp_usage_ns",
		MessageID:  "msg_usage_ns",
		CreatedAt:  1715620000,
	}
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Usage == nil || v.Usage.InputTokens != 9 || v.Usage.OutputTokens != 2 || v.Usage.TotalTokens != 11 {
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
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, call, es, openairesponses.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Usage *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails struct {
				CachedTokens   int `json:"cached_tokens"`
				UncachedTokens int `json:"x_aip_uncached_tokens"`
				CacheWrite     int `json:"x_aip_cache_write_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Usage == nil {
		t.Fatal("usage is nil")
	}
	if v.Usage.InputTokens != 10 || v.Usage.OutputTokens != 0 || v.Usage.TotalTokens != 10 {
		t.Fatalf("top-level usage = %+v", *v.Usage)
	}
	if v.Usage.InputTokensDetails.CachedTokens != 7 || v.Usage.OutputTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("usage details = %+v", *v.Usage)
	}
	if v.Usage.InputTokensDetails.UncachedTokens != 0 || v.Usage.InputTokensDetails.CacheWrite != 0 {
		t.Fatalf("x_aip details should be hidden by default: %+v", *v.Usage)
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
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, call, es, openairesponses.EncodeOptions{ExposeAipUsageExtensions: true}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Usage *struct {
			CostNanoUnits      int64  `json:"x_aip_cost_nano_units"`
			Currency           string `json:"x_aip_currency"`
			CostSource         string `json:"x_aip_cost_source"`
			InputTokensDetails struct {
				UncachedTokens int `json:"x_aip_uncached_tokens"`
				CacheWrite     int `json:"x_aip_cache_write_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Usage == nil || v.Usage.CostNanoUnits != 99 || v.Usage.Currency != "USD" || v.Usage.CostSource != "estimated" {
		t.Fatalf("cost extensions = %+v", v.Usage)
	}
	if v.Usage.InputTokensDetails.UncachedTokens != 3 || v.Usage.InputTokensDetails.CacheWrite != 2 {
		t.Fatalf("cache extensions = %+v", v.Usage.InputTokensDetails)
	}
}

func TestWriteErrorJSON_preOutputShape(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteErrorJSON(rec, 400, "bad", "invalid_request_error", "empty"); err != nil {
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

func TestWriteStreamSSE_PrePumpCleanupRespectsCallerOwnership(t *testing.T) {
	t.Parallel()
	writeErr := errors.New("client disconnected")
	closeErr := errors.New("close failed")
	for _, wire := range []bool{false, true} {
		name := "canonical"
		if wire {
			name = "wire"
		}
		for _, stage := range []string{"response.created", "response.in_progress", "missing_flusher"} {
			t.Run(name+"/"+stage, func(t *testing.T) {
				es := &cleanupCountingStream{EventStream: aipapi.NewFixedEventStream(nil), closeErr: closeErr}
				rec := httptest.NewRecorder()
				fw := &initialEventFailWriter{ResponseRecorder: rec, failEvent: stage, err: writeErr}
				var w http.ResponseWriter = fw
				if stage == "missing_flusher" {
					w = struct{ http.ResponseWriter }{rec}
				}
				spec := (&openairesponses.Handler{}).Spec()
				var err error
				if wire {
					err = spec.WireWriteStream(t.Context(), w, frontendpipe.ResponseContext{}, es)
				} else {
					err = spec.WriteStream(t.Context(), w, &aipapi.Call{}, es, openairesponses.EncodeOptions{})
				}
				if stage == "missing_flusher" {
					if err == nil || !strings.Contains(err.Error(), "not a Flusher") {
						t.Fatalf("error = %v, want missing flusher", err)
					}
				} else if err != writeErr {
					t.Fatalf("error = %v, want primary write error", err)
				}
				wantWrites := 0
				wantBody := ""
				switch stage {
				case "response.created":
					wantWrites = 1
				case "response.in_progress":
					wantWrites = 2
					wantBody = "event: response.created\n"
				}
				if fw.writes != wantWrites || (wantBody == "" && rec.Body.Len() != 0) || (wantBody != "" && !strings.HasPrefix(rec.Body.String(), wantBody)) {
					t.Fatalf("writes/body = %d/%q, want %d/prefix %q", fw.writes, rec.Body.String(), wantWrites, wantBody)
				}
				if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" {
					t.Fatalf("status/headers = %d/%v", rec.Code, rec.Header())
				}
				wantCloses := int32(1)
				if wire {
					wantCloses = 0 // replayCandidate retains its raw-stream fallback close.
				}
				if es.recvs.Load() != 0 || es.closes.Load() != wantCloses {
					t.Fatalf("Recv/Close = %d/%d, want 0/%d", es.recvs.Load(), es.closes.Load(), wantCloses)
				}
			})
		}
	}
}

func TestWriteStreamSSE_PumpRetainsCloseAndErrorOwnership(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("close failed")
	for _, wire := range []bool{false, true} {
		name := "canonical"
		if wire {
			name = "wire"
		}
		for _, terminal := range []aipapi.EventKind{aipapi.EventResponseFinished, aipapi.EventError} {
			t.Run(name+"/"+string(terminal), func(t *testing.T) {
				es := &cleanupCountingStream{
					EventStream: aipapi.NewFixedEventStream([]aipapi.Event{{Kind: terminal, ErrorCode: "upstream_failure", ErrorMessage: "failed"}}),
					closeErr:    closeErr,
				}
				rec := httptest.NewRecorder()
				spec := (&openairesponses.Handler{}).Spec()
				var err error
				if wire {
					err = spec.WireWriteStream(t.Context(), rec, frontendpipe.ResponseContext{}, es)
				} else {
					err = spec.WriteStream(t.Context(), rec, &aipapi.Call{}, es, openairesponses.EncodeOptions{})
				}
				if !errors.Is(err, closeErr) || es.closes.Load() != 1 {
					t.Fatalf("error/Close = %v/%d, want close error/1", err, es.closes.Load())
				}
				var streamErr *aipapi.StreamError
				if terminal == aipapi.EventError {
					if !errors.As(err, &streamErr) || streamErr.Code != "upstream_failure" || strings.Contains(rec.Body.String(), "[DONE]") {
						t.Fatalf("error/body = %v/%q, want stream error without DONE", err, rec.Body.String())
					}
				} else if !strings.Contains(rec.Body.String(), "event: response.completed\n") || !strings.HasSuffix(rec.Body.String(), "data: [DONE]\n\n") {
					t.Fatalf("missing completion framing: %q", rec.Body.String())
				}
			})
		}
	}
}

type cleanupCountingStream struct {
	aipapi.EventStream
	closeErr error
	recvs    atomic.Int32
	closes   atomic.Int32
}

func (s *cleanupCountingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.recvs.Add(1)
	return s.EventStream.Recv(ctx)
}

func (s *cleanupCountingStream) Close() error {
	s.closes.Add(1)
	return s.closeErr
}

type initialEventFailWriter struct {
	*httptest.ResponseRecorder
	failEvent string
	err       error
	writes    int
}

func (w *initialEventFailWriter) Write(p []byte) (int, error) {
	w.writes++
	if bytes.HasPrefix(p, []byte("event: "+w.failEvent+"\n")) {
		return 0, w.err
	}
	return w.ResponseRecorder.Write(p)
}

func TestWriteStreamSSE_containsCompletedAndDone(t *testing.T) {
	t.Parallel()
	es := &cleanupCountingStream{EventStream: aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "stream-ok"},
		{Kind: aipapi.EventResponseFinished},
	})}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:y"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	opts := openairesponses.EncodeOptions{ResponseID: "resp_stream_ut", CreatedAt: 1715620000}
	if err := openairesponses.WriteStreamSSE(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if !strings.Contains(s, "response.completed") {
		t.Fatalf("missing completed: %q", s)
	}
	if !strings.Contains(s, "stream-ok") {
		t.Fatalf("missing text: %q", s)
	}
	if !strings.Contains(s, "[DONE]") {
		t.Fatalf("missing done: %q", s)
	}
	if es.closes.Load() != 1 {
		t.Fatalf("Close = %d, want 1", es.closes.Load())
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
	opts := openairesponses.EncodeOptions{ResponseID: "resp_stream_incr", MessageID: "msg_stream_incr", CreatedAt: 1715620000}
	if err := openairesponses.WriteStreamSSE(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var seq []string
	var doneText string
	var firstTextDeltaItemID string
	firstTextDeltaOutputIndex := -1
	var completedUsage struct {
		In    int `json:"input_tokens"`
		Out   int `json:"output_tokens"`
		Total int `json:"total_tokens"`
	}
	for _, fr := range frames {
		if fr.Data == "[DONE]" {
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
		if typ == "response.output_text.delta" {
			d, _ := v["delta"].(string)
			if d == "" {
				t.Fatalf("empty delta in %s", fr.Data)
			}
			if firstTextDeltaItemID == "" {
				firstTextDeltaItemID, _ = v["item_id"].(string)
				if idx, ok := v["output_index"].(float64); ok {
					firstTextDeltaOutputIndex = int(idx)
				}
			}
		}
		if typ == "response.output_text.done" {
			txt, _ := v["text"].(string)
			doneText = txt
		}
		if typ == "response.completed" {
			resp, _ := v["response"].(map[string]any)
			if resp == nil {
				t.Fatal("missing response")
			}
			if u, ok := resp["usage"].(map[string]any); ok {
				if x, ok := u["input_tokens"].(float64); ok {
					completedUsage.In = int(x)
				}
				if x, ok := u["output_tokens"].(float64); ok {
					completedUsage.Out = int(x)
				}
				if x, ok := u["total_tokens"].(float64); ok {
					completedUsage.Total = int(x)
				}
			}
		}
	}
	var deltaCount int
	for _, typ := range seq {
		if typ == "response.output_text.delta" {
			deltaCount++
		}
	}
	if deltaCount != 3 {
		t.Fatalf("delta count %d seq %v", deltaCount, seq)
	}
	if firstTextDeltaItemID != "msg_stream_incr" || firstTextDeltaOutputIndex != 0 {
		t.Fatalf("text delta item_id/output_index = %q/%d", firstTextDeltaItemID, firstTextDeltaOutputIndex)
	}
	if doneText != "hello world" {
		t.Fatalf("done text %q", doneText)
	}
	if completedUsage.In != 7 || completedUsage.Out != 3 || completedUsage.Total != 10 {
		t.Fatalf("usage %+v", completedUsage)
	}
	itemDoneIdx, completedIdx := -1, -1
	for i, typ := range seq {
		if typ == "response.output_item.done" && itemDoneIdx == -1 {
			itemDoneIdx = i
		}
		if typ == "response.completed" {
			completedIdx = i
		}
	}
	if itemDoneIdx < 0 || completedIdx < 0 || itemDoneIdx >= completedIdx {
		t.Fatalf("message output_item.done (idx %d) must precede response.completed (idx %d); seq %v", itemDoneIdx, completedIdx, seq)
	}
}

func TestWriteNonStreamJSON_functionCallOutput(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "fn1"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"z":2}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: aipapi.EventTextDelta, Delta: "t"},
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
	opts := openairesponses.EncodeOptions{ResponseID: "resp_tool_ns", MessageID: "msg_tool_ns", CreatedAt: 1715620000}
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Output) < 2 {
		t.Fatalf("output: %+v", v.Output)
	}
	if v.Output[1]["type"] != "function_call" || v.Output[1]["name"] != "fn1" {
		t.Fatalf("fc: %+v", v.Output[1])
	}
}

func TestWriteStreamSSE_reasoningDeltaDoesNotBreakCompletion(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "think-step"},
		{Kind: aipapi.EventTextDelta, Delta: "answer"},
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
	opts := openairesponses.EncodeOptions{ResponseID: "resp_re", CreatedAt: 1715620000}
	if err := openairesponses.WriteStreamSSE(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var seq []string
	var reasoningDelta, textDelta map[string]any
	var completedOutput []any
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
		case "response.reasoning_summary_text.delta":
			reasoningDelta = v
		case "response.output_text.delta":
			textDelta = v
		case "response.completed":
			resp, _ := v["response"].(map[string]any)
			completedOutput, _ = resp["output"].([]any)
		}
	}
	wantSeq := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done",
		"response.reasoning_summary_part.done",
		"response.output_item.done",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	if strings.Join(seq, ",") != strings.Join(wantSeq, ",") {
		t.Fatalf("event sequence:\n got %v\nwant %v", seq, wantSeq)
	}
	if reasoningDelta == nil || reasoningDelta["item_id"] != "rs_resp_re" ||
		reasoningDelta["output_index"] != float64(0) ||
		reasoningDelta["summary_index"] != float64(0) ||
		reasoningDelta["delta"] != "think-step" {
		t.Fatalf("reasoning delta metadata: %+v", reasoningDelta)
	}
	if textDelta == nil || textDelta["item_id"] != "msg_resp_re" ||
		textDelta["output_index"] != float64(1) ||
		textDelta["delta"] != "answer" {
		t.Fatalf("text delta metadata: %+v", textDelta)
	}
	if len(completedOutput) != 2 {
		t.Fatalf("completed output len: %d", len(completedOutput))
	}
	if o0, _ := completedOutput[0].(map[string]any); o0 == nil || o0["type"] != "reasoning" {
		t.Fatalf("completed output[0] must be reasoning: %+v", completedOutput[0])
	}
	if o1, _ := completedOutput[1].(map[string]any); o1 == nil || o1["type"] != "message" {
		t.Fatalf("completed output[1] must be message: %+v", completedOutput[1])
	}
}

func TestWriteNonStreamJSON_reasoningOutputItem(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "plan "},
		{Kind: aipapi.EventReasoningDelta, Delta: "step"},
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
	opts := openairesponses.EncodeOptions{ResponseID: "resp_re_ns", MessageID: "msg_re_ns", CreatedAt: 1715620000}
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Output []struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Status  string `json:"status"`
			Summary []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"summary"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Output) < 2 {
		t.Fatalf("output: %+v", v.Output)
	}
	if v.Output[0].Type != "reasoning" {
		t.Fatalf("output[0] type %q", v.Output[0].Type)
	}
	if v.Output[0].ID != "rs_resp_re_ns" {
		t.Fatalf("output[0] id %q", v.Output[0].ID)
	}
	if v.Output[0].Status != "completed" {
		t.Fatalf("output[0] status %q", v.Output[0].Status)
	}
	if len(v.Output[0].Summary) != 1 || v.Output[0].Summary[0].Type != "summary_text" || v.Output[0].Summary[0].Text != "plan step" {
		t.Fatalf("summary: %+v", v.Output[0].Summary)
	}
	if v.Output[1].Type != "message" {
		t.Fatalf("output[1] type %q", v.Output[1].Type)
	}
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(v.Output[1].Content, &content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 || content[0].Text != "ans" {
		t.Fatalf("message content: %+v", content)
	}
}

func TestWriteStreamSSE_toolCallEvents(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "pre"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "fn_a"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"ci`},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `ty":"ny"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: aipapi.EventTextDelta, Delta: "post"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 5, OutputTokens: 8},
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
	opts := openairesponses.EncodeOptions{ResponseID: "resp_tc", MessageID: "msg_tc", CreatedAt: 1715620000}
	if err := openairesponses.WriteStreamSSE(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var argDeltas, addedTypes []string
	var doneArgs string
	var completedFC map[string]any
	for _, fr := range frames {
		if fr.Data == "[DONE]" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		typ, _ := v["type"].(string)
		switch typ {
		case "response.output_item.added":
			item, _ := v["item"].(map[string]any)
			if item != nil && item["type"] == "function_call" {
				addedTypes = append(addedTypes, "function_call")
			}
		case "response.function_call_arguments.delta":
			d, _ := v["delta"].(string)
			argDeltas = append(argDeltas, d)
		case "response.function_call_arguments.done":
			doneArgs, _ = v["arguments"].(string)
		case "response.completed":
			resp, _ := v["response"].(map[string]any)
			out, _ := resp["output"].([]any)
			for _, item := range out {
				m, _ := item.(map[string]any)
				if m != nil && m["type"] == "function_call" {
					completedFC = m
				}
			}
		}
	}
	if len(addedTypes) == 0 || addedTypes[0] != "function_call" {
		t.Fatalf("expected function_call output_item.added, got %v", addedTypes)
	}
	if len(argDeltas) != 2 || argDeltas[0] != `{"ci` || argDeltas[1] != `ty":"ny"}` {
		t.Fatalf("arg deltas: %#v", argDeltas)
	}
	if doneArgs != `{"city":"ny"}` {
		t.Fatalf("done args: %q", doneArgs)
	}
	if completedFC == nil || completedFC["name"] != "fn_a" || completedFC["status"] != "completed" {
		t.Fatalf("completed function_call: %+v", completedFC)
	}
}

func TestWriteStreamSSE_reasoningSignatureDeltaNoOp(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "plan"},
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
	opts := openairesponses.EncodeOptions{ResponseID: "resp_signoop", MessageID: "msg_signoop", CreatedAt: 1715620000}
	if err := openairesponses.WriteStreamSSE(t.Context(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if strings.Contains(s, "signature") {
		t.Fatalf("stream body should not contain signature, got %q", s)
	}
	if !strings.Contains(s, "response.reasoning_summary_text.delta") {
		t.Fatalf("reasoning delta should be present, got %q", s)
	}
	if !strings.Contains(s, "response.output_text.delta") {
		t.Fatalf("text delta should be present, got %q", s)
	}
}

func TestWriteNonStreamJSON_messageContentIncludesAssistantImageRef(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "x"},
		{Kind: aipapi.EventAssistantImageRef, AssistantRef: "https://example.com/p.png"},
		{Kind: aipapi.EventResponseFinished},
	})
	call := &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "a:b"},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("u")}}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(t.Context(), rec, call, es, openairesponses.EncodeOptions{CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Output) < 1 {
		t.Fatal("missing output")
	}
	var msg struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(body.Output[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "message" {
		t.Fatalf("type %q", msg.Type)
	}
	var content []map[string]any
	if err := json.Unmarshal(msg.Content, &content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 2 {
		t.Fatalf("content len %d: %v", len(content), content)
	}
	if content[0]["type"] != "output_text" || content[0]["text"] != "x" {
		t.Fatalf("part0: %#v", content[0])
	}
	if content[1]["type"] != "input_image" || content[1]["image_url"] != "https://example.com/p.png" {
		t.Fatalf("part1: %#v", content[1])
	}
}

func mustModelExt(tb testing.TB, model string) map[string]json.RawMessage {
	tb.Helper()
	raw, err := json.Marshal(model)
	if err != nil {
		tb.Fatal(err)
	}
	return map[string]json.RawMessage{"openairesponses.model": raw}
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
