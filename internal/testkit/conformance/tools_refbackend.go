package conformance

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"

	refanthropic "github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
	refbedrock "github.com/aiproxer/aiproxer/internal/refbackend/bedrock"
	refgemini "github.com/aiproxer/aiproxer/internal/refbackend/gemini"
	refopenaichat "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	refopenairesponses "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
)

// NewToolRefBackend wires a reference backend emulator that completes a single tool call and
// includes usage fields where the wire shape supports it (Task 12.2).
func NewToolRefBackend(tb testing.TB, backendID string, onBody func([]byte)) *httptest.Server {
	tb.Helper()
	cfgBody := func(b []byte) {
		if onBody != nil {
			onBody(b)
		}
	}
	switch backendID {
	case openairesponses.ID:
		const toolStreamSSE = "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_int_t\",\"call_id\":\"call_fc\",\"name\":\"get_weather\",\"status\":\"in_progress\"}}\n\n" +
			"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":1,\"item_id\":\"fc_int_t\",\"output_index\":0,\"delta\":\"{\\\"q\\\":\"}\n\n" +
			"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":2,\"item_id\":\"fc_int_t\",\"output_index\":0,\"delta\":\"1}\"}\n\n" +
			"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"sequence_number\":3,\"item_id\":\"fc_int_t\",\"output_index\":0,\"name\":\"get_weather\",\"arguments\":\"{\\\"q\\\":1}\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"r_tool\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4o-mini\",\"usage\":{\"input_tokens\":3,\"output_tokens\":7,\"total_tokens\":10,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens_details\":{\"reasoning_tokens\":0}},\"output\":[{\"type\":\"function_call\",\"id\":\"fc_int_t\",\"name\":\"get_weather\",\"arguments\":\"{\\\"q\\\":1}\"}]}}\n\n" +
			"data: [DONE]\n\n"
		srv := httptest.NewServer(refopenairesponses.NewHandler(refopenairesponses.Config{
			StreamSSE:     toolStreamSSE,
			OnRequestBody: cfgBody,
		}))
		tb.Cleanup(srv.Close)
		return srv
	case openailegacy.ID:
		const refbackendToolCallsStreamSSE = "data: " +
			`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather"}}]},"finish_reason":null}]}` +
			"\n\n" + "data: " +
			`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]},"finish_reason":null}]}` +
			"\n\n" + "data: " +
			`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"NYC\"}"}}]},"finish_reason":null}]}` +
			"\n\n" + "data: " +
			`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` +
			"\n\n" + "data: " +
			`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}}` +
			"\n\n" + "data: [DONE]\n\n"
		srv := httptest.NewServer(refopenaichat.NewHandler(refopenaichat.Config{
			StreamSSE:     refbackendToolCallsStreamSSE,
			OnRequestBody: cfgBody,
		}))
		tb.Cleanup(srv.Close)
		return srv
	case anthropic.ID:
		const refbackendToolUseStreamSSE = "event: message_start\ndata: " +
			`{"type":"message_start","message":{"id":"m_tool","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` +
			"\n\n" +
			"event: content_block_start\ndata: " +
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}` +
			"\n\n" +
			"event: content_block_delta\ndata: " +
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"NYC\"}"}}` +
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
		srv := httptest.NewServer(refanthropic.NewHandler(refanthropic.Config{
			StreamSSE:     refbackendToolUseStreamSSE,
			OnRequestBody: cfgBody,
		}))
		tb.Cleanup(srv.Close)
		return srv
	case gemini.ID:
		const sse = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"get_temp\",\"args\":{\"city\":\"NYC\"},\"id\":\"call_gem_1\"}}]}}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":7,\"totalTokenCount\":10}}\n\n"
		srv := httptest.NewServer(refgemini.NewHandler(refgemini.Config{
			StreamSSE:     sse,
			OnRequestBody: cfgBody,
		}))
		tb.Cleanup(srv.Close)
		return srv
	case bedrock.ID:
		streamBody := bedrockToolStreamWithUsage(tb)
		srv := httptest.NewServer(refbedrock.NewHandler(refbedrock.Config{
			StreamEvents:  streamBody,
			OnRequestBody: cfgBody,
		}))
		tb.Cleanup(srv.Close)
		return srv
	case BackendOpenResponses, BackendOpenRouter, BackendNVIDIA:
		// The generic OpenResponses backend and the configured OpenAI-compatible
		// provider-mode route both consume the OpenAI Responses-shaped tool stream.
		srv := httptest.NewServer(&providerModeOrigin{
			responses: refopenairesponses.NewHandler(refopenairesponses.Config{
				AllowMissingBearer: true,
				StreamSSE:          openAICompatToolResponsesStreamSSE(DefaultModel(backendID)),
				OnRequestBody:      cfgBody,
			}),
		})
		tb.Cleanup(srv.Close)
		return srv
	default:
		tb.Fatalf("no tool refbackend for %q", backendID)
		return nil
	}
}

