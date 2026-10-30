package conformance

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Shared OpenAI-compatible wire origin for the optional connector columns.
//
// The real connectors/openrouter and connectors/nvidia executables map each call
// operation to an OpenAI-compatible wire by flavor: openai.responses and any
// explicit responses flavor extension select the OpenAI Responses wire, while
// every other operation (openai.chat_completions, openresponses.create, and the
// empty operation produced by the Anthropic/Gemini frontends) selects the
// chat-completions wire. The connector-column harness origins therefore dispatch
// on the requested path (/chat/completions vs /responses) so the matrix
// exercises the actual wire the connector requests — never a forced Responses
// substitution.

// connectorWire serves the OpenAI-compatible wires a connector requests per
// operation flavor. chat, when non-nil, is the /chat/completions responder;
// responses, when non-nil, is the /responses responder; /models serves inventory
// discovery. nil responders fall back to the deterministic text wire carrying
// text.
type connectorWire struct {
	chat      http.Handler
	responses http.Handler
	text      string
}

// ServeHTTP dispatches a connector request to the wire the connector selected by
// its operation flavor and serves /models inventory discovery.
func (w *connectorWire) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	if isConnectorModelsRequest(req) {
		res.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(res, `{"object":"list","data":[{"id":"gpt-4o-mini","object":"model","owned_by":"provider"}]}`)
		return
	}
	if strings.TrimRight(req.URL.Path, "/") == "/chat/completions" {
		if w.chat != nil {
			w.chat.ServeHTTP(res, req)
			return
		}
		serveConnectorChat(res, req, w.text)
		return
	}
	if w.responses != nil {
		w.responses.ServeHTTP(res, req)
		return
	}
	serveConnectorResponses(res, req, w.text)
}

// isConnectorModelsRequest reports whether req targets the /models discovery
// path the connector inventory provider queries.
func isConnectorModelsRequest(r *http.Request) bool {
	return strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/models")
}

func serveConnectorResponses(w http.ResponseWriter, r *http.Request, text string) {
	if isStreamingRequest(r) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, openResponsesRichSSE(text, 1715620000))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, openResponsesRichResource(text, 1715620000))
}

func serveConnectorChat(w http.ResponseWriter, r *http.Request, text string) {
	if isStreamingRequest(r) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chatStreamSSE(text))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, chatNonStreamResource(text))
}

// chatNonStreamResource builds a completed OpenAI chat-completions resource
// carrying text as the assistant message. The payload is a typed value encoded
// with encoding/json so no interpolated string can corrupt the wire format.
func chatNonStreamResource(text string) string {
	payload := map[string]any{
		"id":      "chatcmpl_connector_1",
		"object":  "chat.completion",
		"created": 1715620000,
		"model":   "gpt-4o-mini",
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": text},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     1,
			"completion_tokens": 1,
			"total_tokens":      2,
		},
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// chatStreamSSE builds the incremental OpenAI chat-completions SSE trajectory
// (assistant delta → finish → usage → [DONE]) carrying text. Every event payload
// is constructed as a typed value and encoded with encoding/json so no
// interpolated string can corrupt the wire format.
func chatStreamSSE(text string) string {
	var b strings.Builder
	b.WriteString(chatDataEvent(map[string]any{
		"id":      "chatcmpl_connector_stream",
		"object":  "chat.completion.chunk",
		"created": 1715620000,
		"model":   "gpt-4o-mini",
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         map[string]any{"role": "assistant", "content": text},
				"finish_reason": nil,
			},
		},
	}))
	b.WriteString(chatDataEvent(map[string]any{
		"id":      "chatcmpl_connector_stream",
		"object":  "chat.completion.chunk",
		"created": 1715620000,
		"model":   "gpt-4o-mini",
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			},
		},
	}))
	b.WriteString(chatDataEvent(map[string]any{
		"id":      "chatcmpl_connector_stream",
		"object":  "chat.completion.chunk",
		"created": 1715620000,
		"model":   "gpt-4o-mini",
		"choices": []any{},
		"usage": map[string]any{
			"prompt_tokens":     1,
			"completion_tokens": 1,
			"total_tokens":      2,
		},
	}))
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// chatDataEvent encodes data as a single OpenAI-compatible SSE data frame.
func chatDataEvent(data map[string]any) string {
	payload, _ := json.Marshal(data)
	return "data: " + string(payload) + "\n\n"
}
