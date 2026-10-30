package openresponses

import (
	"encoding/json"

	"github.com/aiproxer/aiproxer/internal/core/jsonpresence"
)

// cloneBytes returns a clean copy of byte slice b (or nil if b is nil).
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}

// ensureNonNullJSON returns raw if present and non-null, or literal JSON "null" if empty/nil/null.
func ensureNonNullJSON(raw []byte) json.RawMessage {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return json.RawMessage("null")
	}
	return json.RawMessage(cloneBytes(raw))
}

// ensureNonNilSlice returns s if non-nil, or an empty slice if s is nil.
func ensureNonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ensureNonNilMap returns m if non-nil, or an empty map if m is nil.
func ensureNonNilMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return make(map[K]V)
	}
	return m
}
