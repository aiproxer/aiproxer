package openresponses

import (
	"encoding/json"
	"slices"
	"strings"
)

// ResponseResource is the independent wire model of the complete OpenResponses
// response resource. Pointer fields preserve null/default presence.
type ResponseResource struct {
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

// requiredResponseFields is the pinned required-presence set. Fields that the
// profile permits to be null must still appear on the wire.
var requiredResponseFields = []string{
	"id", "object", "created_at", "status", "model", "output",
	"parallel_tool_calls", "reasoning", "store", "background", "temperature",
	"text", "tool_choice", "tools", "top_p", "truncation", "usage", "metadata",
	"service_tier", "max_output_tokens", "max_tool_calls", "instructions",
	"previous_response_id", "error", "incomplete_details",
}

// ParseResponseResource decodes and validates a response resource with bounded reads
// and required-presence enforcement.
func ParseResponseResource(data []byte, opts ParseOptions) (*ResponseResource, error) {
	return parseResponseResource(data, opts, true)
}

// ParseResponseResourceLoose decodes a response resource without required-presence
// enforcement. It is used for partial response envelopes embedded in streaming events.
func ParseResponseResourceLoose(data []byte, opts ParseOptions) (*ResponseResource, error) {
	return parseResponseResource(data, opts, false)
}

func parseResponseResource(data []byte, opts ParseOptions, strict bool) (*ResponseResource, error) {
	opts = opts.normalize()
	m, err := decodeObject(data)
	if err != nil {
		return nil, err
	}
	if strict {
		for _, f := range requiredResponseFields {
			if !hasKey(m, f) {
				return nil, presencef(f)
			}
		}
	}
	res := &ResponseResource{Extensions: map[string]json.RawMessage{}}
	for k, raw := range m {
		switch k {
		case "id":
			res.ID, err = rawString(raw, true)
		case "object":
			res.Object, err = rawString(raw, true)
		case "created_at":
			res.CreatedAt, err = rawInt64(raw, true)
		case "status":
			res.Status, err = rawString(raw, true)
		case "completed_at":
			if string(raw) != "null" {
				v, e := rawInt64(raw, false)
				if e == nil {
					res.CompletedAt = &v
				}
				err = e
			}
		case "model":
			res.Model, err = rawString(raw, true)
		case "output":
			var items []Item
			if e := json.Unmarshal(raw, &items); e != nil {
				return nil, malformedf("output must be an item array: %v", e)
			}
			if len(items) > opts.MaxItems {
				return nil, limitf("output item count %d exceeds %d", len(items), opts.MaxItems)
			}
			res.Output = items
		case "parallel_tool_calls":
			res.ParallelToolCalls, err = rawBool(raw, true)
		case "reasoning":
			res.Reasoning = append(json.RawMessage(nil), raw...)
		case "store":
			res.Store, err = rawBool(raw, true)
		case "background":
			res.Background, err = rawBool(raw, true)
		case "temperature":
			if string(raw) != "null" {
				var v float64
				if e := json.Unmarshal(raw, &v); e != nil {
					return nil, malformedf("temperature must be number or null")
				}
				res.Temperature = &v
			}
		case "text":
			res.Text = append(json.RawMessage(nil), raw...)
		case "tool_choice":
			res.ToolChoice = append(json.RawMessage(nil), raw...)
		case "tools":
			var tools []Tool
			if e := json.Unmarshal(raw, &tools); e != nil {
				return nil, malformedf("tools must be a tool array: %v", e)
			}
			res.Tools = tools
		case "top_p":
			if string(raw) != "null" {
				var v float64
				if e := json.Unmarshal(raw, &v); e != nil {
					return nil, malformedf("top_p must be number or null")
				}
				res.TopP = &v
			}
		case "truncation":
			res.Truncation, err = rawString(raw, true)
		case "usage":
			var u Usage
			if e := parseUsage(raw, &u); e != nil {
				return nil, e
			}
			res.Usage = u
		case "metadata":
			res.Metadata = append(json.RawMessage(nil), raw...)
		case "service_tier":
			res.ServiceTier, err = rawString(raw, true)
		case "max_output_tokens":
			if string(raw) != "null" {
				var v int
				if e := json.Unmarshal(raw, &v); e != nil {
					return nil, malformedf("max_output_tokens must be integer or null")
				}
				res.MaxOutputTokens = &v
			}
		case "max_tool_calls":
			if string(raw) != "null" {
				var v int
				if e := json.Unmarshal(raw, &v); e != nil {
					return nil, malformedf("max_tool_calls must be integer or null")
				}
				res.MaxToolCalls = &v
			}
		case "instructions":
			if string(raw) != "null" {
				var v string
				if e := json.Unmarshal(raw, &v); e != nil {
					return nil, malformedf("instructions must be string or null")
				}
				res.Instructions = &v
			}
		case "previous_response_id":
			if string(raw) != "null" {
				var v string
				if e := json.Unmarshal(raw, &v); e != nil {
					return nil, malformedf("previous_response_id must be string or null")
				}
				res.PreviousResponseID = &v
			}
		case "error":
			if string(raw) != "null" {
				var eo ErrorObject
				if e := json.Unmarshal(raw, &eo); e != nil {
					return nil, malformedf("error object malformed: %v", e)
				}
				res.Error = &eo
			}
		case "incomplete_details":
			res.IncompleteDetails = append(json.RawMessage(nil), raw...)
		case "safety_identifier":
			if string(raw) != "null" {
				var v string
				if e := json.Unmarshal(raw, &v); e != nil {
					return nil, malformedf("safety_identifier must be string or null")
				}
				res.SafetyIdentifier = &v
			}
		case "prompt_cache_key":
			if string(raw) != "null" {
				var v string
				if e := json.Unmarshal(raw, &v); e != nil {
					return nil, malformedf("prompt_cache_key must be string or null")
				}
				res.PromptCacheKey = &v
			}
		case "prompt_cache_retention":
			if string(raw) != "null" {
				var v string
				if e := json.Unmarshal(raw, &v); e != nil {
					return nil, malformedf("prompt_cache_retention must be string or null")
				}
				res.PromptCacheRetention = &v
			}
		default:
			res.Extensions[k] = append(json.RawMessage(nil), raw...)
		}
		if err != nil {
			return nil, err
		}
	}
	if strict {
		if slices.Contains([]string{res.ID, res.Object, res.Status, res.Model}, "") {
			return nil, malformedf("required string field must be non-empty")
		}
	}
	return res, nil
}

// OutputText concatenates the text of output_text content parts across assistant
// message items in order.
func (r *ResponseResource) OutputText() string {
	var b strings.Builder
	for _, it := range r.Output {
		if it.Type != string(ItemMessage) {
			continue
		}
		for _, p := range it.Content {
			if p.Type == "output_text" && p.Text != "" {
				b.WriteString(p.Text)
			}
		}
	}
	return b.String()
}

// Failed reports whether the response ended in the failed state.
func (r *ResponseResource) Failed() bool { return r.Status == "failed" }

// Terminal reports whether the resource is in a terminal state.
func (r *ResponseResource) Terminal() bool {
	switch r.Status {
	case "completed", "failed", "incomplete":
		return true
	}
	return false
}

// parseUsage decodes token counters; input/output/total are required.
func parseUsage(raw json.RawMessage, u *Usage) error {
	m, err := decodeObject(raw)
	if err != nil {
		return err
	}
	for _, f := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		if !hasKey(m, f) {
			return presencef("usage." + f)
		}
	}
	if u.InputTokens, err = intValue(m["input_tokens"]); err != nil {
		return err
	}
	if u.OutputTokens, err = intValue(m["output_tokens"]); err != nil {
		return err
	}
	if u.TotalTokens, err = intValue(m["total_tokens"]); err != nil {
		return err
	}
	if rawDetails, ok := m["input_tokens_details"]; ok && string(rawDetails) != "null" {
		dm, err := decodeObject(rawDetails)
		if err != nil {
			return err
		}
		if v, err := intValue(dm["cached_tokens"]); err == nil {
			u.CachedTokens = v
		} else if hasKey(dm, "cached_tokens") {
			return err
		}
	}
	if rawDetails, ok := m["output_tokens_details"]; ok && string(rawDetails) != "null" {
		dm, err := decodeObject(rawDetails)
		if err != nil {
			return err
		}
		if v, err := intValue(dm["reasoning_tokens"]); err == nil {
			u.ReasoningTokens = v
		} else if hasKey(dm, "reasoning_tokens") {
			return err
		}
	}
	return nil
}

