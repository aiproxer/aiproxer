package openresponses

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSplitStreamControlPreservesRemainingBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "first", body: `{ "stream" : true , "model" : "m" }`, want: `{  "model" : "m" }`},
		{name: "middle", body: `{ "model" : "m" , "stream" : true , "weird\u0069d" : "x" }`, want: `{ "model" : "m"  , "weird\u0069d" : "x" }`},
		{name: "last", body: `{ "model" : "m" , "stream" : true }`, want: `{ "model" : "m"  }`},
		{name: "only", body: `{  "stream" : true  }`, want: `{    }`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, stream, err := splitStreamControl([]byte(tt.body))
			if err != nil {
				t.Fatalf("splitStreamControl: %v", err)
			}
			if !stream {
				t.Fatal("stream=false, want true")
			}
			if string(got) != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			var decoded any
			if err := json.Unmarshal(got, &decoded); err != nil {
				t.Fatalf("rewritten JSON is invalid: %v (%q)", err, got)
			}
			if bytes.Contains(got, []byte(`"stream"`)) {
				t.Fatalf("stream member remains in %q", got)
			}
		})
	}
}
