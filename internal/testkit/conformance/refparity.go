package conformance

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"

	refacp "github.com/aiproxer/aiproxer/internal/refbackend/acp"
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

const parityText = "conformance-parity"

// NewSuccessRefBackend returns a reference backend whose streaming and non-streaming paths
// both surface parityText as assistant text (ACP keeps the stock emulator which answers "ok").
// Optional onRequestBody observes the raw upstream HTTP body after route/auth checks.
func NewSuccessRefBackend(tb testing.TB, backendID string, onRequestBody func([]byte)) *httptest.Server {
	tb.Helper()
	if backendID == BackendACP {
		srv := httptest.NewServer(refacp.NewHandler(refacp.Config{OnRequestBody: onRequestBody}))
		tb.Cleanup(srv.Close)
		return srv
	}
	inner := parityRefHandler(tb, backendID)
	h := inner
	if onRequestBody != nil {
		h = captureRequestBodyHandler(inner, onRequestBody)
	}
	srv := httptest.NewServer(h)
	tb.Cleanup(srv.Close)
	return srv
}

func captureRequestBodyHandler(inner http.Handler, onBody func([]byte)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			inner.ServeHTTP(w, r)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(b))
		onBody(b)
		inner.ServeHTTP(w, r)
	})
}

func parityRefHandler(tb testing.TB, backendID string) http.Handler {
	tb.Helper()
	switch backendID {
	case openairesponses.ID:
		ns := strings.Replace(openAIResponsesNonStreamDefault, `"text": "ok"`, `"text": "`+parityText+`"`, 1)
		ss := strings.Replace(openAIResponsesStreamDefault, "stream-ok", parityText, 1)
		return refopenairesponses.NewHandler(refopenairesponses.Config{
			NonStreamJSON: ns,
			StreamSSE:     ss,
		})
	case openailegacy.ID:
		ns := strings.Replace(openAILegacyNonStreamDefault, `"content":"ok"`, `"content":"`+parityText+`"`, 1)
		ss := strings.Replace(openAILegacyStreamDefault, "stream-ok", parityText, 1)
		return refopenaichat.NewHandler(refopenaichat.Config{
			NonStreamJSON: ns,
			StreamSSE:     ss,
		})
	case anthropic.ID:
		ns := strings.Replace(anthropicNonStreamDefault, `"text":"ok"`, `"text":"`+parityText+`"`, 1)
		return refanthropic.NewHandler(refanthropic.Config{
			NonStreamJSON: ns,
			StreamSSE:     anthropicParityStreamSSE(parityText),
		})
	case gemini.ID:
		ns := strings.Replace(geminiNonStreamDefault, `"text": "ok"`, `"text": "`+parityText+`"`, 1)
		ss := strings.Replace(geminiStreamDefault, "stream-ok", parityText, 1)
		return refgemini.NewHandler(refgemini.Config{
			NonStreamJSON: ns,
			StreamSSE:     ss,
		})
	case bedrock.ID:
		ns := strings.Replace(bedrockConverseDefault, `"text": "ok"`, `"text": "`+parityText+`"`, 1)
		return refbedrock.NewHandler(refbedrock.Config{
			ConverseJSON: ns,
			StreamEvents: bedrockParityStreamEvents(tb, parityText),
		})
	case BackendOpenResponses:
		// The generic OpenResponses backend consumes the OpenAI Responses-shaped wire.
		return newOpenResponsesParityOrigin(parityText)
	case BackendOpenRouter, BackendNVIDIA:
		// The OpenRouter/NVIDIA connector columns dispatch on the operation flavor:
		// openai.responses reaches the Responses wire, every other operation
		// reaches the chat-completions wire. The per-wire origin serves both with
		// deterministic parity text.
		return &connectorWire{text: parityText}
	default:
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	}
}

// openResponsesParityOrigin serves the rich OpenAI Responses-shaped wire (JSON
// resource + streaming SSE) with deterministic parity text, plus the /models
// discovery path the configured provider-mode inventory resolver queries.
type openResponsesParityOrigin struct {
	text string
}

func newOpenResponsesParityOrigin(text string) http.Handler {
	return &openResponsesParityOrigin{text: text}
}

