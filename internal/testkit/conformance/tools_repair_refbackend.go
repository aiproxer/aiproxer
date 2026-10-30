package conformance

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"

	refanthropic "github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
	refbedrock "github.com/aiproxer/aiproxer/internal/refbackend/bedrock"
	refopenaichat "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	refopenairesponses "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
)

// WantRepairedToolArgsJSON is the closed JSON expected after syntax repair for
// [NewToolCallRepairRefBackend] streams (gemini is skipped; see helper).
func WantRepairedToolArgsJSON(backendID string) string {
	if backendID == openairesponses.ID {
		return `{"q":1}`
	}
	return `{"city":"NYC"}`
}

func WantSafeTailPendingArgsJSON(backendID string) string {
	if backendID == openairesponses.ID {
		return `{"q":1}`
	}
	return `{"city":"NYC"}`
}

// NewToolCallRepairRefBackend wires a reference backend that emits intentionally
// terminal-comma tool-call argument JSON so canonical safe-tail repair must rewrite.
// Safe-tail engine/runtime and local-dogfood suites cover the schema-determined
// pending-value class; protocol adapters remain unaware of both repair classes.
func NewToolCallRepairRefBackend(tb testing.TB, backendID string) *httptest.Server {
	tb.Helper()
	switch backendID {
	case gemini.ID:
		tb.Skip("gemini wire materializes functionCall.args as a JSON object; syntax truncation is not exercisable")
		return nil
	case openairesponses.ID:
		// Safe-tail args JSON ends after a complete value plus a terminal comma.
		srv := httptest.NewServer(refopenairesponses.NewHandler(refopenairesponses.Config{
			StreamSSE: openAICompatToolResponsesTruncatedSSE("gpt-4o-mini", `{"q":1,`),
		}))
		tb.Cleanup(srv.Close)
		return srv
	case openailegacy.ID:
		const sse = "data: " +
			`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather"}}]},"finish_reason":null}]}` +
			"\n\n" + "data: " +
			`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"NYC\","}}]},"finish_reason":null}]}` +
			"\n\n" + "data: " +
			`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` +
			"\n\n" + "data: " +
			`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}}` +
			"\n\n" + "data: [DONE]\n\n"
		srv := httptest.NewServer(refopenaichat.NewHandler(refopenaichat.Config{StreamSSE: sse}))
		tb.Cleanup(srv.Close)
		return srv
	case anthropic.ID:
		const sse = "event: message_start\ndata: " +
			`{"type":"message_start","message":{"id":"m_tool","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` +
			"\n\n" +
			"event: content_block_start\ndata: " +
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}` +
			"\n\n" +
			"event: content_block_delta\ndata: " +
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"NYC\","}}` +
			"\n\n" +
			"event: content_block_stop\ndata: " +
			`{"type":"content_block_stop","index":0}` +
			"\n\n" +
			"event: message_delta\ndata: " +
			`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":""},"usage":{"input_tokens":3,"output_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"server_tool_use":{"web_fetch_requests":0,"web_search_requests":0}}}` +
			"\n\n" +
			"event: message_stop\ndata: " +
			`{"type":"message_stop"}` +
			"\n\n"
		srv := httptest.NewServer(refanthropic.NewHandler(refanthropic.Config{StreamSSE: sse}))
		tb.Cleanup(srv.Close)
		return srv
	case bedrock.ID:
		srv := httptest.NewServer(refbedrock.NewHandler(refbedrock.Config{StreamEvents: bedrockToolStreamTruncated(tb)}))
		tb.Cleanup(srv.Close)
		return srv
	case BackendOpenResponses, BackendOpenRouter, BackendNVIDIA:
		srv := httptest.NewServer(&providerModeOrigin{
			responses: refopenairesponses.NewHandler(refopenairesponses.Config{
				AllowMissingBearer: true,
				StreamSSE:          openAICompatToolResponsesTruncatedSSE(DefaultModel(backendID), `{"city":"NYC",`),
			}),
		})
		tb.Cleanup(srv.Close)
		return srv
	default:
		tb.Fatalf("no truncated tool-call-repair refbackend for %q", backendID)
		return nil
	}
}

func openAICompatToolResponsesTruncatedSSE(model, truncArgs string) string {
	events := []struct {
		event   string
		payload any
	}{
		{"response.created", map[string]any{
			"type": "response.created", "sequence_number": 0,
			"response": map[string]any{"id": "r_tool", "object": "response", "status": "in_progress", "model": model, "output": []any{}},
		}},
		{"response.output_item.added", map[string]any{
			"type": "response.output_item.added", "sequence_number": 1, "output_index": 0,
			"item": map[string]any{"type": "function_call", "id": "fc_int_t", "call_id": "call_fc", "name": "get_weather", "status": "in_progress"},
		}},
		{"response.function_call_arguments.delta", map[string]any{
			"type": "response.function_call_arguments.delta", "sequence_number": 2, "item_id": "fc_int_t", "output_index": 0, "delta": truncArgs,
		}},
		{"response.function_call_arguments.done", map[string]any{
			"type": "response.function_call_arguments.done", "sequence_number": 3, "item_id": "fc_int_t", "output_index": 0, "name": "get_weather", "arguments": truncArgs,
		}},
		{"response.output_item.done", map[string]any{
			"type": "response.output_item.done", "sequence_number": 4, "output_index": 0,
			"item": map[string]any{"type": "function_call", "id": "fc_int_t", "name": "get_weather", "arguments": truncArgs},
		}},
		{"response.completed", map[string]any{
			"type": "response.completed", "sequence_number": 5,
			"response": map[string]any{
				"id": "r_tool", "object": "response", "status": "completed", "model": model,
				"usage": map[string]any{
					"input_tokens": 3, "output_tokens": 7, "total_tokens": 10,
					"input_tokens_details":  map[string]any{"cached_tokens": 0},
					"output_tokens_details": map[string]any{"reasoning_tokens": 0},
				},
				"output": []any{map[string]any{"type": "function_call", "id": "fc_int_t", "name": "get_weather", "arguments": truncArgs}},
			},
		}},
	}
	var b strings.Builder
	for _, ev := range events {
		raw, err := json.Marshal(ev.payload)
		if err != nil {
			panic(err)
		}
		b.WriteString("event: ")
		b.WriteString(ev.event)
		b.WriteString("\ndata: ")
		b.Write(raw)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func bedrockToolStreamTruncated(tb testing.TB) []byte {
	tb.Helper()
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	events := []struct {
		eventType string
		payload   map[string]any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockStart", map[string]any{
			"contentBlockIndex": 0,
			"start": map[string]any{
				"toolUse": map[string]any{
					"toolUseId": "tool_use_int_1",
					"name":      "get_weather",
					"type":      "tool_use",
				},
			},
		}},
		{"contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta": map[string]any{
				"toolUse": map[string]any{"input": `{"city":"NYC",`},
			},
		}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "tool_use"}},
	}
	for _, ev := range events {
		bedrockAppendEventFrame(tb, &buf, enc, ev.eventType, ev.payload)
	}
	return bedrockAppendMetadata(tb, buf.Bytes())
}
