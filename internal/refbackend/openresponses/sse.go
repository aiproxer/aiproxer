package openresponses

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// StreamEvent is one independent server-side lifecycle event. Fields is rendered
// as the JSON `data:` payload with `type` and `sequence_number` included.
type StreamEvent struct {
	Type   string
	Seq    int64
	Fields map[string]any
}

// buildStreamEvents derives a full lifecycle event stream from a response
// resource: response.created, per-item output lifecycle, exactly one terminal
// response.completed, with monotonically increasing sequence numbers.
func buildStreamEvents(res *Resource) []StreamEvent {
	var out []StreamEvent
	seq := int64(0)
	add := func(typ string, fields map[string]any) {
		out = append(out, StreamEvent{Type: typ, Seq: seq, Fields: fields})
		seq++
	}

	add("response.created", map[string]any{
		"response": map[string]any{
			"id": res.ID, "object": orString(res.Object, "response"),
			"created_at": res.CreatedAt, "status": "in_progress", "model": res.Model,
		},
	})

	for i := range res.Output {
		it := res.Output[i]
		inflight := it
		inflight.Status = "in_progress"

		add("response.output_item.added", map[string]any{
			"output_index": i,
			"item":         inflight,
		})

		switch it.Type {
		case ItemMessage:
			for j := range it.Content {
				part := it.Content[j]
				add("response.content_part.added", map[string]any{
					"item_id":       it.ID,
					"output_index":  i,
					"content_index": j,
					"part":          ContentPart{Type: part.Type, Annotations: rawOrEmptyObject(part.Annotations)},
				})
				if part.Type == "output_text" && part.Text != "" {
					add("response.output_text.delta", map[string]any{
						"item_id": it.ID, "output_index": i, "content_index": j, "delta": part.Text,
					})
					add("response.output_text.done", map[string]any{
						"item_id": it.ID, "output_index": i, "content_index": j, "text": part.Text,
					})
				}
				add("response.content_part.done", map[string]any{
					"item_id": it.ID, "output_index": i, "content_index": j, "part": part,
				})
			}
		case ItemFunctionCall:
			add("response.function_call_arguments.delta", map[string]any{
				"item_id": it.ID, "output_index": i, "arguments": it.Arguments,
			})
			add("response.function_call_arguments.done", map[string]any{
				"item_id": it.ID, "output_index": i, "arguments": it.Arguments,
			})
		case ItemReasoning:
			if it.Reasoning != nil {
				for j := range it.Reasoning.Summary {
					part := it.Reasoning.Summary[j]
					add("response.reasoning_summary_text.delta", map[string]any{
						"item_id": it.ID, "output_index": i, "content_index": j, "summary": part.Text,
					})
					add("response.reasoning_summary_text.done", map[string]any{
						"item_id": it.ID, "output_index": i, "content_index": j, "summary": part.Text,
					})
				}
				for j := range it.Reasoning.Content {
					part := it.Reasoning.Content[j]
					add("response.reasoning.delta", map[string]any{
						"item_id": it.ID, "output_index": i, "content_index": j, "delta": part.Text,
					})
					add("response.reasoning.done", map[string]any{
						"item_id": it.ID, "output_index": i, "content_index": j, "text": part.Text,
					})
				}
			}
		}

		done := it
		done.Status = "completed"
		add("response.output_item.done", map[string]any{
			"output_index": i,
			"item":         done,
		})
	}

	add("response.completed", map[string]any{
		"response": res,
	})
	return out
}

// renderPayload renders the event's data payload (type + sequence + fields).
// Reserved discriminator keys in Fields are ignored so callers cannot corrupt
// the emitted `type`/`sequence_number`.
func (ev StreamEvent) renderPayload() ([]byte, error) {
	m := map[string]any{"type": ev.Type, "sequence_number": ev.Seq}
	for k, v := range ev.Fields {
		if k == "type" || k == "sequence_number" {
			continue
		}
		m[k] = v
	}
	return json.Marshal(m)
}

// sseWriter writes `event:`/`data:` frames with an explicit event header.
type sseWriter struct {
	w io.Writer
}

func (sw *sseWriter) writeEvent(ev StreamEvent, header string) error {
	payload, err := ev.renderPayload()
	if err != nil {
		return err
	}
	if header == "" {
		header = ev.Type
	}
	// renderPayload JSON-escapes control characters, so a literal newline here
	// would indicate a malformed field value that must not reach the wire.
	if !safeSSEType(header) || strings.ContainsAny(string(payload), "\n\r") {
		return fmt.Errorf("refbackend/openresponses: unsafe sse event framing")
	}
	if _, err := fmt.Fprintf(sw.w, "event: %s\ndata: %s\n\n", header, payload); err != nil {
		return err
	}
	return nil
}

func (sw *sseWriter) writeDone() error {
	if _, err := io.WriteString(sw.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	return nil
}

func safeSSEType(s string) bool {
	if s == "" || strings.ContainsAny(s, "\n\r") {
		return false
	}
	return true
}
