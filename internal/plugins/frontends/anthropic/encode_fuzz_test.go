package anthropic_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// FuzzWriteNonStreamJSON_toolArguments exercises json.Unmarshal on tool call arguments during encode.
func FuzzWriteNonStreamJSON_toolArguments(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"a":1}`))

	f.Fuzz(func(t *testing.T, args []byte) {
		args = testkit.CapBytes(args, 32<<10)
		call := &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "stub:claude"},
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("hi")},
			}},
		}
		evs := []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventToolCallStarted, ToolCallID: "idfuz", ToolName: "fn"},
			{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "idfuz", Delta: string(args)},
			{Kind: aipapi.EventToolCallFinished, ToolCallID: "idfuz"},
			{Kind: aipapi.EventResponseFinished},
		}
		rec := httptest.NewRecorder()
		_ = anthropic.WriteNonStreamJSON(context.Background(), rec, call, aipapi.NewFixedEventStream(evs), anthropic.EncodeOptions{})
	})
}
