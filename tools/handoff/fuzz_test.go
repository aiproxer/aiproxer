package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func FuzzDecodeResult(f *testing.F) {
	for _, seed := range []string{`{}`, `{"role":"implementer","behavioral":false}`, `{"status":"BLOCKED","status":"APPROVED"}`, `{"source":{"head":"a","head":"b"}}`, `[]`, `{} {}`, `{"commands":[null]}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		result, err := decodeResult(bytes.NewReader(input))
		if err != nil {
			return
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		again, err := decodeResult(bytes.NewReader(encoded))
		if err != nil || !reflect.DeepEqual(result, again) {
			t.Fatalf("successful decode lost result semantics: %v", err)
		}
	})
}
