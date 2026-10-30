package openresponses

import (
	"encoding/json"
	"strings"
)

// InputValue is the request `input` field: a string or an ordered item array.
type InputValue struct {
	Text    string
	TextSet bool
	Items   []Item
}

// UnmarshalJSON parses a string or item-array input.
func (i *InputValue) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return malformedf("input string malformed: %v", err)
		}
		i.Text = s
		i.TextSet = true
		return nil
	}
	var items []Item
	if err := json.Unmarshal(data, &items); err != nil {
		return malformedf("input must be a string or item array: %v", err)
	}
	i.Items = items
	return nil
}

// MarshalJSON renders string or item-array input; empty input marshals as null.
func (i InputValue) MarshalJSON() ([]byte, error) {
	if i.TextSet || i.Text != "" {
		return json.Marshal(i.Text)
	}
	if len(i.Items) > 0 {
		return json.Marshal(i.Items)
	}
	return []byte("null"), nil
}

// CreateRequest is the independent server-side wire model of a create request body
// (also used to parse compaction bodies, which are a subset).
type CreateRequest struct {
	Model                string
	Input                *InputValue
	Instructions         *string
	Tools                []Tool
	ToolChoice           json.RawMessage
	ParallelToolCalls    *bool
	Temperature          *float64
	TopP                 *float64
	MaxOutputTokens      *int
	MaxToolCalls         *int
	Truncation           string
	Text                 json.RawMessage
	Reasoning            json.RawMessage
	Store                *bool
	Background           *bool
	PreviousResponseID   *string
	Metadata             json.RawMessage
	ServiceTier          string
	SafetyIdentifier     string
	PromptCacheKey       string
	PromptCacheRetention string
	Stream               bool
	Extensions           map[string]json.RawMessage
}

// Items returns the ordered input items (empty for string input).
func (p *CreateRequest) Items() []Item {
	if p.Input == nil {
		return nil
	}
	return p.Input.Items
}

// ExtensionItemTypes returns the distinct prefixed input item types.
func (p *CreateRequest) ExtensionItemTypes() []string {
	var out []string
	for _, it := range p.Items() {
		if it.IsExtension() {
			out = append(out, it.Type)
		}
	}
	return out
}

// parseCreateRequest parses and validates a create or compact request body.
func parseCreateRequest(data []byte) (*CreateRequest, error) {
	m, err := decodeObject(data)
	if err != nil {
		return nil, err
	}
	return parseCreateRequestObject(m)
}

