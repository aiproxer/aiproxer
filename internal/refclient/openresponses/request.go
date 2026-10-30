package openresponses

import (
	"bytes"
	"encoding/json"
)

// Input is the request `input` field: either a string or an ordered item array.
type Input struct {
	Text    string
	TextSet bool
	Items   []Item
}

// UnmarshalJSON parses a string or item-array input.
func (i *Input) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '"' {
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
func (i Input) MarshalJSON() ([]byte, error) {
	if i.TextSet || i.Text != "" {
		return json.Marshal(i.Text)
	}
	if len(i.Items) > 0 {
		return json.Marshal(i.Items)
	}
	return []byte("null"), nil
}

// CreateParams is the independent wire model of an OpenResponses create request body.
type CreateParams struct {
	Model                string
	Input                Input
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

// MarshalJSON renders the create request body, omitting zero controls and emitting
// declared prefixed extensions at the top level.
func (p CreateParams) MarshalJSON() ([]byte, error) {
	m := map[string]any{}
	if p.Model != "" {
		m["model"] = p.Model
	}
	m["input"] = p.Input
	if p.Instructions != nil {
		m["instructions"] = *p.Instructions
	}
	if len(p.Tools) > 0 {
		m["tools"] = p.Tools
	}
	if p.ToolChoice != nil {
		m["tool_choice"] = p.ToolChoice
	}
	if p.ParallelToolCalls != nil {
		m["parallel_tool_calls"] = *p.ParallelToolCalls
	}
	if p.Temperature != nil {
		m["temperature"] = *p.Temperature
	}
	if p.TopP != nil {
		m["top_p"] = *p.TopP
	}
	if p.MaxOutputTokens != nil {
		m["max_output_tokens"] = *p.MaxOutputTokens
	}
	if p.MaxToolCalls != nil {
		m["max_tool_calls"] = *p.MaxToolCalls
	}
	if p.Truncation != "" {
		m["truncation"] = p.Truncation
	}
	if p.Text != nil {
		m["text"] = p.Text
	}
	if p.Reasoning != nil {
		m["reasoning"] = p.Reasoning
	}
	if p.Store != nil {
		m["store"] = *p.Store
	}
	if p.Background != nil {
		m["background"] = *p.Background
	}
	if p.PreviousResponseID != nil {
		m["previous_response_id"] = *p.PreviousResponseID
	}
	if p.Metadata != nil {
		m["metadata"] = p.Metadata
	}
	if p.ServiceTier != "" {
		m["service_tier"] = p.ServiceTier
	}
	if p.SafetyIdentifier != "" {
		m["safety_identifier"] = p.SafetyIdentifier
	}
	if p.PromptCacheKey != "" {
		m["prompt_cache_key"] = p.PromptCacheKey
	}
	if p.PromptCacheRetention != "" {
		m["prompt_cache_retention"] = p.PromptCacheRetention
	}
	if p.Stream {
		m["stream"] = true
	}
	for k, v := range p.Extensions {
		m[k] = v
	}
	return json.Marshal(m)
}

// UnmarshalJSON parses a create request body, including the official request example.
func (p *CreateParams) UnmarshalJSON(data []byte) error {
	raw, err := decodeObject(data)
	if err != nil {
		return err
	}
	for k, v := range raw {
		switch k {
		case "model":
			if string(v) != "null" {
				s, e := rawString(v, false)
				if e != nil {
					return e
				}
				p.Model = s
			}
		case "input":
			if e := json.Unmarshal(v, &p.Input); e != nil {
				return malformedf("input malformed: %v", e)
			}
		case "instructions":
			if string(v) != "null" {
				s, e := rawString(v, false)
				if e != nil {
					return e
				}
				p.Instructions = &s
			}
		case "tools":
			var tools []Tool
			if e := json.Unmarshal(v, &tools); e != nil {
				return malformedf("tools malformed: %v", e)
			}
			p.Tools = tools
		case "tool_choice":
			p.ToolChoice = append(json.RawMessage(nil), v...)
		case "parallel_tool_calls":
			b, e := rawBool(v, false)
			if e != nil {
				return e
			}
			p.ParallelToolCalls = &b
		case "temperature":
			var f float64
			if e := json.Unmarshal(v, &f); e != nil {
				return malformedf("temperature malformed: %v", e)
			}
			p.Temperature = &f
		case "top_p":
			var f float64
			if e := json.Unmarshal(v, &f); e != nil {
				return malformedf("top_p malformed: %v", e)
			}
			p.TopP = &f
		case "max_output_tokens":
			var n int
			if e := json.Unmarshal(v, &n); e != nil {
				return malformedf("max_output_tokens malformed: %v", e)
			}
			p.MaxOutputTokens = &n
		case "max_tool_calls":
			var n int
			if e := json.Unmarshal(v, &n); e != nil {
				return malformedf("max_tool_calls malformed: %v", e)
			}
			p.MaxToolCalls = &n
		case "truncation":
			s, e := rawString(v, false)
			if e != nil {
				return e
			}
			p.Truncation = s
		case "text":
			p.Text = append(json.RawMessage(nil), v...)
		case "reasoning":
			p.Reasoning = append(json.RawMessage(nil), v...)
		case "store":
			b, e := rawBool(v, false)
			if e != nil {
				return e
			}
			p.Store = &b
		case "background":
			b, e := rawBool(v, false)
			if e != nil {
				return e
			}
			p.Background = &b
		case "previous_response_id":
			if string(v) != "null" {
				s, e := rawString(v, false)
				if e != nil {
					return e
				}
				p.PreviousResponseID = &s
			}
		case "metadata":
			p.Metadata = append(json.RawMessage(nil), v...)
		case "service_tier":
			s, e := rawString(v, false)
			if e != nil {
				return e
			}
			p.ServiceTier = s
		case "safety_identifier":
			s, e := rawString(v, false)
			if e != nil {
				return e
			}
			p.SafetyIdentifier = s
		case "prompt_cache_key":
			s, e := rawString(v, false)
			if e != nil {
				return e
			}
			p.PromptCacheKey = s
		case "prompt_cache_retention":
			s, e := rawString(v, false)
			if e != nil {
				return e
			}
			p.PromptCacheRetention = s
		case "stream":
			b, e := rawBool(v, false)
			if e != nil {
				return e
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
	return nil
}

// CompactParams is the independent wire model of an OpenResponses compact request body.
type CompactParams struct {
	Model          string
	Input          Input
	Instructions   *string
	Reasoning      json.RawMessage
	PromptCacheKey string
	Extensions     map[string]json.RawMessage
}

// MarshalJSON renders the compact request body (no stream/transport controls).
func (p CompactParams) MarshalJSON() ([]byte, error) {
	m := map[string]any{}
	if p.Model != "" {
		m["model"] = p.Model
	}
	m["input"] = p.Input
	if p.Instructions != nil {
		m["instructions"] = *p.Instructions
	}
	if p.Reasoning != nil {
		m["reasoning"] = p.Reasoning
	}
	if p.PromptCacheKey != "" {
		m["prompt_cache_key"] = p.PromptCacheKey
	}
	for k, v := range p.Extensions {
		m[k] = v
	}
	return json.Marshal(m)
}
