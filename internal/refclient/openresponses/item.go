package openresponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func knownItemType(t string) bool {
	switch t {
	case ItemMessage, ItemFunctionCall, ItemFunctionCallOutput, ItemReasoning, ItemItemReference, ItemCompaction:
		return true
	}
	return false
}

func isExtensionType(t string) bool {
	return strings.ContainsRune(t, ':')
}

// UnmarshalJSON parses a discriminated wire item. Known portable types are typed;
// unknown valid prefixed types are preserved opaquely; unknown unprefixed types fail.
func (it *Item) UnmarshalJSON(data []byte) error {
	m, err := decodeObject(data)
	if err != nil {
		return err
	}
	rawType, ok := m["type"]
	if !ok {
		return malformedf("item missing type discriminator")
	}
	typ, err := rawString(rawType, true)
	if err != nil {
		return err
	}
	if typ == "" {
		return malformedf("item type cannot be empty")
	}

	if !knownItemType(typ) {
		if !isExtensionType(typ) {
			return malformedf("unknown unprefixed item type %q", typ)
		}
		// Preserve the entire object opaquely.
		it.Type = typ
		it.ID, _ = rawString(m["id"], false)
		it.Status, _ = rawString(m["status"], false)
		it.Opaque = append(json.RawMessage(nil), data...)
		return nil
	}

	it.Type = typ
	it.ID, err = rawString(m["id"], false)
	if err != nil {
		return err
	}
	it.Status, err = rawString(m["status"], false)
	if err != nil {
		return err
	}
	it.Role, err = rawString(m["role"], false)
	if err != nil {
		return err
	}
	it.Phase, err = rawString(m["phase"], false)
	if err != nil {
		return err
	}
	it.CallID, err = rawString(m["call_id"], false)
	if err != nil {
		return err
	}
	it.Name, err = rawString(m["name"], false)
	if err != nil {
		return err
	}
	it.EncapsulatedID, err = rawString(m["encapsulated_id"], false)
	if err != nil {
		return err
	}
	it.EncryptedContent, err = rawString(m["encrypted_content"], false)
	if err != nil {
		return err
	}
	if rawArgs, ok := m["arguments"]; ok && string(rawArgs) != "null" {
		it.Arguments, err = rawString(rawArgs, false)
		if err != nil {
			return err
		}
	}
	if rawOut, ok := m["output"]; ok && string(rawOut) != "null" {
		it.Output = append(json.RawMessage(nil), rawOut...)
	}
	if typ == ItemReasoning {
		// Reasoning items carry summary/content/encrypted_content at the item
		// level per the profile, and may nest them under a `reasoning` object.
		r := &ReasoningItem{}
		if rawReasoning, ok := m["reasoning"]; ok && string(rawReasoning) != "null" {
			if err := json.Unmarshal(rawReasoning, r); err != nil {
				return err
			}
		}
		if rawContent, ok := m["content"]; ok && string(rawContent) != "null" {
			parts, err := parseContentParts(rawContent)
			if err != nil {
				return err
			}
			r.Content = parts
		}
		if rawSummary, ok := m["summary"]; ok && string(rawSummary) != "null" {
			parts, err := parseContentParts(rawSummary)
			if err != nil {
				return err
			}
			r.Summary = parts
		}
		if rawEncrypted, ok := m["encrypted_content"]; ok {
			var ec string
			if err := json.Unmarshal(rawEncrypted, &ec); err != nil {
				return malformedf("encrypted_content must be a string or null")
			}
			r.EncryptedContent = ec
			r.EncryptedContentSet = true
		}
		it.Reasoning = r
		return nil
	}
	if rawContent, ok := m["content"]; ok && string(rawContent) != "null" {
		parts, err := parseContentParts(rawContent)
		if err != nil {
			return err
		}
		it.Content = parts
	}
	if rawReasoning, ok := m["reasoning"]; ok && string(rawReasoning) != "null" {
		var r ReasoningItem
		if err := json.Unmarshal(rawReasoning, &r); err != nil {
			return err
		}
		it.Reasoning = &r
	}
	return nil
}

