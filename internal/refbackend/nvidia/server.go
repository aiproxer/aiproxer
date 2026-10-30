package nvidia

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

	ChatNonStreamJSON string
	ChatStreamSSE     string

	ResponsesNonStreamJSON string
	ResponsesStreamSSE     string
}

// NewHandler returns an http.Handler that emulates NVIDIA NIM endpoints:
// POST /v1/chat/completions and POST /v1/responses.
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
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				writeBody(w, `{"error":{"message":"incorrect api key","type":"invalid_request_error","code":"invalid_api_key"}}`)
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
	writeBody(w, body)
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
	writeBody(w, body)
}

func writeResponsesJSON(w http.ResponseWriter, cfg Config) {
	body := cfg.ResponsesNonStreamJSON
	if body == "" {
		body = DefaultResponsesNonStreamJSON
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	writeBody(w, body)
}

func writeResponsesStream(w http.ResponseWriter, cfg Config) {
	body := cfg.ResponsesStreamSSE
	if body == "" {
		body = DefaultResponsesStreamSSE
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writeBody(w, body)
}

func writeBody(w http.ResponseWriter, body string) {
	_, _ = io.WriteString(w, body)
}

// DefaultChatNonStreamJSON is a minimal Chat Completions response for NVIDIA NIM.
const DefaultChatNonStreamJSON = `{
  "id": "gen-nvidia-refbackend-1",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "nvidia/llama-3.1-nemotron-nano-8b-v1",
  "choices": [{"index":0,"message":{"role":"assistant","content":"nvidia-ok"},"finish_reason":"stop"}],
  "usage": {"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}
}`

// DefaultChatStreamSSE is a minimal streaming Chat Completions response.
const DefaultChatStreamSSE = "data: {\"id\":\"gen-nvidia-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"nvidia/llama-3.1-nemotron-nano-8b-v1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"nvidia-stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: [DONE]\n\n"

// ChatStreamWithUsageSSE includes a final usage chunk before [DONE].
const ChatStreamWithUsageSSE = "data: {\"id\":\"gen-nvidia-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"nvidia/llama-3.1-nemotron-nano-8b-v1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"nvidia-stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"gen-nvidia-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"nvidia/llama-3.1-nemotron-nano-8b-v1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"gen-nvidia-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"nvidia/llama-3.1-nemotron-nano-8b-v1\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":7,\"total_tokens\":10}}\n\n" +
	"data: [DONE]\n\n"

// DefaultResponsesNonStreamJSON is a minimal Responses API completed response.
const DefaultResponsesNonStreamJSON = `{
  "id": "resp_nvidia_refbackend_1",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "nvidia/llama-3.1-nemotron-nano-8b-v1",
  "output": [
    {
      "type": "message",
      "id": "msg_out",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "nvidia-ok"}
      ]
    }
  ],
  "usage": {"input_tokens":3,"output_tokens":7,"total_tokens":10}
}`

// DefaultResponsesStreamSSE is a minimal Responses SSE stream.
const DefaultResponsesStreamSSE = "event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_nvidia_refbackend_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"nvidia/llama-3.1-nemotron-nano-8b-v1\",\"output\":[{\"type\":\"message\",\"id\":\"m1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"nvidia-stream-ok\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":7,\"total_tokens\":10}}}\n\n" +
	"data: [DONE]\n\n"
