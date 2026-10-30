package aipapi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SemanticExtensionPresence distinguishes explicit JSON states.
type SemanticExtensionPresence string

const (
	SemanticExtensionAbsent SemanticExtensionPresence = "absent"
	SemanticExtensionNull   SemanticExtensionPresence = "null"
	SemanticExtensionValue  SemanticExtensionPresence = "value"
)

// SemanticExtension directions are intentionally closed. A carrier must state
// which edge owns its value so it cannot become an unscoped envelope channel.
const (
	SemanticExtensionDirectionRequest       = "request"
	SemanticExtensionDirectionResponse      = "response"
	SemanticExtensionDirectionBidirectional = "bidirectional"
)

var semanticEnvelopeKeys = map[string]struct{}{
	"request": {}, "response": {}, "messages": {}, "items": {}, "input": {},
	"output": {}, "events": {}, "stream": {},
}

// SemanticExtension is one bounded negotiated residual semantic carrier.
// Data is never a complete request or response envelope.
type SemanticExtension struct {
	Namespace   string
	Type        string
	Implementor string
	Direction   string
	Presence    SemanticExtensionPresence
	Data        json.RawMessage
}

// PromptCacheKeyValue returns the one effective prompt-cache value. The legacy
// field is accepted only as an equal source-compatible alias to its carrier.
func (c Call) PromptCacheKeyValue() (string, error) {
	legacy := strings.TrimSpace(c.PromptCacheKey)
	carrier := ""
	for i, ext := range c.SemanticExtensions {
		if ext.Namespace != "aip" || ext.Type != "prompt_cache_key" || ext.Implementor != "proxy" || ext.Direction != "request" {
			continue
		}
		if ext.Presence != SemanticExtensionValue {
			return "", &ValidationError{Field: fmt.Sprintf("SemanticExtensions[%d]", i), Message: "prompt_cache_key must carry a JSON string value"}
		}
		var value string
		if err := json.Unmarshal(ext.Data, &value); err != nil {
			return "", &ValidationError{Field: fmt.Sprintf("SemanticExtensions[%d].Data", i), Message: "prompt_cache_key must carry a JSON string value"}
		}
		carrier = strings.TrimSpace(value)
	}
	if legacy != "" && carrier != "" && legacy != carrier {
		return "", &ValidationError{Field: "PromptCacheKey", Message: "legacy alias conflicts with semantic carrier"}
	}
	if carrier != "" {
		return carrier, nil
	}
	return legacy, nil
}

func (e SemanticExtension) validate(field string) error {
	if strings.TrimSpace(e.Namespace) == "" || strings.TrimSpace(e.Type) == "" || strings.TrimSpace(e.Implementor) == "" || strings.TrimSpace(e.Direction) == "" {
		return &ValidationError{Field: field, Message: "semantic extension identity and direction are required"}
	}
	for _, part := range []struct {
		name  string
		value string
		max   int
	}{
		{"Namespace", e.Namespace, MaxExtensionNamespaceBytes},
		{"Type", e.Type, MaxExtensionTypeBytes},
		{"Implementor", e.Implementor, MaxExtensionImplementorBytes},
		{"Direction", e.Direction, MaxExtensionDirectionBytes},
	} {
		if err := validateStringField(field+"."+part.name, part.value, part.max); err != nil {
			return err
		}
		if !validSemanticExtensionToken(part.value) {
			return &ValidationError{Field: field + "." + part.name, Message: "must use lowercase ASCII identifier syntax"}
		}
	}
	switch e.Direction {
	case SemanticExtensionDirectionRequest, SemanticExtensionDirectionResponse, SemanticExtensionDirectionBidirectional:
	default:
		return &ValidationError{Field: field + ".Direction", Message: "must be request, response, or bidirectional"}
	}
	switch e.Presence {
	case SemanticExtensionNull:
		if len(e.Data) != 0 {
			return &ValidationError{Field: field + ".Data", Message: "null semantic extension must not carry data"}
		}
	case SemanticExtensionValue:
		if len(e.Data) == 0 || len(e.Data) > MaxSemanticExtensionDataBytes {
			return &ValidationError{Field: field + ".Data", Message: fmt.Sprintf("value data must be 1..%d bytes", MaxSemanticExtensionDataBytes)}
		}
		if !json.Valid(e.Data) {
			return &ValidationError{Field: field + ".Data", Message: "value data must be valid JSON"}
		}
		if err := validateJSONDepth(e.Data, MaxJSONDepth); err != nil {
			return &ValidationError{Field: field + ".Data", Message: err.Error()}
		}
		if err := validateSemanticExtensionData(e.Data); err != nil {
			return &ValidationError{Field: field + ".Data", Message: err.Error()}
		}
	case SemanticExtensionAbsent:
		return &ValidationError{Field: field + ".Presence", Message: "absent semantic extensions must not be carried"}
	default:
		return &ValidationError{Field: field + ".Presence", Message: "unknown semantic extension presence"}
	}
	return nil
}

func validSemanticExtensionToken(value string) bool {
	if value == "" || value != strings.ToLower(value) {
		return false
	}
	for i, r := range value {
		if i == 0 {
			if r < 'a' || r > 'z' {
				return false
			}
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func validateSemanticExtensionData(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("value data must be a JSON value: %w", err)
	}
	return rejectSemanticEnvelope(value)
}

func rejectSemanticEnvelope(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, forbidden := semanticEnvelopeKeys[strings.ToLower(key)]; forbidden {
				return fmt.Errorf("value data must not contain request/response envelope field %q", key)
			}
			if err := rejectSemanticEnvelope(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectSemanticEnvelope(child); err != nil {
				return err
			}
		}
	}
	return nil
}
