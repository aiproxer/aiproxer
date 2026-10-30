package openairesponses

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

type TextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type ReasoningOutputItem struct {
	Label        string
	ID           string
	Summary      []TextPart
	Content      []TextPart
	EncryptedRaw json.RawMessage
	Status       string
}

// ScriptedPart is one ordered output item. Exactly one of Reasoning, Message, or Tool is set.
type ScriptedPart struct {
	Reasoning *ReasoningOutputItem
	Message   string
	Tool      *ToolCall
}

type ScriptedTurn struct {
	ResponseID  string
	Parts       []ScriptedPart
	Reasoning   []ReasoningOutputItem
	VisibleText string
	Tool        *ToolCall
}

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

func normalizeParts(turn ScriptedTurn) []ScriptedPart {
	if len(turn.Parts) > 0 {
		return turn.Parts
	}
	parts := make([]ScriptedPart, 0, len(turn.Reasoning)+2)
	for i := range turn.Reasoning {
		r := turn.Reasoning[i]
		parts = append(parts, ScriptedPart{Reasoning: &r})
	}
	if turn.VisibleText != "" {
		parts = append(parts, ScriptedPart{Message: turn.VisibleText})
	}
	if turn.Tool != nil {
		parts = append(parts, ScriptedPart{Tool: turn.Tool})
	}
	return parts
}

func ScriptedTurnJSON(turn ScriptedTurn) string {
	rid := turn.ResponseID
	if rid == "" {
		rid = "resp_scripted"
	}
	out := buildOutputItems(turn)
	body := map[string]any{
		"id":         rid,
		"object":     "response",
		"created_at": 1715620000,
		"status":     "completed",
		"model":      "gpt-4o-mini",
		"output":     out,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return `{"error":{"message":"marshal","type":"invalid_request_error"}}`
	}
	return string(b)
}

func ScriptedTurnSSE(turn ScriptedTurn) string {
	rid := turn.ResponseID
	if rid == "" {
		rid = "resp_scripted_stream"
	}
	var b strings.Builder
	seq := 0
	next := func() int { seq++; return seq }
	write := func(event string, payload any) {
		raw, _ := json.Marshal(payload)
		if event != "" {
			b.WriteString("event: ")
			b.WriteString(event)
			b.WriteByte('\n')
		}
		b.WriteString("data: ")
		b.Write(raw)
		b.WriteString("\n\n")
	}
	created := map[string]any{
		"type": "response.created", "sequence_number": next(),
		"response": map[string]any{
			"id": rid, "object": "response", "created_at": 1715620000,
			"status": "in_progress", "model": "gpt-4o-mini", "output": []any{},
		},
	}
	write("response.created", created)
	created["type"] = "response.in_progress"
	created["sequence_number"] = next()
	write("response.in_progress", created)

	outIdx := int64(0)
	completedOut := buildOutputItems(turn)
	for _, part := range normalizeParts(turn) {
		switch {
		case part.Reasoning != nil:
			item := *part.Reasoning
			obj := reasoningItemMap(item)
			added := map[string]any{
				"id": item.ID, "type": "reasoning", "summary": []any{},
			}
			if item.Status != "" {
				added["status"] = item.Status
			}
			write("response.output_item.added", map[string]any{
				"type": "response.output_item.added", "sequence_number": next(),
				"output_index": outIdx, "item": added,
			})
			for i, sp := range item.Summary {
				write("response.reasoning_summary_part.added", map[string]any{
					"type": "response.reasoning_summary_part.added", "sequence_number": next(),
					"item_id": item.ID, "output_index": outIdx, "summary_index": i,
					"part": map[string]any{"type": "summary_text", "text": ""},
				})
				write("response.reasoning_summary_text.done", map[string]any{
					"type": "response.reasoning_summary_text.done", "sequence_number": next(),
					"item_id": item.ID, "output_index": outIdx, "summary_index": i, "text": sp.Text,
				})
				write("response.reasoning_summary_part.done", map[string]any{
					"type": "response.reasoning_summary_part.done", "sequence_number": next(),
					"item_id": item.ID, "output_index": outIdx, "summary_index": i,
					"part": map[string]any{"type": "summary_text", "text": sp.Text},
				})
			}
			for i, cp := range item.Content {
				write("response.reasoning_text.done", map[string]any{
					"type": "response.reasoning_text.done", "sequence_number": next(),
					"item_id": item.ID, "output_index": outIdx, "content_index": i, "text": cp.Text,
				})
			}
			write("response.output_item.done", map[string]any{
				"type": "response.output_item.done", "sequence_number": next(),
				"output_index": outIdx, "item": obj,
			})
			outIdx++
		case part.Message != "":
			mid := "msg_" + rid
			if outIdx > 0 {
				mid = "msg_" + rid + "_" + itoa(int(outIdx))
			}
			write("response.output_item.added", map[string]any{
				"type": "response.output_item.added", "sequence_number": next(),
				"output_index": outIdx,
				"item": map[string]any{
					"type": "message", "id": mid, "status": "in_progress", "role": "assistant", "content": []any{},
				},
			})
			write("response.output_text.done", map[string]any{
				"type": "response.output_text.done", "sequence_number": next(),
				"item_id": mid, "output_index": outIdx, "text": part.Message,
			})
			write("response.output_item.done", map[string]any{
				"type": "response.output_item.done", "sequence_number": next(),
				"output_index": outIdx,
				"item": map[string]any{
					"type": "message", "id": mid, "status": "completed", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": part.Message}},
				},
			})
			outIdx++
		case part.Tool != nil:
			itemID := "fc_" + part.Tool.ID
			write("response.output_item.added", map[string]any{
				"type": "response.output_item.added", "sequence_number": next(),
				"output_index": outIdx,
				"item": map[string]any{
					"type": "function_call", "id": itemID, "call_id": part.Tool.ID,
					"name": part.Tool.Name, "arguments": "", "status": "in_progress",
				},
			})
			write("response.function_call_arguments.done", map[string]any{
				"type": "response.function_call_arguments.done", "sequence_number": next(),
				"item_id": itemID, "output_index": outIdx, "name": part.Tool.Name, "arguments": part.Tool.Arguments,
			})
			write("response.output_item.done", map[string]any{
				"type": "response.output_item.done", "sequence_number": next(),
				"output_index": outIdx,
				"item": map[string]any{
					"type": "function_call", "id": itemID, "call_id": part.Tool.ID,
					"name": part.Tool.Name, "arguments": part.Tool.Arguments, "status": "completed",
				},
			})
			outIdx++
		}
	}
	write("response.completed", map[string]any{
		"type": "response.completed", "sequence_number": next(),
		"response": map[string]any{
			"id": rid, "object": "response", "created_at": 1715620000,
			"status": "completed", "model": "gpt-4o-mini", "output": completedOut,
		},
	})
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d [16]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[i:])
}