func openAICompatToolResponsesStreamSSE(model string) string {
	return "event: response.created\ndata: " + `{"type":"response.created","sequence_number":0,"response":{"id":"r_tool","object":"response","status":"in_progress","model":"` + model + `","output":[]}}` + "\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_int_t\",\"call_id\":\"call_fc\",\"name\":\"get_weather\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":2,\"item_id\":\"fc_int_t\",\"output_index\":0,\"delta\":\"{\\\"city\\\":\"}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":3,\"item_id\":\"fc_int_t\",\"output_index\":0,\"delta\":\"\\\"NYC\\\"}\"}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"sequence_number\":4,\"item_id\":\"fc_int_t\",\"output_index\":0,\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"NYC\\\"}\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":5,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_int_t\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"NYC\\\"}\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":6,\"response\":{\"id\":\"r_tool\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"" + model + "\",\"usage\":{\"input_tokens\":3,\"output_tokens\":7,\"total_tokens\":10,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens_details\":{\"reasoning_tokens\":0}},\"output\":[{\"type\":\"function_call\",\"id\":\"fc_int_t\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"NYC\\\"}\"}]}}\n\n" +
		"data: [DONE]\n\n"
}

func bedrockToolStreamWithUsage(tb testing.TB) []byte {
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
				"toolUse": map[string]any{"input": `{"city":"NYC"}`},
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

func bedrockAppendMetadata(tb testing.TB, prefix []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	_, _ = buf.Write(prefix)
	enc := eventstream.NewEncoder()
	payload := map[string]any{
		"metrics": map[string]any{"latencyMs": 1},
		"usage": map[string]any{
			"inputTokens":  3,
			"outputTokens": 7,
			"totalTokens":  10,
		},
	}
	bedrockAppendEventFrame(tb, &buf, enc, "metadata", payload)
	return buf.Bytes()
}

func bedrockAppendEventFrame(tb testing.TB, buf *bytes.Buffer, enc *eventstream.Encoder, typ string, payload map[string]any) {
	tb.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		tb.Fatal(err)
	}
	msg := eventstream.Message{
		Headers: []eventstream.Header{
			{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.EventMessageType)},
			{Name: eventstreamapi.EventTypeHeader, Value: eventstream.StringValue(typ)},
			{Name: eventstreamapi.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
		},
		Payload: b,
	}
	if err := enc.Encode(buf, msg); err != nil {
		tb.Fatal(err)
	}
}

func toolSchemaForBackend(backendID string) string {
	switch backendID {
	case openairesponses.ID:
		return `{"type":"object","properties":{"q":{"type":"integer"}}}`
	case gemini.ID:
		return `{"type":"object","properties":{"city":{"type":"string"}}}`
	default:
		return `{"type":"object","properties":{"city":{"type":"string"}}}`
	}
}

func toolNameForBackend(backendID string) string {
	if backendID == gemini.ID {
		return "get_temp"
	}
	return "get_weather"
}