func parseCreateRequestObject(m map[string]json.RawMessage) (*CreateRequest, error) {
	p := &CreateRequest{}
	var err error
	for k, v := range m {
		switch k {
		case "model":
			if p.Model, err = objectString(m, "model", false); err != nil {
				return nil, err
			}
		case "input":
			var in InputValue
			if err = json.Unmarshal(v, &in); err != nil {
				return nil, malformedf("input malformed: %v", err)
			}
			p.Input = &in
		case "instructions":
			if string(v) == "null" {
				continue
			}
			var s string
			if err = json.Unmarshal(v, &s); err != nil {
				return nil, malformedf("instructions must be a string or null")
			}
			p.Instructions = &s
		case "tools":
			var tools []Tool
			if err = json.Unmarshal(v, &tools); err != nil {
				return nil, malformedf("tools malformed: %v", err)
			}
			p.Tools = tools
		case "tool_choice":
			p.ToolChoice = append(json.RawMessage(nil), v...)
		case "parallel_tool_calls":
			var b bool
			if err = json.Unmarshal(v, &b); err != nil {
				return nil, malformedf("parallel_tool_calls must be a boolean")
			}
			p.ParallelToolCalls = &b
		case "temperature":
			var f float64
			if err = json.Unmarshal(v, &f); err != nil {
				return nil, malformedf("temperature must be a number or null")
			}
			p.Temperature = &f
		case "top_p":
			var f float64
			if err = json.Unmarshal(v, &f); err != nil {
				return nil, malformedf("top_p must be a number or null")
			}
			p.TopP = &f
		case "max_output_tokens":
			var n int
			if err = json.Unmarshal(v, &n); err != nil {
				return nil, malformedf("max_output_tokens must be an integer or null")
			}
			p.MaxOutputTokens = &n
		case "max_tool_calls":
			var n int
			if err = json.Unmarshal(v, &n); err != nil {
				return nil, malformedf("max_tool_calls must be an integer or null")
			}
			p.MaxToolCalls = &n
		case "truncation":
			if p.Truncation, err = objectString(m, "truncation", false); err != nil {
				return nil, err
			}
		case "text":
			p.Text = append(json.RawMessage(nil), v...)
		case "reasoning":
			p.Reasoning = append(json.RawMessage(nil), v...)
		case "store":
			var b bool
			if err = json.Unmarshal(v, &b); err != nil {
				return nil, malformedf("store must be a boolean or null")
			}
			p.Store = &b
		case "background":
			var b bool
			if err = json.Unmarshal(v, &b); err != nil {
				return nil, malformedf("background must be a boolean or null")
			}
			p.Background = &b
		case "previous_response_id":
			var s string
			if err = json.Unmarshal(v, &s); err != nil {
				return nil, malformedf("previous_response_id must be a string or null")
			}
			p.PreviousResponseID = &s
		case "metadata":
			p.Metadata = append(json.RawMessage(nil), v...)
		case "service_tier":
			if p.ServiceTier, err = objectString(m, "service_tier", false); err != nil {
				return nil, err
			}
		case "safety_identifier":
			if p.SafetyIdentifier, err = objectString(m, "safety_identifier", false); err != nil {
				return nil, err
			}
		case "prompt_cache_key":
			if p.PromptCacheKey, err = objectString(m, "prompt_cache_key", false); err != nil {
				return nil, err
			}
		case "prompt_cache_retention":
			if p.PromptCacheRetention, err = objectString(m, "prompt_cache_retention", false); err != nil {
				return nil, err
			}
		case "stream":
			var b bool
			if err = json.Unmarshal(v, &b); err != nil {
				return nil, malformedf("stream must be a boolean")
			}
			p.Stream = b
		default:
			if isExtensionType(k) {
				if p.Extensions == nil {
					p.Extensions = map[string]json.RawMessage{}
				}
				p.Extensions[k] = append(json.RawMessage(nil), v...)
			}
		}
	}
	return p, nil
}

// CompactRequest is the independent server-side wire model of a compact request body.
type CompactRequest struct {
	Model          string
	Input          *InputValue
	Instructions   *string
	Reasoning      json.RawMessage
	PromptCacheKey string
	Extensions     map[string]json.RawMessage
}

// Items returns the ordered compact input items.
func (p *CompactRequest) Items() []Item {
	if p.Input == nil {
		return nil
	}
	return p.Input.Items
}

// parseCompactRequest parses a compact request body.
func parseCompactRequest(data []byte) (*CompactRequest, error) {
	m, err := decodeObject(data)
	if err != nil {
		return nil, err
	}
	p := &CompactRequest{}
	for k, v := range m {
		switch k {
		case "model":
			if p.Model, err = objectString(m, "model", false); err != nil {
				return nil, err
			}
		case "input":
			var in InputValue
			if err = json.Unmarshal(v, &in); err != nil {
				return nil, malformedf("compact input malformed: %v", err)
			}
			p.Input = &in
		case "instructions":
			if string(v) == "null" {
				continue
			}
			var s string
			if err = json.Unmarshal(v, &s); err != nil {
				return nil, malformedf("compact instructions must be a string or null")
			}
			p.Instructions = &s
		case "reasoning":
			p.Reasoning = append(json.RawMessage(nil), v...)
		case "prompt_cache_key":
			if p.PromptCacheKey, err = objectString(m, "prompt_cache_key", false); err != nil {
				return nil, err
			}
		default:
			if isExtensionType(k) {
				if p.Extensions == nil {
					p.Extensions = map[string]json.RawMessage{}
				}
				p.Extensions[k] = append(json.RawMessage(nil), v...)
			}
		}
	}
	return p, nil
}
