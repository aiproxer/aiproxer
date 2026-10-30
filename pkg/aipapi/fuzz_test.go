package aipapi_test

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzCallValidateJSON(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"Messages":[{"Role":"user","Parts":[{"Kind":"text","Text":"x"}]}]}`))
	f.Add([]byte(`{"Messages":[{"Role":"assistant","Parts":[{"Kind":"reasoning","Reasoning":{"Dialect":"openai.chat.reasoning_text.v1","Text":"think"}},{"Kind":"text","Text":"hi"}]}]}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, 512<<10)
		var c aipapi.Call
		if err := json.Unmarshal(raw, &c); err != nil {
			return
		}
		_ = c.Validate()
	})
}

func FuzzSemanticExtensionValidation(f *testing.F) {
	f.Add([]byte(`{"kind":"hint"}`))
	f.Add([]byte(`{"request":{"messages":[]}}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, aipapi.MaxSemanticExtensionDataBytes)
		call := aipapi.Call{
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "fuzz"}}}},
			SemanticExtensions: []aipapi.SemanticExtension{{
				Namespace: "aip", Type: "fuzz_hint", Implementor: "fuzzer", Direction: "request",
				Presence: aipapi.SemanticExtensionValue, Data: raw,
			}},
		}
		err := call.Validate()
		if err == nil {
			if !json.Valid(raw) {
				t.Fatalf("call.Validate() accepted invalid JSON data: %q", string(raw))
			}
			if len(raw) == 0 || len(raw) > aipapi.MaxSemanticExtensionDataBytes {
				t.Fatalf("call.Validate() accepted raw data length %d outside valid 1..%d byte range", len(raw), aipapi.MaxSemanticExtensionDataBytes)
			}
			var parsed any
			if unmarshalErr := json.Unmarshal(raw, &parsed); unmarshalErr != nil {
				t.Fatalf("call.Validate() accepted raw data that fails json.Unmarshal: %v", unmarshalErr)
			}
		} else {
			if len(raw) == 0 || !json.Valid(raw) {
				return // expected validation failure
			}
		}
	})
}

func FuzzMergeRouteQueryGenerationOptions(f *testing.F) {
	f.Add("temperature=0.5&max_output_tokens=3")
	f.Add("parallel_tool_calls=1")

	f.Fuzz(func(t *testing.T, q string) {
		q = testkit.CapString(q, 16<<10)
		v, err := url.ParseQuery(q)
		if err != nil {
			return
		}
		_, _ = aipapi.MergeRouteQueryIntoGenerationOptions(aipapi.GenerationOptions{}, v)
	})
}

func FuzzCollectWithLimitsProgram(f *testing.F) {
	f.Add([]byte{1, 2, 3})
	f.Add([]byte{0, 1, 2, 3, 4, 5})

	f.Fuzz(func(t *testing.T, b []byte) {
		b = testkit.CapBytes(b, 4096)
		evs := collectFuzzEvents(b)
		ctx := context.Background()
		_, _ = aipapi.CollectWithLimits(ctx, aipapi.NewFixedEventStream(evs), aipapi.CollectLimits{
			MaxTextBytes:             1 << 20,
			MaxReasoningBytes:        1 << 20,
			MaxToolArgsTotalBytes:    1 << 20,
			MaxWarnings:              1000,
			MaxAggregatePayloadBytes: 2 << 20,
		})
		// Also fuzz aggregate as standalone dimension with zero per-dimension limits.
		_, _ = aipapi.CollectWithLimits(ctx, aipapi.NewFixedEventStream(evs), aipapi.CollectLimits{
			MaxAggregatePayloadBytes: 1 << 20,
		})
	})
}

func collectFuzzEvents(b []byte) []aipapi.Event {
	evs := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
	}
	const chunk = 48
	for i := 0; i < len(b); i += chunk {
		j := min(i+chunk, len(b))
		if i >= j {
			break
		}
		switch b[i] % 4 {
		case 0:
			evs = append(evs, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: string(b[i:j])})
		case 1:
			evs = append(evs, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: string(b[i:j])})
		case 2:
			evs = append(evs, aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "fuzz-call", Delta: string(b[i:j])})
		default:
			// Always emit a structurally valid exact part inside a legal sequence.
			opaque := json.RawMessage(`{"v":1}`)
			if alt, err := json.Marshal(map[string]any{"v": 1, "n": len(b[i:j])}); err == nil {
				opaque = alt
			}
			evs = append(evs, aipapi.Event{
				Kind: aipapi.EventReasoningPart,
				Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
					Opaque:  opaque,
				},
			})
		}
	}
	evs = append(evs, aipapi.Event{Kind: aipapi.EventResponseFinished})
	return evs
}