// MarshalJSON renders the item back onto the wire, preserving opaque extension bytes.
func (it Item) MarshalJSON() ([]byte, error) {
	if it.IsExtension() {
		if len(it.Opaque) > 0 {
			return it.Opaque, nil
		}
		m := map[string]any{"type": it.Type, "id": it.ID, "status": it.Status}
		if it.Opaque != nil {
			m["opaque"] = it.Opaque
		}
		return json.Marshal(m)
	}
	m := map[string]any{"type": it.Type}
	if it.ID != "" {
		m["id"] = it.ID
	}
	if it.Status != "" {
		m["status"] = it.Status
	}
	if it.Role != "" {
		m["role"] = it.Role
	}
	if it.Phase != "" {
		m["phase"] = it.Phase
	}
	if len(it.Content) > 0 {
		m["content"] = it.Content
	}
	if it.CallID != "" {
		m["call_id"] = it.CallID
	}
	if it.Name != "" {
		m["name"] = it.Name
	}
	if it.Arguments != "" {
		m["arguments"] = it.Arguments
	}
	if it.Output != nil {
		m["output"] = it.Output
	}
	if it.Reasoning != nil {
		m["reasoning"] = it.Reasoning
	}
	if it.EncapsulatedID != "" {
		m["encapsulated_id"] = it.EncapsulatedID
	}
	if it.EncryptedContent != "" {
		m["encrypted_content"] = it.EncryptedContent
	}
	return json.Marshal(m)
}

// NewMessageItem builds a portable message item with one content part.
func NewMessageItem(role, partType, text string) Item {
	return Item{
		Type: string(ItemMessage),
		Role: role,
		Content: []ContentPart{{
			Type: partType,
			Text: text,
		}},
	}
}

// NewFunctionCallItem builds a function_call output item.
func NewFunctionCallItem(id, callID, name, arguments string) Item {
	return Item{
		Type:      string(ItemFunctionCall),
		ID:        id,
		CallID:    callID,
		Name:      name,
		Arguments: arguments,
	}
}

// NewFunctionCallOutputItem builds a function_call_output item from a call id.
func NewFunctionCallOutputItem(callID, output string) Item {
	return Item{
		Type:   string(ItemFunctionCallOutput),
		CallID: callID,
		Output: json.RawMessage(fmt.Sprintf("%q", output)),
	}
}

// NewItemReference builds an item_reference item.
func NewItemReference(encapsulatedID, id string) Item {
	return Item{
		Type:           string(ItemItemReference),
		ID:             id,
		EncapsulatedID: encapsulatedID,
	}
}

// NewCustomItem builds an opaque prefixed extension item. The type discriminator is
// injected into the preserved raw object when absent.
func NewCustomItem(typeName, raw string) Item {
	it := Item{Type: typeName}
	if strings.TrimSpace(raw) == "" {
		it.Opaque = json.RawMessage(`{"type":"` + typeName + `"}`)
		return it
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		// Invalid JSON and JSON null both lack an object to merge into; the
		// original raw body is preserved verbatim.
		it.Opaque = json.RawMessage(raw)
		return it
	}
	if _, ok := m["type"]; !ok {
		m["type"] = json.RawMessage(fmt.Sprintf("%q", typeName))
		b, err := json.Marshal(m)
		if err != nil {
			it.Opaque = json.RawMessage(raw)
			return it
		}
		raw = string(b)
	}
	it.Opaque = json.RawMessage(raw)
	return it
}

// parseContentParts decodes a message content field (string or part array).
func parseContentParts(raw json.RawMessage) ([]ContentPart, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, malformedf("invalid string content: %v", err)
		}
		return []ContentPart{{Type: "input_text", Text: s}}, nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, malformedf("invalid content array: %v", err)
	}
	return parts, nil
}

// UnmarshalJSON parses a discriminated content part.
func (p *ContentPart) UnmarshalJSON(data []byte) error {
	m, err := decodeObject(data)
	if err != nil {
		return err
	}
	rawType, ok := m["type"]
	if !ok {
		return malformedf("content part missing type discriminator")
	}
	typ, err := rawString(rawType, true)
	if err != nil {
		return err
	}
	if typ == "" {
		return malformedf("content part type cannot be empty")
	}
	if !knownPartType(typ) {
		if !isExtensionType(typ) {
			return malformedf("unknown unprefixed content part type %q", typ)
		}
		p.Type = typ
		p.Opaque = append(json.RawMessage(nil), data...)
		return nil
	}
	p.Type = typ
	p.Text, err = rawString(m["text"], false)
	if err != nil {
		return err
	}
	p.Refusal, err = rawString(m["refusal"], false)
	if err != nil {
		return err
	}
	p.Summary, err = rawString(m["summary"], false)
	if err != nil {
		return err
	}
	p.Filename, err = rawString(m["filename"], false)
	if err != nil {
		return err
	}
	if raw, ok := m["image_url"]; ok {
		p.ImageURL = append(json.RawMessage(nil), raw...)
	}
	if raw, ok := m["file_url"]; ok {
		p.FileURL = append(json.RawMessage(nil), raw...)
	}
	if raw, ok := m["file_data"]; ok {
		p.FileData = append(json.RawMessage(nil), raw...)
	}
	if raw, ok := m["video_url"]; ok {
		p.VideoURL = append(json.RawMessage(nil), raw...)
	}
	if raw, ok := m["annotations"]; ok {
		p.Annotations = append(json.RawMessage(nil), raw...)
	}
	if raw, ok := m["logprobs"]; ok {
		p.Logprobs = append(json.RawMessage(nil), raw...)
	}
	return nil
}

