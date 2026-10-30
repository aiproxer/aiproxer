package jsonshape_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
)

func BenchmarkPreflight(b *testing.B) {
	benches := []struct {
		name   string
		data   []byte
		limits jsonshape.Limits
	}{
		{name: "valid small", data: []byte(`{"model":"x","input":"hello"}`)},
		{name: "large string accept", data: []byte(`"` + strings.Repeat(`a`, 64<<10) + `"`), limits: jsonshape.Limits{
			MaxBytes: 128 << 10, MaxStringBytes: 64 << 10, MaxTokens: 8, MaxNumberBytes: 64,
			MaxDepth: 8, MaxArrayElems: 8, MaxObjectKeys: 8, MaxKeyBytes: 64,
		}},
		{name: "wide array reject", data: []byte(`[` + strings.TrimRight(strings.Repeat(`1,`, 4096), ",") + `]`), limits: jsonshape.Limits{MaxArrayElems: 128}},
		{name: "deep reject", data: []byte(strings.Repeat(`[`, 64) + `0` + strings.Repeat(`]`, 64)), limits: jsonshape.Limits{MaxDepth: 16}},
		{name: "malformed early", data: []byte(`{` + strings.Repeat(`"a":`, 256))},
	}

	for _, bench := range benches {
		b.Run(bench.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = jsonshape.Preflight(bench.data, bench.limits)
			}
		})
	}
}
