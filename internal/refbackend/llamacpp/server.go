package llamacpp

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/refbackend/jsonprobe"
)

const maxBodyBytes = 10 << 20

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
}

func NewHandler(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		case r.Method == http.MethodGet && path == "/health":
			writeHealth(w)
			return
		case r.Method == http.MethodGet && path == "/v1/models":
			writeModels(w)
			return
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/chat/completions"):
			handleChatCompletions(w, r, cfg)
			return
		default:
			http.NotFound(w, r)
		}
	})
}

func writeHealth(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	writeBody(w, `{"status":"ok"}`)
}

func writeModels(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	writeBody(w, DefaultModelsJSON)
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request, cfg Config) {
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
	if stream {
		writeChatStream(w, cfg)
	} else {
		writeChatJSON(w, cfg)
	}
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

func writeChatStream(w http.ResponseWriter, cfg Config) {
	body := cfg.ChatStreamSSE
	if body == "" {
		body = DefaultChatStreamSSE
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writeBody(w, body)
}

func writeBody(w http.ResponseWriter, body string) {
	_, _ = io.WriteString(w, body)
}

const DefaultModelsJSON = `{
  "object": "list",
  "data": [
    {
      "id": "local-model",
      "object": "model",
      "created": 1715620000,
      "owned_by": "llamacpp"
    }
  ]
}`

const DefaultChatNonStreamJSON = `{
  "id": "gen-llamacpp-refbackend-1",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "local-model",
  "choices": [{"index":0,"message":{"role":"assistant","content":"llamacpp-ok"},"finish_reason":"stop"}],
  "usage": {"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}
}`

const DefaultChatStreamSSE = "data: {\"id\":\"gen-llamacpp-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"local-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llamacpp-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: [DONE]\n\n"