// MarshalJSON renders the content part back onto the wire.
func (p ContentPart) MarshalJSON() ([]byte, error) {
	if p.IsExtension() {
		if len(p.Opaque) > 0 {
			return p.Opaque, nil
		}
		return json.Marshal(map[string]any{"type": p.Type})
	}
	m := map[string]any{"type": p.Type}
	if p.Text != "" {
		m["text"] = p.Text
	}
	if p.Refusal != "" {
		m["refusal"] = p.Refusal
	}
	if p.Summary != "" {
		m["summary"] = p.Summary
	}
	if p.ImageURL != nil {
		m["image_url"] = p.ImageURL
	}
	if p.FileURL != nil {
		m["file_url"] = p.FileURL
	}
	if p.FileData != nil {
		m["file_data"] = p.FileData
	}
	if p.VideoURL != nil {
		m["video_url"] = p.VideoURL
	}
	if p.Annotations != nil {
		m["annotations"] = p.Annotations
	}
	if p.Logprobs != nil {
		m["logprobs"] = p.Logprobs
	}
	if p.Filename != "" {
		m["filename"] = p.Filename
	}
	return json.Marshal(m)
}

func knownPartType(t string) bool {
	switch t {
	case "input_text", "output_text", "input_image", "input_file", "input_audio",
		"input_video", "output_image", "output_file", "refusal", "summary_text",
		"reasoning", "output_text_done", "annotations":
		return true
	}
	return false
}

// UnmarshalJSON parses a reasoning item preserving content, summary, and
// encrypted_content null presence.
func (r *ReasoningItem) UnmarshalJSON(data []byte) error {
	m, err := decodeObject(data)
	if err != nil {
		return err
	}
	if raw, ok := m["content"]; ok && string(raw) != "null" {
		parts, err := parseContentParts(raw)
		if err != nil {
			return err
		}
		r.Content = parts
	}
	if raw, ok := m["summary"]; ok && string(raw) != "null" {
		parts, err := parseContentParts(raw)
		if err != nil {
			return err
		}
		r.Summary = parts
	}
	if raw, ok := m["encrypted_content"]; ok {
		var ec string
		if err := json.Unmarshal(raw, &ec); err != nil {
			return malformedf("encrypted_content must be a string or null")
		}
		r.EncryptedContent = ec
		r.EncryptedContentSet = true
	}
	return nil
}

// MarshalJSON renders the reasoning item back onto the wire.
func (r ReasoningItem) MarshalJSON() ([]byte, error) {
	m := map[string]any{}
	if len(r.Content) > 0 {
		m["content"] = r.Content
	}
	if len(r.Summary) > 0 {
		m["summary"] = r.Summary
	}
	if r.EncryptedContentSet {
		if r.EncryptedContent == "" {
			m["encrypted_content"] = nil
		} else {
			m["encrypted_content"] = r.EncryptedContent
		}
	}
	return json.Marshal(m)
}

// UnmarshalJSON parses a wire tool definition.
func (t *Tool) UnmarshalJSON(data []byte) error {
	m, err := decodeObject(data)
	if err != nil {
		return err
	}
	typ, err := rawString(m["type"], true)
	if err != nil {
		return err
	}
	if typ == "" {
		return malformedf("tool type cannot be empty")
	}
	if typ != "function" && !isExtensionType(typ) {
		return malformedf("unknown unprefixed tool type %q", typ)
	}
	t.Type = typ
	t.Name, err = rawString(m["name"], false)
	if err != nil {
		return err
	}
	t.Description, err = rawString(m["description"], false)
	if err != nil {
		return err
	}
	if raw, ok := m["parameters"]; ok {
		t.Parameters = append(json.RawMessage(nil), raw...)
	}
	if raw, ok := m["strict"]; ok {
		var s bool
		if err := json.Unmarshal(raw, &s); err != nil {
			return malformedf("strict must be boolean")
		}
		t.Strict = &s
	}
	if isExtensionType(typ) {
		t.Opaque = append(json.RawMessage(nil), data...)
	}
	return nil
}

// MarshalJSON renders the tool definition back onto the wire.
func (t Tool) MarshalJSON() ([]byte, error) {
	if t.IsExtension() && len(t.Opaque) > 0 {
		return t.Opaque, nil
	}
	m := map[string]any{"type": t.Type}
	if t.Name != "" {
		m["name"] = t.Name
	}
	if t.Description != "" {
		m["description"] = t.Description
	}
	if t.Parameters != nil {
		m["parameters"] = t.Parameters
	}
	if t.Strict != nil {
		m["strict"] = *t.Strict
	}
	return json.Marshal(m)
}
