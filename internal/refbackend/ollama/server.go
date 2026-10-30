package ollama

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/refbackend/jsonprobe"
)

const maxBodyBytes = 10 << 20

const defaultVersion = "0.13.3"

// Config tunes the emulator handler.
type Config struct {
	Version      string
	LocalModels  []string
	CloudModels  []string
	Capabilities map[string][]string

	ResponsesSupported         bool
	ResponsesUnsupportedStatus int

	RequireBearer          bool
	OnAuthorizedCredential func(secret string)
	ForcedHTTPStatus       int
	ForcedRetryAfter       string
	ForcedErrorJSON        string
	OnRequest              func(r *http.Request, body []byte)
	OnRequestBody          func(body []byte)

	ChatNonStreamJSON      string
	ChatStreamSSE          string
	ResponsesNonStreamJSON string
	ResponsesStreamSSE     string
}

// NewHandler returns an http.Handler that emulates Ollama endpoints.
func NewHandler(cfg Config) http.Handler {
	version := cfg.Version
	if version == "" {
		version = defaultVersion
	}
	responsesUnsupportedStatus := cfg.ResponsesUnsupportedStatus
	if responsesUnsupportedStatus == 0 {
		responsesUnsupportedStatus = http.StatusNotFound
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		case r.Method == http.MethodGet && path == "/api/version":
			writeVersion(w, version)
			return
		case r.Method == http.MethodGet && path == "/v1/models":
			writeModels(w, cfg)
			return
		case r.Method == http.MethodPost && path == "/api/show":
			handleShow(w, r, cfg)
			return
		case r.Method == http.MethodPost && path == "/v1/chat/completions":
			handleChatCompletions(w, r, cfg)
			return
		case r.Method == http.MethodPost && path == "/v1/responses":
			handleResponses(w, r, cfg, responsesUnsupportedStatus)
			return
		default:
			http.NotFound(w, r)
		}
	})
}

func writeVersion(w http.ResponseWriter, version string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"version": version})
}

func writeModels(w http.ResponseWriter, cfg Config) {
	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	data := make([]modelEntry, 0, len(cfg.LocalModels)+len(cfg.CloudModels))
	for _, id := range cfg.LocalModels {
		data = append(data, modelEntry{ID: id, Object: "model", OwnedBy: "ollama"})
	}
	for _, id := range cfg.CloudModels {
		data = append(data, modelEntry{ID: id, Object: "model", OwnedBy: "ollama-cloud"})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}

func handleShow(w http.ResponseWriter, r *http.Request, cfg Config) {
	body, err := readBody(w, r)
	if err != nil {
		return
	}
	invokeRequestCallbacks(r, body, cfg)

	var req struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &req)
	modelName := req.Name
	if modelName == "" {
		modelName = req.Model
	}

	caps := cfg.Capabilities[modelName]
	if caps == nil {
		caps = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"capabilities": caps,
		"details": map[string]string{
			"format":             "gguf",
			"family":             "llama",
			"parameter_size":     "8.0B",
			"quantization_level": "Q4_K_M",
		},
		"model_info": map[string]string{
			"general.architecture": "llama",
		},
	})
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request, cfg Config) {
	body, err := readAndAuthorize(w, r, cfg)
	if err != nil {
		return
	}
	if jsonprobe.TryWriteForcedHTTPError(w, cfg.ForcedHTTPStatus, cfg.ForcedRetryAfter, cfg.ForcedErrorJSON, defaultForcedErrorJSON) {
		return
	}
	stream := bytes.Contains(body, []byte(`"stream":true`))
	if stream {
		writeChatStream(w, cfg, body)
		return
	}
	writeChatJSON(w, cfg)
}

func handleResponses(w http.ResponseWriter, r *http.Request, cfg Config, unsupportedStatus int) {
	if !cfg.ResponsesSupported {
		http.Error(w, "responses not supported", unsupportedStatus)
		return
	}
	body, err := readAndAuthorize(w, r, cfg)
	if err != nil {
		return
	}
	if jsonprobe.TryWriteForcedHTTPError(w, cfg.ForcedHTTPStatus, cfg.ForcedRetryAfter, cfg.ForcedErrorJSON, defaultForcedErrorJSON) {
		return
	}
	stream := bytes.Contains(body, []byte(`"stream":true`))
	if stream {
		writeResponsesStream(w, cfg)
		return
	}
	writeResponsesJSON(w, cfg)
}

func readAndAuthorize(w http.ResponseWriter, r *http.Request, cfg Config) ([]byte, error) {
	body, err := readBody(w, r)
	if err != nil {
		return nil, err
	}
	invokeRequestCallbacks(r, body, cfg)

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	secret := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if cfg.RequireBearer && (secret == "" || !strings.HasPrefix(auth, "Bearer ")) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		writeBody(w, `{"error":{"message":"incorrect api key","type":"invalid_request_error","code":"invalid_api_key"}}`)
		return nil, errUnauthorized
	}
	if cfg.OnAuthorizedCredential != nil && secret != "" {
		cfg.OnAuthorizedCredential(secret)
	}
	return body, nil
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return nil, err
	}
	return body, nil
}

func invokeRequestCallbacks(r *http.Request, body []byte, cfg Config) {
	if cfg.OnRequest != nil {
		cfg.OnRequest(r, body)
	}
	if cfg.OnRequestBody != nil {
		cfg.OnRequestBody(body)
	}
}

var errUnauthorized = errors.New("unauthorized")

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

const DefaultChatNonStreamJSON = `{
  "id": "chatcmpl-ollama-refbackend-1",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "llama3:latest",
  "choices": [{"index":0,"message":{"role":"assistant","content":"ollama-ok"},"finish_reason":"stop"}],
  "usage": {"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}
}`

const DefaultChatStreamSSE = "data: {\"id\":\"chatcmpl-ollama-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"llama3:latest\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ollama-stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: [DONE]\n\n"

const ChatStreamWithUsageSSE = "data: {\"id\":\"chatcmpl-ollama-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"llama3:latest\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ollama-stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-ollama-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"llama3:latest\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"chatcmpl-ollama-refbackend-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"llama3:latest\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":7,\"total_tokens\":10}}\n\n" +
	"data: [DONE]\n\n"

const DefaultResponsesNonStreamJSON = `{
  "id": "resp_ollama_refbackend_1",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "llama3:latest",
  "output": [
    {
      "type": "message",
      "id": "msg_out",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "ollama-ok"}
      ]
    }
  ],
  "usage": {"input_tokens":3,"output_tokens":7,"total_tokens":10}
}`

const DefaultResponsesStreamSSE = "event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_ollama_refbackend_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"llama3:latest\",\"output\":[{\"type\":\"message\",\"id\":\"m1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ollama-stream-ok\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":7,\"total_tokens\":10}}}\n\n" +
	"data: [DONE]\n\n"
