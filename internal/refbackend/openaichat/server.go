// Package openaichat is a reference backend emulator for the OpenAI Chat Completions API.
// It serves POST …/chat/completions with JSON or SSE bodies compatible with
// github.com/openai/openai-go/v3.
package openaichat

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/refbackend/jsonprobe"
)

const maxBodyBytes = 10 << 20

// Config tunes the emulator handler.
//
// Response precedence after route/auth succeed:
//  1. ForcedHTTPStatus (when non-zero) — Responder is not invoked
//  2. StrictReasoningContent violation — 400 DeepSeek thinking-mode error
//  3. Responder (when non-nil) — overrides NonStreamJSON / StreamSSE / defaults
//  4. NonStreamJSON / StreamSSE fixed overrides
//  5. built-in default JSON / SSE bodies
type Config struct {
	// AllowMissingBearer, if true, skips the Authorization: Bearer check.
	AllowMissingBearer bool
	// OnAuthorizedCredential is invoked after local auth passes with the raw bearer
	// secret (Authorization without the "Bearer " prefix). Do not log this value.
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
	// Responder, when non-nil and ForcedHTTPStatus is zero, builds the HTTP response
	// per request. Must be safe for concurrent use. See Request / Response.
	Responder Responder
	// NonStreamJSON overrides the JSON body for non-streaming responses. When empty, a
	// minimal chat.completion is returned.
	NonStreamJSON string
	// StreamSSE overrides the full SSE payload for streaming responses. When empty, a
	// minimal chat.completion.chunk stream ending with [DONE] is returned.
	StreamSSE string
	// StrictReasoningContent, when true, emulates DeepSeek thinking-mode strictness:
	// any assistant message with tool_calls that lacks the reasoning_content field
	// returns 400. Off by default. Empty string counts as present.
	StrictReasoningContent bool
}

// NewHandler returns an http.Handler that emulates POST …/chat/completions for the official SDK.
func NewHandler(cfg Config) http.Handler {
	var seq atomic.Int64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
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
		// Oracle/validate on a clone of the post-transform body before any response path.
		// Responder receives an independent clone so validators cannot mutate scripted input.
		if cfg.OnRequestBody != nil {
			cfg.OnRequestBody(append([]byte(nil), body...))
		}

		secret := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
		secret = strings.TrimSpace(secret)
		if cfg.OnAuthorizedCredential != nil {
			cfg.OnAuthorizedCredential(secret)
		}
		if jsonprobe.TryWriteForcedHTTPError(w, cfg.ForcedHTTPStatus, cfg.ForcedRetryAfter, cfg.ForcedErrorJSON, defaultForcedErrorJSON) {
			return
		}
		if cfg.StrictReasoningContent && strictReasoningContentViolation(body) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(strictReasoningContentError))
			return
		}
		if jsonprobe.HasJSONNumber(body, "temperature", 0.11) {
			_ = jsonprobe.TryWriteForcedHTTPError(w, http.StatusTooManyRequests, "60", "", defaultForcedErrorJSON)
			return
		}
		if jsonprobe.HasJSONNumber(body, "temperature", 0.22) {
			_ = jsonprobe.TryWriteForcedHTTPError(w, http.StatusBadRequest, "", "", defaultForcedErrorJSON)
			return
		}
		if jsonprobe.HasJSONNumber(body, "temperature", 0.33) {
			_ = jsonprobe.TryWriteForcedHTTPError(w, http.StatusTooManyRequests, "60", "", defaultForcedErrorJSON)
			return
		}
		if jsonprobe.HasJSONNumber(body, "temperature", 0.44) {
			_ = jsonprobe.TryWriteForcedHTTPError(w, http.StatusTooManyRequests, "60", "", defaultForcedErrorJSON)
			return
		}

		stream := bytes.Contains(body, []byte(`"stream":true`))
		if cfg.Responder != nil {
			cloned := append([]byte(nil), body...)
			req := Request{
				Sequence: seq.Add(1),
				Body:     cloned,
				Stream:   stream,
			}
			writeResponder(w, req, cfg.Responder(req))
			return
		}
		if stream {
			writeStream(w, cfg, body)
			return
		}
		writeJSON(w, cfg, body)
	})
}

func writeResponder(w http.ResponseWriter, req Request, resp Response) {
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	if resp.Headers != nil {
		for k, vals := range resp.Headers.Clone() {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
	}
	if req.Stream {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp.SSE))
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(resp.JSON))
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
	if cfg.StreamSSE != "" {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(cfg.StreamSSE))
		return
	}
	body := streamWithUsageSSE
	if jsonprobe.HasJSONKey(requestBody, "tools") {
		body = streamWithToolCallSSE
	}
	if !jsonprobe.HasJSONKey(requestBody, "stream_options") && jsonprobe.HasJSONKey(requestBody, "tools") {
		body = streamWithToolCallSSE
	}
	if jsonprobe.HasJSONNumber(requestBody, "temperature", 0.11) {
		body = `data: {"id":"err","object":"chat.completion.chunk","choices":[],"finish_reason":null,"error":{"message":"rate limit exceeded"}}

data: [DONE]

`
	}
	if jsonprobe.HasJSONNumber(requestBody, "temperature", 0.22) {
		body = `data: {"id":"err","object":"chat.completion.chunk","choices":[],"finish_reason":null,"error":{"message":"bad request"}}

data: [DONE]

`
	}
	if jsonprobe.HasJSONNumber(requestBody, "max_tokens", 0) || jsonprobe.HasJSONNumber(requestBody, "max_tokens", 1) || jsonprobe.HasJSONNumber(requestBody, "max_output_tokens", 0) || jsonprobe.HasJSONNumber(requestBody, "max_output_tokens", 1) {
		body = streamWithZeroUsageSSE
	}
	if jsonprobe.HasJSONNumber(requestBody, "temperature", 0.11) {
		body = streamWithErrorSSE
	}
	if jsonprobe.HasJSONNumber(requestBody, "temperature", 0.22) {
		body = streamWithErrorSSE
	}
	if jsonprobe.HasJSONBool(requestBody, "include_usage", true) && !jsonprobe.HasJSONNumber(requestBody, "max_tokens", 1) && !jsonprobe.HasJSONKey(requestBody, "tools") {
		body = streamWithUsageSSE
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

const defaultNonStreamJSON = `{
  "id": "chatcmpl_refbackend_1",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
}`

const nonStreamWithUsageJSON = `{
  "id": "chatcmpl_refbackend_1",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
  "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
}`

const nonStreamWithZeroUsageJSON = `{
  "id": "chatcmpl_refbackend_1",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
  "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
}`

const nonStreamWithToolCallJSON = `{
  "id": "chatcmpl_refbackend_1",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
  "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
}`

const defaultStreamSSE = "data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: [DONE]\n\n"

const streamWithZeroUsageSSE = "data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n" +
	"data: [DONE]\n\n"

const streamWithToolCallSSE = "data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
	"data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n" +
	"data: [DONE]\n\n"

const streamWithErrorSSE = "data: {\"id\":\"err\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"finish_reason\":null,\"error\":{\"message\":\"provider error\"}}\n\ndata: [DONE]\n\n"

// streamWithUsageSSE is returned when the client sets stream_options.include_usage,
// matching OpenAI's final usage chunk before [DONE].
const streamWithUsageSSE = "data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":7,\"total_tokens\":10}}\n\n" +
	"data: [DONE]\n\n"
