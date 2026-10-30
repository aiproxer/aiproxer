package jsonshape_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair/jsonshape"
)

func FuzzPreflight(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"x":1}`),
		[]byte(`{`),
		[]byte(`[[[[[[[[[[0]]]]]]]]]]`),
		[]byte(`[` + strings.Repeat(`1,`, 128) + `1]`),
		[]byte(`"` + strings.Repeat(`a`, 4096) + `"`),
		[]byte(`{"a":1,"a":2}`),
		[]byte(`123456789012345678901234567890`),
		[]byte("\"\xff\""),
		[]byte(`{}{}`),
		[]byte(`null`),
		[]byte(`{"outer":{"x":1,"x":2}}`),
		[]byte(`[1,2,3]`),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, limits := range []jsonshape.Limits{
			jsonshape.ToolSchemaLimits(),
			jsonshape.ToolArgumentsLimits(),
		} {
			result, err := jsonshape.Preflight(data, limits)
			if err != nil {
				msg := err.Error()
				if strings.Contains(msg, "\xff") {
					t.Fatalf("error appears to leak invalid UTF-8 payload: %q", msg)
				}
				continue
			}
			if !json.Valid(data) {
				t.Fatalf("Preflight succeeded but encoding/json.Valid returned false")
			}
			if result.Bytes != len(data) || result.Tokens <= 0 || result.MaxDepth < 0 {
				t.Fatalf("unexpected result: %+v", result)
			}
		}
	})
}
