// Package mediaurl extracts media URLs from protocol JSON payloads.
package mediaurl

import (
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/jsonpresence"
)

func ExtractImageURL(raw json.RawMessage) string {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.URL != "" {
		return obj.URL
	}
	return ""
}

func SniffImageMIME(ref string) string {
	lower := strings.ToLower(ref)
	switch {
	case strings.Contains(lower, ".png"), strings.Contains(lower, "image/png"):
		return "image/png"
	case strings.Contains(lower, ".jpg"), strings.Contains(lower, ".jpeg"), strings.Contains(lower, "image/jpeg"):
		return "image/jpeg"
	case strings.Contains(lower, ".webp"):
		return "image/webp"
	default:
		return ""
	}
}