func intValue(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, malformedf("expected integer, got %s", truncate(raw))
	}
	return v, nil
}

// CompactResource is the independent wire model of the response.compaction resource.
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

// IsCompact reports whether the resource is a compaction resource.
func (c *CompactResource) IsCompact() bool { return c.Object == "response.compaction" }

// ParseCompactResource decodes and validates a response.compaction resource.
func ParseCompactResource(data []byte, opts ParseOptions) (*CompactResource, error) {
	opts = opts.normalize()
	m, err := decodeObject(data)
	if err != nil {
		return nil, err
	}
	for _, f := range []string{"id", "object", "created_at", "status", "model", "output", "usage"} {
		if !hasKey(m, f) {
			return nil, presencef(f)
		}
	}
	res := &CompactResource{Extensions: map[string]json.RawMessage{}}
	for k, raw := range m {
		switch k {
		case "id":
			res.ID, err = rawString(raw, true)
		case "object":
			res.Object, err = rawString(raw, true)
		case "created_at":
			res.CreatedAt, err = rawInt64(raw, true)
		case "status":
			res.Status, err = rawString(raw, true)
		case "model":
			res.Model, err = rawString(raw, true)
		case "output":
			var items []Item
			if e := json.Unmarshal(raw, &items); e != nil {
				return nil, malformedf("output must be an item array: %v", e)
			}
			if len(items) > opts.MaxItems {
				return nil, limitf("output item count %d exceeds %d", len(items), opts.MaxItems)
			}
			res.Output = items
		case "usage":
			var u Usage
			if e := parseUsage(raw, &u); e != nil {
				return nil, e
			}
			res.Usage = u
		default:
			res.Extensions[k] = append(json.RawMessage(nil), raw...)
		}
		if err != nil {
			return nil, err
		}
	}
	if slices.Contains([]string{res.ID, res.Object, res.Status, res.Model}, "") {
		return nil, malformedf("required string field must be non-empty")
	}
	return res, nil
}