func (p *openResponsesParityOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/models") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o-mini","object":"model","owned_by":"provider"}]}`)
		return
	}
	if isStreamingRequest(r) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, openResponsesRichSSE(p.text, 1715620000))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, openResponsesRichResource(p.text, 1715620000))
}

// openResponsesRichResource builds a completed OpenResponses response resource
// carrying text as the assistant output. The payload is a typed value encoded
// with encoding/json; no string is interpolated into the wire format.
func openResponsesRichResource(text string, created int64) string {
	payload := map[string]any{
		"id":         "resp_harness_1",
		"object":     "response",
		"created_at": created,
		"status":     "completed",
		"model":      "gpt-4o-mini",
		"output": []any{
			map[string]any{
				"type":    "message",
				"id":      "msg_harness_out",
				"status":  "completed",
				"role":    "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": text}},
			},
		},
		"usage": map[string]any{
			"input_tokens":  1,
			"output_tokens": 1,
			"total_tokens":  2,
		},
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// openResponsesRichSSE builds the full incremental OpenResponses SSE trajectory
// (created → item/part → delta → done → completed → [DONE]) carrying text. Every
// event payload is constructed as a typed value and encoded with encoding/json
// so no interpolated string can corrupt the wire format.
func openResponsesRichSSE(text string, created int64) string {
	item := func(status, itemText string) map[string]any {
		return map[string]any{
			"type":    "message",
			"id":      "msg_harness_1",
			"status":  status,
			"role":    "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": itemText}},
		}
	}
	response := func(status string, output []any) map[string]any {
		return map[string]any{
			"id":         "resp_harness_stream",
			"object":     "response",
			"created_at": created,
			"status":     status,
			"model":      "gpt-4o-mini",
			"output":     output,
		}
	}
	events := []struct {
		name string
		data map[string]any
	}{
		{"response.created", map[string]any{
			"type":            "response.created",
			"sequence_number": 1,
			"response":        response("in_progress", []any{}),
		}},
		{"response.output_item.added", map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": 2,
			"output_index":    0,
			"item":            item("in_progress", ""),
		}},
		{"response.content_part.added", map[string]any{
			"type":            "response.content_part.added",
			"sequence_number": 3,
			"item_id":         "msg_harness_1",
			"output_index":    0,
			"content_index":   0,
			"part":            map[string]any{"type": "output_text", "text": ""},
		}},
		{"response.output_text.delta", map[string]any{
			"type":            "response.output_text.delta",
			"sequence_number": 4,
			"item_id":         "msg_harness_1",
			"output_index":    0,
			"content_index":   0,
			"delta":           text,
		}},
		{"response.output_text.done", map[string]any{
			"type":            "response.output_text.done",
			"sequence_number": 5,
			"item_id":         "msg_harness_1",
			"output_index":    0,
			"content_index":   0,
			"text":            text,
		}},
		{"response.content_part.done", map[string]any{
			"type":            "response.content_part.done",
			"sequence_number": 6,
			"item_id":         "msg_harness_1",
			"output_index":    0,
			"content_index":   0,
		}},
		{"response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": 7,
			"output_index":    0,
			"item":            item("completed", text),
		}},
		{"response.completed", map[string]any{
			"type":            "response.completed",
			"sequence_number": 8,
			"response":        response("completed", []any{item("completed", text)}),
		}},
	}
	var buf bytes.Buffer
	for _, ev := range events {
		data, _ := json.Marshal(ev.data)
		buf.WriteString("event: " + ev.name + "\n")
		buf.WriteString("data: " + string(data) + "\n\n")
	}
	buf.WriteString("data: [DONE]\n\n")
	return buf.String()
}

// providerModeOrigin serves the /models discovery path the configured
// OpenAI-compatible provider-mode inventory queries, delegating every other
// request to the OpenAI Responses-shaped reference responder.
type providerModeOrigin struct {
	responses http.Handler
}

func (p *providerModeOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/models") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o-mini","object":"model","owned_by":"provider"}]}`)
		return
	}
	p.responses.ServeHTTP(w, r)
}

const openAIResponsesNonStreamDefault = `{
  "id": "resp_refbackend_1",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_out",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "ok"}
      ]
    }
  ]
}`

const openAIResponsesStreamDefault = "event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_refbackend_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"gpt-4o-mini\",\"output\":[{\"type\":\"message\",\"id\":\"m1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"stream-ok\"}]}]}}\n\n" +
	"data: [DONE]\n\n"

const openAILegacyNonStreamDefault = `{
  "id": "chatcmpl_refbackend_1",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
}`

const openAILegacyStreamDefault = "data: {\"id\":\"chatcmpl_refbackend_stream\"," +
	"\"object\":\"chat.completion.chunk\",\"created\":1715620000," +
	"\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0," +
	"\"delta\":{\"content\":\"stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: [DONE]\n\n"

const anthropicNonStreamDefault = `{
  "id": "msg_refbackend_1",
  "type": "message",
  "role": "assistant",
  "model": "claude-3-5-haiku-20241022",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`

const geminiNonStreamDefault = `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [{"text": "ok"}]
      }
    }
  ]
}`

const geminiStreamDefault = "data: " +
	"{\"candidates\":[{\"content\":{" +
	"\"role\":\"model\",\"parts\":[{\"text\":\"stream-ok\"}]}}]}\n\n"

const bedrockConverseDefault = `{
  "metrics": { "latencyMs": 1 },
  "output": {
    "message": {
      "role": "assistant",
      "content": [ { "text": "ok" } ]
    }
  },
  "stopReason": "end_turn",
  "usage": { "inputTokens": 1, "outputTokens": 1 }
}`

func anthropicParityStreamSSE(text string) string {
	escaped, _ := json.Marshal(text)
	return "event: message_start\ndata: " +
		`{"type":"message_start","message":{"id":"m_stream","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` +
		"\n\n" +
		"event: content_block_start\ndata: " +
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` +
		"\n\n" +
		"event: content_block_delta\ndata: " +
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(escaped) + `}}` +
		"\n\n" +
		"event: content_block_stop\ndata: " +
		`{"type":"content_block_stop","index":0}` +
		"\n\n" +
		"event: message_stop\ndata: " +
		`{"type":"message_stop"}` +
		"\n\n"
}

func bedrockParityStreamEvents(tb testing.TB, text string) []byte {
	tb.Helper()
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	events := []struct {
		eventType string
		payload   map[string]any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"text": text},
		}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	}
	for _, ev := range events {
		payload, err := json.Marshal(ev.payload)
		if err != nil {
			tb.Fatalf("bedrock parity stream: marshal %s: %v", ev.eventType, err)
		}
		msg := eventstream.Message{
			Headers: []eventstream.Header{
				{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.EventMessageType)},
				{Name: eventstreamapi.EventTypeHeader, Value: eventstream.StringValue(ev.eventType)},
				{Name: eventstreamapi.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
			},
			Payload: payload,
		}
		if err := enc.Encode(&buf, msg); err != nil {
			tb.Fatalf("bedrock parity stream: encode %s: %v", ev.eventType, err)
		}
	}
	return buf.Bytes()
}
