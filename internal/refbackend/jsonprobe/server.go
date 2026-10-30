// Package jsonprobe inspects JSON request bodies for reference backend servers.
package jsonprobe

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// HasJSONKey reports whether a request contains a key at any JSON object depth.
// It deliberately ignores quoted text and malformed payloads.
func HasJSONKey(body []byte, key string) bool {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	return findJSONKey(value, key)
}

// HasJSONNumber reports whether a request contains an exact numeric key/value
// pair at any JSON object depth. It prevents fixture controls from matching
// user text or quoted numeric values.
func HasJSONNumber(body []byte, key string, want float64) bool {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return false
	}
	return findJSONNumber(value, key, want)
}

// HasJSONBool reports whether a request contains an exact boolean key/value.
func HasJSONBool(body []byte, key string, want bool) bool {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	return findJSONBool(value, key, want)
}

func findJSONKey(value any, key string) bool {
	switch value := value.(type) {
	case map[string]any:
		if _, ok := value[key]; ok {
			return true
		}
		for _, child := range value {
			if findJSONKey(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if findJSONKey(child, key) {
				return true
			}
		}
	}
	return false
}

func findJSONNumber(value any, key string, want float64) bool {
	switch value := value.(type) {
	case map[string]any:
		if number, ok := value[key].(json.Number); ok {
			got, err := number.Float64()
			if err == nil && got == want {
				return true
			}
		}
		for _, child := range value {
			if findJSONNumber(child, key, want) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if findJSONNumber(child, key, want) {
				return true
			}
		}
	}
	return false
}

func findJSONBool(value any, key string, want bool) bool {
	switch value := value.(type) {
	case map[string]any:
		if got, ok := value[key].(bool); ok && got == want {
			return true
		}
		for _, child := range value {
			if findJSONBool(child, key, want) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if findJSONBool(child, key, want) {
				return true
			}
		}
	}
	return false
}

// TryWriteForcedHTTPError writes a forced HTTP error if status is non-zero.
// If it returns true, the caller should return and stop processing.
func TryWriteForcedHTTPError(w http.ResponseWriter, status int, retryAfter string, body string, defaultBody func(int) string) bool {
	if status == 0 {
		return false
	}
	if retryAfter != "" && status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == "" && defaultBody != nil {
		body = defaultBody(status)
	}
	_, _ = io.WriteString(w, body)
	return true
}
