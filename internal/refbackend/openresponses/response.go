package openresponses

import (
	"encoding/json"
)

// requiredResponseFields is the pinned required-presence set for a response resource.
var requiredResponseFields = []string{
	"id", "object", "created_at", "status", "model", "output",
	"parallel_tool_calls", "reasoning", "store", "background", "temperature",
	"text", "tool_choice", "tools", "top_p", "truncation", "usage", "metadata",
	"service_tier", "max_output_tokens", "max_tool_calls", "instructions",
	"previous_response_id", "error", "incomplete_details",
}

// Resource is the independent server-side wire model of the complete OpenResponses
// response resource. Pointer fields preserve null/default presence on the wire.
type Resource struct {
	ID                   string
	Object               string
	CreatedAt            int64
	Status               string
	CompletedAt          *int64
	Model                string
	Output               []Item
	ParallelToolCalls    bool
	Reasoning            json.RawMessage
	Store                bool
	Background           bool
	Temperature          *float64
	Text                 json.RawMessage
	ToolChoice           json.RawMessage
	Tools                []Tool
	TopP                 *float64
	Truncation           string
	Usage                Usage
	Metadata             json.RawMessage
	ServiceTier          string
	MaxOutputTokens      *int
	MaxToolCalls         *int
	Instructions         *string
	PreviousResponseID   *string
	Error                *ErrorObject
	IncompleteDetails    json.RawMessage
	SafetyIdentifier     *string
	PromptCacheKey       *string
	PromptCacheRetention *string
	Extensions           map[string]json.RawMessage
}

// NewResource returns a completed response resource with the pinned required
// presence populated with profile defaults.
func NewResource(id, model string, createdAt int64, output []Item) *Resource {
	if output == nil {
		output = []Item{}
	}
	temperature := 1.0
	topP := 1.0
	return &Resource{
		ID:                id,
		Object:            "response",
		CreatedAt:         createdAt,
		Status:            "completed",
		Model:             model,
		Output:            output,
		ParallelToolCalls: false,
		Store:             true,
		Background:        false,
		Temperature:       &temperature,
		Text:              json.RawMessage(`{}`),
		ToolChoice:        json.RawMessage(`"auto"`),
		Tools:             []Tool{},
		TopP:              &topP,
		Truncation:        "disabled",
		Metadata:          json.RawMessage(`{}`),
		ServiceTier:       "default",
		Extensions:        map[string]json.RawMessage{},
	}
}

// WithUsage overrides the token counters.
func (r *Resource) WithUsage(u Usage) *Resource { r.Usage = u; return r }

// WithError marks the resource failed with a structured error.
func (r *Resource) WithError(status string, e *ErrorObject) *Resource {
	r.Status = status
	r.Error = e
	return r
}

// MarshalJSON emits every required field (null where permitted) plus optional
// extensions, guaranteeing the pinned required-presence contract.
func (r *Resource) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"id":                   r.ID,
		"object":               orString(r.Object, "response"),
		"created_at":           r.CreatedAt,
		"status":               orString(r.Status, "completed"),
		"model":                r.Model,
		"output":               nonNilItems(r.Output),
		"parallel_tool_calls":  r.ParallelToolCalls,
		"reasoning":            rawOrNull(r.Reasoning),
		"store":                r.Store,
		"background":           r.Background,
		"temperature":          floatOrNull(r.Temperature),
		"text":                 rawOrEmptyObject(r.Text),
		"tool_choice":          rawOrString(r.ToolChoice, "auto"),
		"tools":                nonNilTools(r.Tools),
		"top_p":                floatOrNull(r.TopP),
		"truncation":           orString(r.Truncation, "disabled"),
		"usage":                r.Usage,
		"metadata":             rawOrEmptyObject(r.Metadata),
		"service_tier":         orString(r.ServiceTier, "default"),
		"max_output_tokens":    intOrNull(r.MaxOutputTokens),
		"max_tool_calls":       intOrNull(r.MaxToolCalls),
		"instructions":         stringOrNull(r.Instructions),
		"previous_response_id": stringOrNull(r.PreviousResponseID),
		"error":                errOrNull(r.Error),
		"incomplete_details":   rawOrNull(r.IncompleteDetails),
	}
	if r.CompletedAt != nil {
		m["completed_at"] = *r.CompletedAt
	} else {
		m["completed_at"] = nil
	}
	if r.SafetyIdentifier != nil {
		m["safety_identifier"] = *r.SafetyIdentifier
	}
	if r.PromptCacheKey != nil {
		m["prompt_cache_key"] = *r.PromptCacheKey
	}
	if r.PromptCacheRetention != nil {
		m["prompt_cache_retention"] = *r.PromptCacheRetention
	}
	for k, v := range r.Extensions {
		m[k] = v
	}
	return json.Marshal(m)
}

// OmitRequiredField re-marshals the resource without one required field, for the
// malformed-resource-missing-field mode.
func (r *Resource) OmitRequiredField(field string) ([]byte, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	m, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	delete(m, field)
	return json.Marshal(m)
}

