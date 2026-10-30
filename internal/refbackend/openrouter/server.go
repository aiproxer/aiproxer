package openrouter

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/refbackend/jsonprobe"
)

const maxBodyBytes = 10 << 20

// Config tunes the emulator handler.
type Config struct {
	AllowMissingBearer     bool
	OnAuthorizedCredential func(secret string)
	ForcedHTTPStatus       int
	ForcedRetryAfter       string
	ForcedErrorJSON        string
	OnRequestBody          func(body []byte)
	OnRequestHeaders       func(h http.Header)

	// Chat completions overrides.
	ChatNonStreamJSON string
	ChatStreamSSE     string

	// Responses overrides.
	ResponsesNonStreamJSON string
	ResponsesStreamSSE     string
}

// NewHandler returns an http.Handler that emulates both OpenRouter endpoints:
// POST /api/v1/chat/completions and POST /api/v1/responses.
func NewHandler(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}

		path := r.URL.Path
		isChat := strings.HasSuffix(path, "/chat/completions")
		isResponses := strings.HasSuffix(path, "/responses") && !strings.HasSuffix(path, "/chat/completions")

		if !isChat && !isResponses {
			http.NotFound(w, r)
			return
		}

		if !cfg.AllowMissingBearer {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
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
		if cfg.OnRequestHeaders != nil {
			cfg.OnRequestHeaders(r.Header)
		}

		secret := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
		secret = strings.TrimSpace(secret)
		if cfg.OnAuthorizedCredential != nil {
			cfg.OnAuthorizedCredential(secret)
		}

		if jsonprobe.TryWriteForcedHTTPError(w, cfg.ForcedHTTPStatus, cfg.ForcedRetryAfter, cfg.ForcedErrorJSON, defaultForcedErrorJSON) {
			return
		}

		stream := bytes.Contains(body, []byte(`"stream":true`))
		if isChat {
			if stream {
				writeChatStream(w, cfg, body)
			} else {
				writeChatJSON(w, cfg)
			}
			return
		}
		// isResponses
		if stream {
			writeResponsesStream(w, cfg)
		} else {
			writeResponsesJSON(w, cfg)
		}
	})
}

func defaultForcedErrorJSON(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return `{"error":{"message":"incorrect api key","type":"invalid_request_error","code":"invalid_api_key"}}`
	case http.StatusTooManyRequests:
		return `{"error":{"message":"rate limit exceeded","type":"requests","code":"rate_limit_exceeded"}}`
	default:
		return `{"error":{"message":"error","type":"invalid_request_error"}}`
	}
}

func writeChatJSON(w http.ResponseWriter, cfg Config) {
	body := cfg.ChatNonStreamJSON
	if body == "" {
		body = DefaultChatNonStreamJSON
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if !writeBody(w, body) {
		return
	}
}

func writeChatStream(w http.ResponseWriter, cfg Config, requestBody []byte) {
	body := cfg.ChatStreamSSE
	if body == "" {
		if bytes.Contains(requestBody, []byte(`"include_usage":true`)) {
			body = ChatStreamWithUsageSSE
		} else {
			body = DefaultChatStreamSSE
		}
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if !writeBody(w, body) {
		return
	}
}

func writeResponsesJSON(w http.ResponseWriter, cfg Config) {
	body := cfg.ResponsesNonStreamJSON
	if body == "" {
		body = DefaultResponsesNonStreamJSON
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if !writeBody(w, body) {
		return
	}
}

func writeResponsesStream(w http.ResponseWriter, cfg Config) {
	body := cfg.ResponsesStreamSSE
	if body == "" {
		body = DefaultResponsesStreamSSE
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if !writeBody(w, body) {
		return
	}
}

func writeBody(w http.ResponseWriter, body string) bool {
	_, err := io.WriteString(w, body)
	return err == nil
}

// DefaultChatNonStreamJSON is a minimal Chat Completions response with OpenRouter extensions.
const DefaultChatNonStreamJSON = `{
  "id": "gen-or-refbackend-1",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "openai/gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"or-ok"},"finish_reason":"stop","native_finish_reason":"stop"}],
  "usage": {"prompt_tokens":3,"completion_tokens":7,"total_tokens":10,"cost":0.00014}
}`

// DefaultChatStreamSSE is a minimal streaming Chat Completions response.
const DefaultChatStreamSSE = "data: {\"id\":\"gen-or-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"openai/gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"or-stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: [DONE]\n\n"

// ChatStreamWithUsageSSE includes a final usage chunk before [DONE].
const ChatStreamWithUsageSSE = "data: {\"id\":\"gen-or-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"openai/gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"or-stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"gen-or-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"openai/gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"gen-or-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"openai/gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":7,\"total_tokens\":10,\"cost\":0.00014}}\n\n" +
	"data: [DONE]\n\n"

// DefaultResponsesNonStreamJSON is a minimal Responses API completed response.
const DefaultResponsesNonStreamJSON = `{
  "id": "resp_or_refbackend_1",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "openai/gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_out",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "or-ok"}
      ]
    }
  ],
  "usage": {"input_tokens":3,"output_tokens":7,"total_tokens":10}
}`

// DefaultResponsesStreamSSE is a minimal Responses SSE stream.
const DefaultResponsesStreamSSE = "event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_or_refbackend_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"openai/gpt-4o-mini\",\"output\":[{\"type\":\"message\",\"id\":\"m1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"or-stream-ok\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":7,\"total_tokens\":10}}}\n\n" +
	"data: [DONE]\n\n"
