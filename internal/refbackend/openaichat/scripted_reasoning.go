package openaichat

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// ScriptedTurn is one deterministic assistant response for the chat emulator.
type ScriptedTurn struct {
	VisibleText string
	Reasoning   string
	ToolID      string
	ToolName    string
	ToolArgs    string
	FinishStop  bool // when true and no tool, finish_reason=stop
}

// ScriptedResponder returns a concurrency-safe Responder that serves ScriptedTurn
// bodies in order for successive authorized requests (sequence 1..N).
func ScriptedResponder(turns []ScriptedTurn) Responder {
	var mu sync.Mutex
	idx := 0
	return func(req Request) Response {
		mu.Lock()
		defer mu.Unlock()
		if idx >= len(turns) {
			return Response{
				Status: http.StatusInternalServerError,
				JSON:   `{"error":{"message":"script exhausted","type":"invalid_request_error"}}`,
			}
		}
		turn := turns[idx]
		idx++
		if req.Stream {
			return Response{Status: http.StatusOK, SSE: ScriptedTurnSSE(turn)}
		}
		return Response{Status: http.StatusOK, JSON: ScriptedTurnJSON(turn)}
	}
}

// ScriptedTurnJSON builds a minimal chat.completion body.
func ScriptedTurnJSON(turn ScriptedTurn) string {
	msg := map[string]any{
		"role":    "assistant",
		"content": turn.VisibleText,
	}
	if turn.Reasoning != "" {
		msg["reasoning_content"] = turn.Reasoning
		msg["reasoning"] = turn.Reasoning
	}
	finish := "stop"
	if turn.ToolID != "" {
		msg["tool_calls"] = []map[string]any{{
			"id":   turn.ToolID,
			"type": "function",
			"function": map[string]any{
				"name":      turn.ToolName,
				"arguments": turn.ToolArgs,
			},
		}}
		finish = "tool_calls"
	}
	out := map[string]any{
		"id":      "chatcmpl_scripted",
		"object":  "chat.completion",
		"created": 1715620000,
		"model":   "moonshot-v1-8k",
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"error":{"message":"marshal","type":"invalid_request_error"}}`
	}
	return string(b)
}

// ScriptedTurnSSE builds a minimal chat.completion.chunk SSE body ending with [DONE].
func ScriptedTurnSSE(turn ScriptedTurn) string {
	var b strings.Builder
	writeChunk := func(delta map[string]any, finish any) {
		ch := map[string]any{
			"id":      "chatcmpl_scripted_stream",
			"object":  "chat.completion.chunk",
			"created": 1715620000,
			"model":   "moonshot-v1-8k",
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
		raw, _ := json.Marshal(ch)
		b.WriteString("data: ")
		b.Write(raw)
		b.WriteString("\n\n")
	}
	writeChunk(map[string]any{"role": "assistant"}, nil)
	if turn.Reasoning != "" {
		writeChunk(map[string]any{"reasoning_content": turn.Reasoning}, nil)
	}
	if turn.VisibleText != "" {
		writeChunk(map[string]any{"content": turn.VisibleText}, nil)
	}
	if turn.ToolID != "" {
		writeChunk(map[string]any{
			"tool_calls": []map[string]any{{
				"index": 0,
				"id":    turn.ToolID,
				"type":  "function",
				"function": map[string]any{
					"name":      turn.ToolName,
					"arguments": turn.ToolArgs,
				},
			}},
		}, nil)
		writeChunk(map[string]any{}, "tool_calls")
	} else {
		writeChunk(map[string]any{}, "stop")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// SyncOracleChannel returns an OnRequestBody hook that sends body clones to ch.
// The HTTP handler must not call testing.T; consumers drain ch from the test goroutine.
// ch should be buffered for the expected turn count so the handler does not block forever.
func SyncOracleChannel(ch chan<- []byte) func([]byte) {
	return func(body []byte) {
		cloned := append([]byte(nil), body...)
		ch <- cloned
	}
}
