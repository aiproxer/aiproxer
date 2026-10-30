package anthropicmessages

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// ThinkingTurn is one deterministic Anthropic assistant message for E2E scripting.
type ThinkingTurn struct {
	VisibleText     string
	Thinking        string
	Signature       string
	RedactedData    string // when set, emits redacted_thinking with this data
	IncludeRedacted bool
	ToolID          string
	ToolName        string
	ToolInputJSON   string
}

// ScriptedThinkingResponder serves ThinkingTurn bodies in order.
func ScriptedThinkingResponder(turns []ThinkingTurn) Responder {
	var mu sync.Mutex
	idx := 0
	return func(req Request) Response {
		mu.Lock()
		defer mu.Unlock()
		if idx >= len(turns) {
			return Response{
				Status: http.StatusInternalServerError,
				JSON:   `{"type":"error","error":{"type":"api_error","message":"script exhausted"}}`,
			}
		}
		turn := turns[idx]
		idx++
		if req.Stream {
			return Response{Status: http.StatusOK, SSE: ThinkingTurnSSE(turn)}
		}
		return Response{Status: http.StatusOK, JSON: ThinkingTurnJSON(turn)}
	}
}

// ThinkingTurnJSON builds a completed Messages body with thinking / redacted_thinking.
func ThinkingTurnJSON(turn ThinkingTurn) string {
	content := make([]map[string]any, 0, 4)
	if turn.Thinking != "" {
		content = append(content, map[string]any{
			"type":      "thinking",
			"thinking":  turn.Thinking,
			"signature": turn.Signature,
		})
	}
	if turn.IncludeRedacted || turn.RedactedData != "" {
		content = append(content, map[string]any{
			"type": "redacted_thinking",
			"data": turn.RedactedData,
		})
	}
	if turn.VisibleText != "" {
		content = append(content, map[string]any{"type": "text", "text": turn.VisibleText})
	}
	if turn.ToolID != "" {
		var input any = map[string]any{}
		if strings.TrimSpace(turn.ToolInputJSON) != "" {
			_ = json.Unmarshal([]byte(turn.ToolInputJSON), &input)
		}
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    turn.ToolID,
			"name":  turn.ToolName,
			"input": input,
		})
	}
	stop := "end_turn"
	if turn.ToolID != "" {
		stop = "tool_use"
	}
	out := map[string]any{
		"id":            "msg_scripted",
		"type":          "message",
		"role":          "assistant",
		"model":         "claude-3-5-haiku-20241022",
		"content":       content,
		"stop_reason":   stop,
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 1, "output_tokens": 1},
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"type":"error","error":{"type":"api_error","message":"marshal"}}`
	}
	return string(b)
}

// ThinkingTurnSSE builds a minimal SSE stream with thinking deltas and optional text/tool.
func ThinkingTurnSSE(turn ThinkingTurn) string {
	var b strings.Builder
	write := func(event string, payload any) {
		raw, _ := json.Marshal(payload)
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteString("\ndata: ")
		b.Write(raw)
		b.WriteString("\n\n")
	}
	write("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_scripted_stream", "type": "message", "role": "assistant",
			"model": "claude-3-5-haiku-20241022", "content": []any{},
			"stop_reason": "", "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 0},
		},
	})
	idx := 0
	if turn.Thinking != "" {
		write("content_block_start", map[string]any{
			"type": "content_block_start", "index": idx,
			"content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""},
		})
		write("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": idx,
			"delta": map[string]any{"type": "thinking_delta", "thinking": turn.Thinking},
		})
		if turn.Signature != "" {
			write("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]any{"type": "signature_delta", "signature": turn.Signature},
			})
		}
		write("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		idx++
	}
	if turn.IncludeRedacted || turn.RedactedData != "" {
		write("content_block_start", map[string]any{
			"type": "content_block_start", "index": idx,
			"content_block": map[string]any{"type": "redacted_thinking", "data": turn.RedactedData},
		})
		write("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		idx++
	}
	if turn.VisibleText != "" {
		write("content_block_start", map[string]any{
			"type": "content_block_start", "index": idx,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		write("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": idx,
			"delta": map[string]any{"type": "text_delta", "text": turn.VisibleText},
		})
		write("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		idx++
	}
	if turn.ToolID != "" {
		input := turn.ToolInputJSON
		if input == "" {
			input = "{}"
		}
		write("content_block_start", map[string]any{
			"type": "content_block_start", "index": idx,
			"content_block": map[string]any{
				"type": "tool_use", "id": turn.ToolID, "name": turn.ToolName, "input": map[string]any{},
			},
		})
		write("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": idx,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": input},
		})
		write("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
	}
	stop := "end_turn"
	if turn.ToolID != "" {
		stop = "tool_use"
	}
	write("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": 1},
	})
	write("message_stop", map[string]any{"type": "message_stop"})
	return b.String()
}
