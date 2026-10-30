package openaicompat

import (
	"bytes"
	"encoding/json"
	"strings"
)

const (
	MaxExtraBodyFields          = 32
	MaxExtraBodyFieldNameBytes  = 64
	MaxExtraBodyFieldValueBytes = 16 << 10
)

var rawJSONNull = json.RawMessage("null")

// ValidExtraBodyFieldName reports whether name is a safe top-level JSON object key.
func ValidExtraBodyFieldName(name string) bool {
	if name == "" || len(name) > MaxExtraBodyFieldNameBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// ExtraBodyValueWithinBounds reports whether raw is non-empty, non-null, and sized for passthrough.
func ExtraBodyValueWithinBounds(raw json.RawMessage) bool {
	return len(raw) > 0 && len(raw) <= MaxExtraBodyFieldValueBytes && !bytes.Equal(raw, rawJSONNull)
}

// CollectPrefixedExtraBody extracts validated fields from call extensions using prefix
// (for example a caller-owned "….extra_body." key namespace).
func CollectPrefixedExtraBody(ext map[string]json.RawMessage, prefix string) map[string]any {
	if len(ext) == 0 || prefix == "" {
		return nil
	}
	out := make(map[string]any)
	for key, raw := range ext {
		if len(out) >= MaxExtraBodyFields {
			break
		}
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		field := strings.TrimPrefix(key, prefix)
		if !ValidExtraBodyFieldName(field) || !ExtraBodyValueWithinBounds(raw) {
			continue
		}
		var v any
		if json.Unmarshal(raw, &v) == nil {
			out[field] = v
		}
	}
	return out
}