// CorruptField re-marshals the resource with one field forced to a wrong type,
// for the malformed-resource-bad-type mode.
func (r *Resource) CorruptField(field string) ([]byte, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	m, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	m[field] = json.RawMessage(`"not-the-right-type"`)
	return json.Marshal(m)
}

// CompactResource is the independent server-side wire model of the
// response.compaction resource.
type CompactResource struct {
	ID         string
	Object     string
	CreatedAt  int64
	Status     string
	Model      string
	Output     []Item
	Usage      Usage
	Extensions map[string]json.RawMessage
}

// NewCompactResource returns a completed compaction resource.
func NewCompactResource(id, model string, createdAt int64, output []Item) *CompactResource {
	if output == nil {
		output = []Item{}
	}
	return &CompactResource{
		ID:         id,
		Object:     "response.compaction",
		CreatedAt:  createdAt,
		Status:     "completed",
		Model:      model,
		Output:     output,
		Extensions: map[string]json.RawMessage{},
	}
}

// MarshalJSON renders the compaction resource with required presence.
func (c *CompactResource) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"id":         c.ID,
		"object":     orString(c.Object, "response.compaction"),
		"created_at": c.CreatedAt,
		"status":     orString(c.Status, "completed"),
		"model":      c.Model,
		"output":     nonNilItems(c.Output),
		"usage":      c.Usage,
	}
	for k, v := range c.Extensions {
		m[k] = v
	}
	return json.Marshal(m)
}

// Item builder helpers -----------------------------------------------------

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

// NewMessagePartsItem builds a message item with arbitrary content parts.
func NewMessagePartsItem(role, phase string, parts ...ContentPart) Item {
	return Item{
		Type:    string(ItemMessage),
		Role:    role,
		Phase:   phase,
		Content: parts,
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
		Output: json.RawMessage(jsonEscape(output)),
	}
}

// NewReasoningItem builds a reasoning item with summary and content parts.
func NewReasoningItem(id string, summary, content []ContentPart) Item {
	return Item{
		Type:   string(ItemReasoning),
		ID:     id,
		Status: "completed",
		Reasoning: &ReasoningItem{
			Summary: summary,
			Content: content,
		},
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

// NewCompactionItem builds a pinned-schema-valid compaction item. The pinned
// profile requires a non-empty encrypted_content blob on compaction output
// items; `raw`, when supplied and valid JSON, is merged into the preserved
// opaque body so callers can attach extra fields deterministically.
func NewCompactionItem(id string, raw string) Item {
	encrypted := "gAAAAABwLJ4h0Z" + id
	opaque := json.RawMessage(`{"type":"compaction","id":"` + id + `","encrypted_content":"` + encrypted + `"}`)
	if raw != "" {
		var m map[string]json.RawMessage
		// JSON null decodes without error into a nil map; keep the default
		// pinned-schema body instead of writing into it.
		if json.Unmarshal([]byte(raw), &m) == nil && m != nil {
			m["type"] = json.RawMessage(`"compaction"`)
			m["encrypted_content"] = json.RawMessage(`"` + encrypted + `"`)
			if b, err := json.Marshal(m); err == nil {
				opaque = b
			}
		}
	}
	return Item{Type: string(ItemCompaction), ID: id, EncryptedContent: encrypted, Opaque: opaque}
}

// NewExtensionItem builds an opaque prefixed extension item. The type discriminator
// is injected into the preserved raw object when absent.
func NewExtensionItem(typeName, raw string) Item {
	it := Item{Type: typeName}
	if raw == "" {
		it.Opaque = json.RawMessage(`{"type":"` + typeName + `"}`)
		return it
	}
	m := map[string]json.RawMessage{}
	// JSON null decodes without error into a nil map; preserve the raw body
	// instead of writing the discriminator into it.
	if err := json.Unmarshal([]byte(raw), &m); err == nil && m != nil {
		if _, ok := m["type"]; !ok {
			m["type"] = json.RawMessage(`"` + typeName + `"`)
		}
		if b, err := json.Marshal(m); err == nil {
			raw = string(b)
		}
	}
	it.Opaque = json.RawMessage(raw)
	return it
}

// NewTextPart builds an output_text content part.
func NewTextPart(text string) ContentPart {
	return ContentPart{
		Type:        "output_text",
		Text:        text,
		Annotations: json.RawMessage(`[]`),
	}
}

// marshal/raw helpers ------------------------------------------------------

func orString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func nonNilItems(items []Item) []Item {
	if items == nil {
		return []Item{}
	}
	return items
}

func nonNilTools(tools []Tool) []Tool {
	if tools == nil {
		return []Tool{}
	}
	return tools
}

func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func rawOrEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func rawOrString(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`"` + fallback + `"`)
	}
	return raw
}

func floatOrNull(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func intOrNull(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func stringOrNull(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func errOrNull(v *ErrorObject) any {
	if v == nil {
		return nil
	}
	return *v
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
