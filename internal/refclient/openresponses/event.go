package openresponses

import (
	"encoding/json"
)

// Event is the independent wire model of an OpenResponses streaming event, shared
// by SSE data payloads and WebSocket frame payloads.
type Event struct {
	Type           string
	SequenceNumber int64
	Response       *ResponseResource
	Item           *Item
	Part           *ContentPart
	ItemID         string
	CallID         string
	OutputIndex    *int
	ContentIndex   *int
	Delta          string
	Text           string
	Refusal        string
	Summary        string
	Arguments      string
	Error          *ErrorObject
	Opaque         json.RawMessage
}

// IsTerminal reports whether the event is a terminal response event.
func (e *Event) IsTerminal() bool {
	switch e.Type {
	case "response.completed", "response.failed", "response.incomplete":
		return true
	}
	return false
}

// IsError reports whether the event carries an error envelope.
func (e *Event) IsError() bool { return e.Type == "error" || e.Error != nil }

// Known standard event types for which the parser validates the payload shape.
var knownEventTypes = map[string]bool{
	"response.created":                       true,
	"response.in_progress":                   true,
	"response.completed":                     true,
	"response.failed":                        true,
	"response.incomplete":                    true,
	"response.output_item.added":             true,
	"response.output_item.done":              true,
	"response.content_part.added":            true,
	"response.content_part.done":             true,
	"response.output_text.delta":             true,
	"response.output_text.done":              true,
	"response.refusal.delta":                 true,
	"response.refusal.done":                  true,
	"response.reasoning_summary_text.delta":  true,
	"response.reasoning_summary_text.done":   true,
	"response.reasoning.delta":               true,
	"response.reasoning.done":                true,
	"response.function_call_arguments.delta": true,
	"response.function_call_arguments.done":  true,
	"error":                                  true,
}

// ParseEvent decodes a single event JSON payload (SSE data or WebSocket frame).
func ParseEvent(data []byte, opts ParseOptions) (*Event, error) {
	opts = opts.normalize()
	if len(data) > opts.MaxEventBytes {
		return nil, limitf("event exceeds %d bytes", opts.MaxEventBytes)
	}
	m, err := decodeObject(data)
	if err != nil {
		return nil, err
	}
	rawType, ok := m["type"]
	if !ok {
		return nil, malformedf("event missing type discriminator")
	}
	typ, err := rawString(rawType, true)
	if err != nil {
		return nil, err
	}
	if !knownEventTypes[typ] && !isExtensionType(typ) {
		return nil, malformedf("unknown unprefixed event type %q", typ)
	}

	evt := &Event{Type: typ}
	if rawSeq, ok := m["sequence_number"]; ok && string(rawSeq) != "null" {
		if evt.SequenceNumber, err = rawInt64(rawSeq, false); err != nil {
			return nil, err
		}
	}
	evt.ItemID, err = rawString(m["item_id"], false)
	if err != nil {
		return nil, err
	}
	evt.CallID, err = rawString(m["call_id"], false)
	if err != nil {
		return nil, err
	}
	if rawIdx, ok := m["output_index"]; ok && string(rawIdx) != "null" {
		var v int
		if err = json.Unmarshal(rawIdx, &v); err != nil {
			return nil, malformedf("output_index must be integer")
		}
		evt.OutputIndex = &v
	}
	if rawIdx, ok := m["content_index"]; ok && string(rawIdx) != "null" {
		var v int
		if err = json.Unmarshal(rawIdx, &v); err != nil {
			return nil, malformedf("content_index must be integer")
		}
		evt.ContentIndex = &v
	}
	evt.Delta, err = rawString(m["delta"], false)
	if err != nil {
		return nil, err
	}
	evt.Text, err = rawString(m["text"], false)
	if err != nil {
		return nil, err
	}
	evt.Refusal, err = rawString(m["refusal"], false)
	if err != nil {
		return nil, err
	}
	evt.Summary, err = rawString(m["summary"], false)
	if err != nil {
		return nil, err
	}
	evt.Arguments, err = rawString(m["arguments"], false)
	if err != nil {
		return nil, err
	}
	if raw, ok := m["response"]; ok && string(raw) != "null" {
		res, e := ParseResponseResourceLoose(raw, opts)
		if e != nil {
			return nil, e
		}
		evt.Response = res
	}
	if raw, ok := m["item"]; ok && string(raw) != "null" {
		var it Item
		if e := json.Unmarshal(raw, &it); e != nil {
			return nil, malformedf("event item malformed: %v", e)
		}
		evt.Item = &it
	}
	if raw, ok := m["part"]; ok && string(raw) != "null" {
		var p ContentPart
		if e := json.Unmarshal(raw, &p); e != nil {
			return nil, malformedf("event part malformed: %v", e)
		}
		evt.Part = &p
	}
	if raw, ok := m["error"]; ok && string(raw) != "null" {
		var eo ErrorObject
		if e := json.Unmarshal(raw, &eo); e != nil {
			return nil, malformedf("event error malformed: %v", e)
		}
		evt.Error = &eo
	}
	if isExtensionType(typ) {
		evt.Opaque = append(json.RawMessage(nil), data...)
	}
	return evt, nil
}
