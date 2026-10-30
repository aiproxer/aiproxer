// Package openairesponses is a reference backend emulator for the OpenAI Responses API.
// It serves POST /v1/responses (or any path suffix /responses) with JSON or SSE bodies
// compatible with github.com/openai/openai-go/v3.
package openairesponses

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/refbackend/jsonprobe"
)

const maxBodyBytes = 10 << 20

// Used with [bytes.Contains] on each POST body — package-level to avoid a per-request []byte allocation.
var jsonBodyMarkerStreamTrue = []byte(`"stream":true`)

// Config tunes the emulator handler.
//
// Response precedence after route/auth succeed:
//  1. ForcedHTTPStatus (when non-zero) — Responder is not invoked
//  2. StrictToolOutputAdjacency violation — 400 missing tool output error
//  3. Responder (when non-nil) — overrides NonStreamJSON / StreamSSE / defaults
//  4. NonStreamJSON / StreamSSE fixed overrides
//  5. built-in default JSON / SSE bodies
type Config struct {
	// AllowMissingBearer, if true, skips the Authorization: Bearer check.
	AllowMissingBearer bool
	// OnAuthorizedCredential is invoked after local auth passes with the raw bearer
	// secret (Authorization without the "Bearer " prefix). Do not log this value.
	OnAuthorizedCredential func(secret string)
	// ForcedHTTPStatus, when http.StatusUnauthorized or http.StatusTooManyRequests, returns
	// that status with a provider-shaped JSON error instead of success (stream or JSON).
	ForcedHTTPStatus int
	// ForcedRetryAfter is sent as Retry-After when ForcedHTTPStatus is 429.
	ForcedRetryAfter string
	// ForcedErrorJSON overrides the forced-error JSON body; when empty a minimal default is used.
	ForcedErrorJSON string
	// OnRequestBody is invoked with the full request body after a successful route/auth
	// check and before the response is written.
	OnRequestBody func(body []byte)
	// Responder, when non-nil and ForcedHTTPStatus is zero, builds the HTTP response
	// per request. Must be safe for concurrent use.
	Responder Responder
	// NonStreamJSON overrides the JSON body for non-streaming responses. When empty, a
	// minimal completed response is returned.
	NonStreamJSON string
	// StreamSSE overrides the full SSE payload for streaming responses. When empty, a
	// minimal response.completed plus [DONE] stream is returned.
	StreamSSE string
	// StrictToolOutputAdjacency, when true, emulates strict tool-output adjacency:
	// any non-call, non-output item between a function_call and its matching
	// function_call_output returns 400. Off by default.
	StrictToolOutputAdjacency bool
}

// NewHandler returns an http.Handler that emulates POST …/responses for the official SDK.
func NewHandler(cfg Config) http.Handler {
	var seq atomic.Int64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
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
		if cfg.StrictToolOutputAdjacency {
			if callID, bad := strictAdjacencyViolation(body); bad {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, strictAdjacencyError(callID))
				return
			}
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

		stream := bytes.Contains(body, jsonBodyMarkerStreamTrue)
		if cfg.Responder != nil {
			req := Request{
				Sequence: seq.Add(1),
				Body:     append([]byte(nil), body...),
				Stream:   stream,
			}
			writeResponder(r.Context(), w, req, cfg.Responder(req))
			return
		}
		if stream {
			writeStream(r.Context(), w, cfg, body)
			return
		}
		writeJSON(r.Context(), w, cfg, body)
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

func writeResponder(ctx context.Context, w http.ResponseWriter, req Request, resp Response) {
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
		if _, err := io.WriteString(w, resp.SSE); err != nil {
			slog.ErrorContext(ctx, "refbackend openairesponses: write sse body", "error", err)
		}
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	if _, err := io.WriteString(w, resp.JSON); err != nil {
		slog.ErrorContext(ctx, "refbackend openairesponses: write json body", "error", err)
	}
}

func writeJSON(ctx context.Context, w http.ResponseWriter, cfg Config, requestBody []byte) {
	body := cfg.NonStreamJSON
	if body == "" {
		body = nonStreamWithUsageJSON
		if jsonprobe.HasJSONKey(requestBody, "tools") {
			body = nonStreamWithToolCallJSON
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, body); err != nil {
		slog.ErrorContext(ctx, "refbackend openairesponses: write json body", "error", err)
	}
}

func writeStream(ctx context.Context, w http.ResponseWriter, cfg Config, requestBody []byte) {
	body := cfg.StreamSSE
	if body == "" {
		body = streamWithUsageSSE
		if jsonprobe.HasJSONKey(requestBody, "tools") {
			body = streamWithToolCallSSE
		}
		if jsonprobe.HasJSONNumber(requestBody, "temperature", 0.11) {
			body = `event: error
data: {"type":"error","code":"rate_limit_exceeded","message":"rate limit exceeded"}

data: [DONE]

`
		}
		if jsonprobe.HasJSONNumber(requestBody, "temperature", 0.22) {
			body = `event: error
data: {"type":"error","code":"invalid_request","message":"bad request"}

data: [DONE]

`
		}
		if jsonprobe.HasJSONNumber(requestBody, "max_output_tokens", 0) {
			body = streamWithZeroUsageSSE
		}
		if jsonprobe.HasJSONNumber(requestBody, "max_output_tokens", 1) {
			body = streamWithZeroUsageSSE
		}
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, body); err != nil {
		slog.ErrorContext(ctx, "refbackend openairesponses: write sse body", "error", err)
	}
}

const defaultNonStreamJSON = `{
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

const nonStreamWithUsageJSON = `{
  "id": "resp_refbackend_1",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [{"type":"message","id":"msg_out","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],
  "usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}
}`

const nonStreamWithZeroUsageJSON = `{
  "id": "resp_refbackend_1",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [{"type":"message","id":"msg_out","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],
  "usage": {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
}`

const nonStreamWithToolCallJSON = `{
  "id": "resp_refbackend_1",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [{"type":"function_call","id":"call_1","name":"weather","arguments":"{}"}],
  "usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}
}`

const defaultStreamSSE = "event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_refbackend_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"gpt-4o-mini\",\"output\":[{\"type\":\"message\",\"id\":\"m1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"stream-ok\"}]}]}}\n\n" +
	"data: [DONE]\n\n"

const streamWithUsageSSE = "event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_refbackend_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"gpt-4o-mini\",\"output\":[{\"type\":\"message\",\"id\":\"m1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"stream-ok\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n" +
	"data: [DONE]\n\n"

const streamWithZeroUsageSSE = "event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_refbackend_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"gpt-4o-mini\",\"output\":[{\"type\":\"message\",\"id\":\"m1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"stream-ok\"}]}],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n" +
	"data: [DONE]\n\n"

const streamWithToolCallSSE = "event: response.created\n" +
	"data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_refbackend_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"in_progress\",\"model\":\"gpt-4o-mini\"}}\n\n" +
	"event: response.output_item.added\n" +
	"data: {\"type\":\"response.output_item.added\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"call_1\",\"name\":\"weather\",\"arguments\":\"{}\"}}\n\n" +
	"event: response.output_item.done\n" +
	"data: {\"type\":\"response.output_item.done\",\"sequence_number\":3,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"call_1\",\"name\":\"weather\",\"arguments\":\"{}\"}}\n\n" +
	"event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"resp_refbackend_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"gpt-4o-mini\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n" +
	"data: [DONE]\n\n"
