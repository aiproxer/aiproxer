// Package gemini is a reference backend emulator for the Google Gemini generateContent API.
// It serves POST …:generateContent and …:streamGenerateContent?alt=sse with JSON or SSE
// bodies compatible with google.golang.org/genai (Google AI / ML dev backend).
package gemini

import (
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/refbackend/jsonprobe"
)

const maxBodyBytes = 10 << 20

// Config tunes the emulator handler.
type Config struct {
	// AllowMissingAPIKey, if true, skips the x-goog-api-key header check.
	AllowMissingAPIKey bool
	// OnAuthorizedCredential is invoked after local auth passes with the raw x-goog-api-key value.
	// Do not log this value.
	OnAuthorizedCredential func(secret string)
	// ForcedHTTPStatus, when 401 or 429, returns that status with provider-shaped JSON instead of success.
	ForcedHTTPStatus int
	// ForcedRetryAfter is sent as Retry-After when ForcedHTTPStatus is 429.
	ForcedRetryAfter string
	// ForcedErrorJSON overrides the forced-error JSON body; when empty a minimal default is used.
	ForcedErrorJSON string
	// OnRequestBody is invoked with the full request body after a successful route/auth
	// check and before the response is written.
	OnRequestBody func(body []byte)
	// NonStreamJSON overrides the JSON body for non-streaming responses. When empty, a
	// minimal completed candidate is returned.
	NonStreamJSON string
	// StreamSSE overrides the full SSE payload for streaming responses. When empty, a
	// minimal single data chunk is returned (double-newline terminated for the genai client).
	StreamSSE string
}

// NewHandler returns an http.Handler that emulates generateContent / streamGenerateContent
// for the official genai SDK (API key backend).
func NewHandler(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		path := r.URL.Path
		switch {
		case strings.Contains(path, "streamGenerateContent"):
			body, ok := routeAuthAndBody(w, r, cfg)
			if !ok {
				return
			}
			writeStream(w, cfg, body)
		case strings.Contains(path, ":generateContent"):
			body, ok := routeAuthAndBody(w, r, cfg)
			if !ok {
				return
			}
			writeJSON(w, cfg, body)
		default:
			http.NotFound(w, r)
		}
	})
}

func routeAuthAndBody(w http.ResponseWriter, r *http.Request, cfg Config) ([]byte, bool) {
	if !cfg.AllowMissingAPIKey {
		if r.Header.Get("x-goog-api-key") == "" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return nil, false
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return nil, false
	}
	if cfg.OnRequestBody != nil {
		cfg.OnRequestBody(body)
	}
	if jsonprobe.HasJSONNumber(body, "temperature", 0.11) {
		http.Error(w, `{"error":{"code":429,"message":"rate limit exceeded"}}`, http.StatusTooManyRequests)
		return nil, false
	}
	if jsonprobe.HasJSONNumber(body, "temperature", 0.22) {
		http.Error(w, `{"error":{"code":400,"message":"bad request"}}`, http.StatusBadRequest)
		return nil, false
	}
	key := strings.TrimSpace(r.Header.Get("x-goog-api-key"))
	if cfg.OnAuthorizedCredential != nil {
		cfg.OnAuthorizedCredential(key)
	}
	if jsonprobe.TryWriteForcedHTTPError(w, cfg.ForcedHTTPStatus, cfg.ForcedRetryAfter, cfg.ForcedErrorJSON, defaultForcedErrorJSON) {
		return nil, false
	}
	return body, true
}

func defaultForcedErrorJSON(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return `{"error":{"code":401,"message":"API key not valid","status":"INVALID_ARGUMENT"}}`
	case http.StatusTooManyRequests:
		return `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`
	default:
		return `{"error":{"code":400,"message":"error","status":"INVALID_ARGUMENT"}}`
	}
}

func writeJSON(w http.ResponseWriter, cfg Config, requestBody []byte) {
	body := cfg.NonStreamJSON
	if body == "" {
		body = nonStreamWithUsageJSON
		if jsonprobe.HasJSONKey(requestBody, "tools") {
			body = nonStreamWithToolCallJSON
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func writeStream(w http.ResponseWriter, cfg Config, requestBody []byte) {
	body := cfg.StreamSSE
	if body == "" {
		body = streamWithUsageSSE
		if jsonprobe.HasJSONKey(requestBody, "tools") {
			body = streamWithToolCallSSE
		}
		if jsonprobe.HasJSONNumber(requestBody, "maxOutputTokens", 0) || jsonprobe.HasJSONNumber(requestBody, "maxOutputTokens", 1) {
			body = streamWithZeroUsageSSE
		}
		if jsonprobe.HasJSONNumber(requestBody, "temperature", 0.11) || jsonprobe.HasJSONNumber(requestBody, "temperature", 0.22) {
			body = `data: {"error":{"code":429,"message":"provider error"}}

`
		}
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

const defaultNonStreamJSON = `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [{"text": "ok"}]
      }
    }
  ]
}`

const nonStreamWithUsageJSON = `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [{"text": "ok"}]
      }
    }
  ],
  "usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}
}`

const nonStreamWithZeroUsageJSON = `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [{"text": "ok"}]
      }
    }
  ],
  "usageMetadata": {"promptTokenCount": 0, "candidatesTokenCount": 0, "totalTokenCount": 0}
}`

const nonStreamWithToolCallJSON = `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [{"functionCall": {"name": "weather", "args": {}, "id": "call_1"}}]
      },
      "finishReason": "STOP"
    }
  ],
  "usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}
}`

const defaultStreamSSE = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"stream-ok\"}]}}]}\n\n"

const streamWithUsageSSE = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"stream-ok\"}]}}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15}}\n\n"

const streamWithZeroUsageSSE = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"stream-ok\"}]}}],\"usageMetadata\":{\"promptTokenCount\":0,\"candidatesTokenCount\":0,\"totalTokenCount\":0}}\n\n"

const streamWithToolCallSSE = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"weather\",\"args\":{},\"id\":\"call_1\"}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15}}\n\n"
