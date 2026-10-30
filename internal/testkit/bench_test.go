package testkit_test

import (
	"encoding/json"
	"testing"
)

func BenchmarkJSONNormalize(b *testing.B) {
	payload := []byte(`{"b":2,"a":1,"nested":{"z":3,"y":4}}`)
	b.ReportAllocs()
	for b.Loop() {
		var v any
		if err := json.Unmarshal(payload, &v); err != nil {
			b.Fatal(err)
		}
		if _, err := json.Marshal(v); err != nil {
			b.Fatal(err)
		}
	}
}
