package openairesponses

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func BenchmarkWriteStreamSSE_exactReasoningPart(b *testing.B) {
	opaque := json.RawMessage(`{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"status":"completed"}`)
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  opaque,
		}},
		{Kind: aipapi.EventResponseFinished},
	}
	call := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "stub:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	b.ReportAllocs()
	for b.Loop() {
		es := aipapi.NewFixedEventStream(events)
		rec := httptest.NewRecorder()
		if err := WriteStreamSSE(b.Context(), rec, call, es, EncodeOptions{ResponseID: "r", CreatedAt: 1}); err != nil {
			b.Fatal(err)
		}
	}
}
