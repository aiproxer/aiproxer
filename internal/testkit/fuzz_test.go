package testkit

import (
	"encoding/json"
	"testing"
)

// FuzzJSONRoundTrip exercises JSON normalize/compare helpers with arbitrary payloads.
func FuzzJSONRoundTrip(f *testing.F) {
	f.Add([]byte(`{"a":1,"b":2}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 || !json.Valid(data) {
			return
		}
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return
		}
		AssertJSONEqual(t, data, data)
	})
}
