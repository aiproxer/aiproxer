package jsonpresence

import "bytes"

// IsJSONNull reports whether raw is exactly the JSON null literal (no allocation).
func IsJSONNull(raw []byte) bool {
	return bytes.Equal(raw, []byte("null"))
}

// IsAbsentOrJSONNull reports whether raw is empty or JSON null (typical optional json.RawMessage).
func IsAbsentOrJSONNull(raw []byte) bool {
	return len(raw) == 0 || IsJSONNull(raw)
}

// IsPresentNonNullJSON reports whether raw is non-empty and not JSON null.
func IsPresentNonNullJSON(raw []byte) bool {
	return len(raw) > 0 && !IsJSONNull(raw)
}