func buildOutputItems(turn ScriptedTurn) []any {
	parts := normalizeParts(turn)
	out := make([]any, 0, len(parts))
	rid := turn.ResponseID
	if rid == "" {
		rid = "resp_scripted"
	}
	for i, part := range parts {
		switch {
		case part.Reasoning != nil:
			out = append(out, reasoningItemMap(*part.Reasoning))
		case part.Message != "":
			mid := "msg_" + rid
			if i > 0 {
				mid = "msg_" + rid + "_" + itoa(i)
			}
			out = append(out, map[string]any{
				"type": "message", "id": mid, "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": part.Message}},
			})
		case part.Tool != nil:
			out = append(out, map[string]any{
				"type": "function_call", "id": "fc_" + part.Tool.ID, "call_id": part.Tool.ID,
				"name": part.Tool.Name, "arguments": part.Tool.Arguments, "status": "completed",
			})
		}
	}
	return out
}

func reasoningItemMap(item ReasoningOutputItem) map[string]any {
	obj := map[string]any{
		"type":    "reasoning",
		"id":      item.ID,
		"summary": summaryPartsAny(item.Summary),
	}
	if item.Content != nil {
		obj["content"] = contentPartsAny(item.Content)
	}
	if item.EncryptedRaw != nil {
		var enc any
		_ = json.Unmarshal(item.EncryptedRaw, &enc)
		obj["encrypted_content"] = enc
	}
	if item.Status != "" {
		obj["status"] = item.Status
	}
	return obj
}

func summaryPartsAny(parts []TextPart) []any {
	if parts == nil {
		return []any{}
	}
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		typ := p.Type
		if typ == "" {
			typ = "summary_text"
		}
		out = append(out, map[string]any{"type": typ, "text": p.Text})
	}
	return out
}

func contentPartsAny(parts []TextPart) []any {
	if parts == nil {
		return []any{}
	}
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		typ := p.Type
		if typ == "" {
			typ = "reasoning_text"
		}
		out = append(out, map[string]any{"type": typ, "text": p.Text})
	}
	return out
}
