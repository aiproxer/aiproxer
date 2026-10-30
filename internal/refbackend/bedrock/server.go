// Package bedrock is a reference backend emulator for Amazon Bedrock Runtime
// Converse and ConverseStream. It is test-support only and must not be imported
// from aipcore or protocol plugins.
package bedrock

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/refbackend/jsonprobe"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"
)

const maxBodyBytes = 10 << 20

// Config tunes the emulator handler.
type Config struct {
	// AllowMissingAuthorization, if true, skips the presence check for the
	// Authorization header (SigV4 clients always send it).
	AllowMissingAuthorization bool
	// OnRequestBody is invoked with the full request body after a successful route
	// check and before the response is written.
	OnRequestBody func(body []byte)
	// ConverseJSON overrides the JSON body for Converse (non-stream). When empty, a
	// minimal completed Converse response is returned.
	ConverseJSON string
	// StreamEvents overrides the raw application/vnd.amazon.eventstream body for
	// ConverseStream. When empty, a minimal assistant text stream is returned.
	StreamEvents []byte
}

// NewHandler returns an http.Handler that emulates POST /model/{modelId}/converse
// and POST /model/{modelId}/converse-stream for the official AWS SDK v2 client.
func NewHandler(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		path := r.URL.Path
		if !strings.HasPrefix(path, "/model/") {
			http.NotFound(w, r)
			return
		}
		var op string
		switch {
		case strings.HasSuffix(path, "/converse-stream"):
			op = "converse-stream"
		case strings.HasSuffix(path, "/converse"):
			op = "converse"
		default:
			http.NotFound(w, r)
			return
		}

		if !cfg.AllowMissingAuthorization {
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "missing authorization", http.StatusUnauthorized)
				return
			}
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if cfg.OnRequestBody != nil {
			cfg.OnRequestBody(body)
		}
		if jsonprobe.HasJSONNumber(body, "temperature", 0.11) {
			http.Error(w, `{"message":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		if jsonprobe.HasJSONNumber(body, "temperature", 0.22) {
			http.Error(w, `{"message":"bad request"}`, http.StatusBadRequest)
			return
		}

		switch op {
		case "converse":
			writeConverse(w, cfg, body)
		case "converse-stream":
			writeConverseStream(w, cfg, body)
		}
	})
}

func writeConverse(w http.ResponseWriter, cfg Config, requestBody []byte) {
	body := cfg.ConverseJSON
	if body == "" {
		body = nonStreamWithUsageJSON
		if jsonprobe.HasJSONKey(requestBody, "toolConfig") {
			body = nonStreamWithToolCallJSON
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(body)); err != nil {
		slog.Default().Warn("refbackend/bedrock: write converse response", "error", err)
	}
}

func writeConverseStream(w http.ResponseWriter, cfg Config, requestBody []byte) {
	body := cfg.StreamEvents
	if len(body) == 0 {
		body = streamWithUsageEvents()
		if jsonprobe.HasJSONKey(requestBody, "toolConfig") {
			body = streamWithToolCallEvents()
		}
		if jsonprobe.HasJSONNumber(requestBody, "maxTokens", 0) || jsonprobe.HasJSONNumber(requestBody, "maxTokens", 1) {
			body = streamWithZeroUsageEvents()
		}
	}
	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		slog.Default().Warn("refbackend/bedrock: write converse stream response", "error", err)
	}
}

const defaultConverseJSON = `{
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

const nonStreamWithUsageJSON = `{
  "metrics": { "latencyMs": 1 },
  "output": {
    "message": {
      "role": "assistant",
      "content": [ { "text": "ok" } ]
    }
  },
  "stopReason": "end_turn",
  "usage": { "inputTokens": 10, "outputTokens": 5, "totalTokens": 15 }
}`

const nonStreamWithZeroUsageJSON = `{
  "metrics": { "latencyMs": 1 },
  "output": {
    "message": {
      "role": "assistant",
      "content": [ { "text": "ok" } ]
    }
  },
  "stopReason": "end_turn",
  "usage": { "inputTokens": 0, "outputTokens": 0, "totalTokens": 0 }
}`

const nonStreamWithToolCallJSON = `{
  "metrics": { "latencyMs": 1 },
  "output": {
    "message": {
      "role": "assistant",
      "content": [ { "text": null } ]
    }
  },
  "stopReason": "tool_use",
  "usage": { "inputTokens": 10, "outputTokens": 5, "totalTokens": 15 }
}`

// RefusalStreamEvents returns converse-stream events identical to the usage
// stream except the terminal stopReason reports guardrail_intervened,
// scripting a content-filter refusal for terminal-semantics tests.
func RefusalStreamEvents() []byte {
	return converseStreamEventsHelper([]struct {
		eventType string
		payload   map[string]any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"text": "I cannot help with that"},
		}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "guardrail_intervened"}},
		{"metadata", map[string]any{"usage": map[string]any{"inputTokens": 10, "outputTokens": 5, "totalTokens": 15}}},
	})
}

// LimitStreamEvents returns converse-stream events identical to the usage
// stream except the terminal stopReason reports max_tokens, scripting an
// output-token-limit truncation for terminal-semantics tests.
func LimitStreamEvents() []byte {
	return converseStreamEventsHelper([]struct {
		eventType string
		payload   map[string]any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"text": "cut"},
		}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "max_tokens"}},
		{"metadata", map[string]any{"usage": map[string]any{"inputTokens": 10, "outputTokens": 5, "totalTokens": 15}}},
	})
}

func defaultConverseStreamEvents() []byte {
	return converseStreamEventsHelper([]struct {
		eventType string
		payload   map[string]any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"text": "stream-ok"},
		}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	})
}

func streamWithUsageEvents() []byte {
	return converseStreamEventsHelper([]struct {
		eventType string
		payload   map[string]any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"text": "stream-ok"},
		}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
		{"metadata", map[string]any{"usage": map[string]any{"inputTokens": 10, "outputTokens": 5, "totalTokens": 15}}},
	})
}

func streamWithZeroUsageEvents() []byte {
	return converseStreamEventsHelper([]struct {
		eventType string
		payload   map[string]any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"text": "stream-ok"},
		}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
		{"metadata", map[string]any{"usage": map[string]any{"inputTokens": 0, "outputTokens": 0, "totalTokens": 0}}},
	})
}

func streamWithToolCallEvents() []byte {
	return converseStreamEventsHelper([]struct {
		eventType string
		payload   map[string]any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockStart", map[string]any{
			"contentBlockIndex": 0,
			"start":             map[string]any{"toolUse": map[string]any{"toolUseId": "call_1", "name": "weather"}},
		}},
		{"contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"toolUse": map[string]any{"input": "{}"}},
		}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "tool_use"}},
		{"metadata", map[string]any{"usage": map[string]any{"inputTokens": 10, "outputTokens": 5, "totalTokens": 15}}},
	})
}

func converseStreamEventsHelper(events []struct {
	eventType string
	payload   map[string]any
},
) []byte {
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	for _, ev := range events {
		payload, err := json.Marshal(ev.payload)
		if err != nil {
			slog.Default().Warn("refbackend/bedrock: marshal stream fixture payload", "error", err)
			continue
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
			slog.Default().Warn("refbackend/bedrock: encode stream fixture", "error", err)
			return buf.Bytes()
		}
	}
	return buf.Bytes()
}
