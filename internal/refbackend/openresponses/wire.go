package openresponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Item discriminator constants for the independent server-side wire model.
const (
	ItemMessage            = "message"
	ItemFunctionCall       = "function_call"
	ItemFunctionCallOutput = "function_call_output"
	ItemReasoning          = "reasoning"
	ItemItemReference      = "item_reference"
	ItemCompaction         = "compaction"
)

func knownItemType(t string) bool {
	switch t {
	case ItemMessage, ItemFunctionCall, ItemFunctionCallOutput, ItemReasoning,
		ItemItemReference, ItemCompaction:
		return true
	}
	return false
}

func isExtensionType(t string) bool { return strings.Contains(t, ":") }

// ReasoningItem carries provider-controlled reasoning payloads.
type ReasoningItem struct {
	Content             []ContentPart
	EncryptedContent    string
	EncryptedContentSet bool
	Summary             []ContentPart
}

// Item is the discriminated ordered unit of context on the OpenResponses wire.
type Item struct {
	Type             string
	ID               string
	Status           string
	Role             string
	Phase            string
	Content          []ContentPart
	CallID           string
	Name             string
	Arguments        string
	Output           json.RawMessage
	Reasoning        *ReasoningItem
	EncapsulatedID   string
	EncryptedContent string
	Opaque           json.RawMessage
}

// IsExtension reports whether the item uses a prefixed implementor slug.
func (it Item) IsExtension() bool { return isExtensionType(it.Type) }

// UnmarshalJSON parses a discriminated wire item. Known portable types are typed;
// unknown valid prefixed types are preserved opaquely; unknown unprefixed types fail.
func (it *Item) UnmarshalJSON(data []byte) error {
	m, err := decodeObject(data)
	if err != nil {
		return err
	}
	typ, err := objectString(m, "type", true)
	if err != nil {
		return err
	}
	if !knownItemType(typ) {
		if !isExtensionType(typ) {
			return malformedf("unknown unprefixed item type %q", typ)
		}
		it.Type = typ
		it.ID, _ = objectString(m, "id", false)
		it.Status, _ = objectString(m, "status", false)
		it.Opaque = append(json.RawMessage(nil), data...)
		return nil
	}
	it.Type = typ
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"id", &it.ID},
		{"status", &it.Status},
		{"role", &it.Role},
		{"phase", &it.Phase},
		{"call_id", &it.CallID},
		{"name", &it.Name},
		{"encapsulated_id", &it.EncapsulatedID},
		{"encrypted_content", &it.EncryptedContent},
	} {
		v, e := objectString(m, f.key, false)
		if e != nil {
			return e
		}
		*f.dst = v
	}
	if raw, ok := m["arguments"]; ok && string(raw) != "null" {
		if it.Arguments, err = objectString(m, "arguments", false); err != nil {
			return err
		}
	}
	if raw, ok := m["output"]; ok && string(raw) != "null" {
		it.Output = append(json.RawMessage(nil), raw...)
	}
	if typ == ItemReasoning {
		return it.unmarshalReasoning(m)
	}
	if raw, ok := m["content"]; ok && string(raw) != "null" {
		parts, e := parseContentParts(raw)
		if e != nil {
			return e
		}
		it.Content = parts
	}
	if raw, ok := m["reasoning"]; ok && string(raw) != "null" {
		var r ReasoningItem
		if e := json.Unmarshal(raw, &r); e != nil {
			return malformedf("item reasoning malformed: %v", e)
		}
		it.Reasoning = &r
	}
	return nil
}

func (it *Item) unmarshalReasoning(m map[string]json.RawMessage) error {
	r := &ReasoningItem{}
	if raw, ok := m["reasoning"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, r); err != nil {
			return malformedf("reasoning payload malformed: %v", err)
		}
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
	it.Reasoning = r
	return nil
}

// MarshalJSON renders the item back onto the wire, preserving opaque extension bytes.
func (it Item) MarshalJSON() ([]byte, error) {
	if it.IsExtension() && len(it.Opaque) > 0 {
		return it.Opaque, nil
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

func knownPartType(t string) bool {
	switch t {
	case "input_text", "output_text", "input_image", "input_file", "input_audio",
		"input_video", "output_image", "output_file", "refusal", "summary_text",
		"reasoning", "output_text_done", "annotations":
		return true
	}
	return false
}

// ContentPart is a discriminated content part inside a message.
type ContentPart struct {
	Type        string
	Text        string
	Refusal     string
	Summary     string
	Filename    string
	ImageURL    json.RawMessage
	FileURL     json.RawMessage
	FileData    json.RawMessage
	VideoURL    json.RawMessage
	Annotations json.RawMessage
	Logprobs    json.RawMessage
	Opaque      json.RawMessage
}

// IsExtension reports whether the content part uses a prefixed implementor slug.
func (p ContentPart) IsExtension() bool { return isExtensionType(p.Type) }

// UnmarshalJSON parses a discriminated content part.
func (p *ContentPart) UnmarshalJSON(data []byte) error {
	m, err := decodeObject(data)
	if err != nil {
		return err
	}
	typ, err := objectString(m, "type", true)
	if err != nil {
		return err
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
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"text", &p.Text},
		{"refusal", &p.Refusal},
		{"summary", &p.Summary},
		{"filename", &p.Filename},
	} {
		v, e := objectString(m, f.key, false)
		if e != nil {
			return e
		}
		*f.dst = v
	}
	for _, f := range []struct {
		key string
		dst *json.RawMessage
	}{
		{"image_url", &p.ImageURL},
		{"file_url", &p.FileURL},
		{"file_data", &p.FileData},
		{"video_url", &p.VideoURL},
		{"annotations", &p.Annotations},
		{"logprobs", &p.Logprobs},
	} {
		if raw, ok := m[f.key]; ok {
			*f.dst = append(json.RawMessage(nil), raw...)
		}
	}
	return nil
}

// MarshalJSON renders the content part back onto the wire.
func (p ContentPart) MarshalJSON() ([]byte, error) {
	if p.IsExtension() && len(p.Opaque) > 0 {
		return p.Opaque, nil
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
	if p.Filename != "" {
		m["filename"] = p.Filename
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
	return json.Marshal(m)
}

// UnmarshalJSON parses a reasoning item preserving null presence.
func (r *ReasoningItem) UnmarshalJSON(data []byte) error {
	m, err := decodeObject(data)
	if err != nil {
		return err
	}
	if raw, ok := m["content"]; ok && string(raw) != "null" {
		parts, e := parseContentParts(raw)
		if e != nil {
			return e
		}
		r.Content = parts
	}
	if raw, ok := m["summary"]; ok && string(raw) != "null" {
		parts, e := parseContentParts(raw)
		if e != nil {
			return e
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

// Tool is a function tool or a prefixed hosted tool.
type Tool struct {
	Type        string
	Name        string
	Description string
	Parameters  json.RawMessage
	Strict      *bool
	Opaque      json.RawMessage
}

// IsExtension reports whether the tool uses a prefixed implementor slug.
func (t Tool) IsExtension() bool { return isExtensionType(t.Type) }

// UnmarshalJSON parses a wire tool definition.
func (t *Tool) UnmarshalJSON(data []byte) error {
	m, err := decodeObject(data)
	if err != nil {
		return err
	}
	typ, err := objectString(m, "type", true)
	if err != nil {
		return err
	}
	if typ != "function" && !isExtensionType(typ) {
		return malformedf("unknown unprefixed tool type %q", typ)
	}
	t.Type = typ
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"name", &t.Name}, {"description", &t.Description},
	} {
		v, e := objectString(m, f.key, false)
		if e != nil {
			return e
		}
		*f.dst = v
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

// Usage carries token counters.
type Usage struct {
	InputTokens     int
	CachedTokens    int
	OutputTokens    int
	ReasoningTokens int
	TotalTokens     int
}

// MarshalJSON renders usage with the required input/output/total token presence
// plus the standard detail sub-objects.
func (u Usage) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"input_tokens":  u.InputTokens,
		"output_tokens": u.OutputTokens,
		"total_tokens":  u.TotalTokens,
		"input_tokens_details": map[string]any{
			"cached_tokens": u.CachedTokens,
		},
		"output_tokens_details": map[string]any{
			"reasoning_tokens": u.ReasoningTokens,
		},
	}
	return json.Marshal(m)
}

// ErrorObject is the structured OpenResponses error payload.
type ErrorObject struct {
	Type    string
	Code    string
	Message string
	Param   string
}

// MarshalJSON renders the structured error envelope.
func (e ErrorObject) MarshalJSON() ([]byte, error) {
	m := map[string]any{}
	if e.Type != "" {
		m["type"] = e.Type
	}
	if e.Code != "" {
		m["code"] = e.Code
	}
	if e.Message != "" {
		m["message"] = e.Message
	}
	if e.Param != "" {
		m["param"] = e.Param
	}
	return json.Marshal(m)
}

// parseContentParts decodes a content field (string or part array).
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

func malformedf(format string, args ...any) error {
	return fmt.Errorf("refbackend/openresponses: %s", fmt.Sprintf(format, args...))
}

// decodeObject decodes raw bytes into a map, rejecting non-object roots and empty bodies.
func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, malformedf("empty payload")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, malformedf("invalid JSON object: %v", err)
	}
	return m, nil
}

func objectString(m map[string]json.RawMessage, key string, required bool) (string, error) {
	raw, ok := m[key]
	if !ok {
		if required {
			return "", malformedf("missing required field %q", key)
		}
		return "", nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", malformedf("field %q must be a string", key)
	}
	return s, nil
}
