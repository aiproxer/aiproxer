package repair_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair/jsonshape"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var orderedParseSink any

func reportInputBytes(b *testing.B, n int) {
	b.Helper()
	b.SetBytes(int64(n))
	b.ReportMetric(float64(n), "input_bytes/op")
}

func benchParseAlone(b *testing.B, name string, payload []byte, wantErr bool) {
	b.Helper()
	b.Run(name, func(b *testing.B) {
		reportInputBytes(b, len(payload))
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			v, err := repair.ExportParseOrderedJSON(payload)
			if wantErr {
				if err == nil {
					b.Fatal("expected parse error")
				}
				continue
			}
			if err != nil {
				b.Fatal(err)
			}
			orderedParseSink = v
		}
	})
}

func benchPreflightPlusParse(b *testing.B, name string, payload []byte, wantPreflightErr, wantParseErr bool) {
	b.Helper()
	b.Run(name, func(b *testing.B) {
		reportInputBytes(b, len(payload))
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			err := repair.ExportPreflightArgsJSON(context.Background(), payload, repair.DefaultMaxArgsBytes)
			if wantPreflightErr {
				if err == nil {
					b.Fatal("expected preflight error")
				}
				continue
			}
			if err != nil {
				b.Fatal(err)
			}
			v, err := repair.ExportParseOrderedJSON(payload)
			if wantParseErr {
				if err == nil {
					b.Fatal("expected parse error")
				}
				continue
			}
			if err != nil {
				b.Fatal(err)
			}
			orderedParseSink = v
		}
	})
}

func BenchmarkOrderedParse(b *testing.B) {
	argsDepth := jsonshape.ToolArgumentsLimits().MaxDepth
	payloads := []struct {
		name    string
		payload []byte
		wantErr bool
	}{
		{"depth1", nestedArrayJSON(1), false},
		{"depth16", nestedArrayJSON(16), false},
		{"depth32", nestedArrayJSON(32), false},
		{"depth64", nestedArrayJSON(argsDepth), false},
		{"wide_object_1024", wideObjectJSON(jsonshape.ToolArgumentsLimits().MaxObjectKeys), false},
		{"wide_array_4096", wideArrayJSON(jsonshape.ToolArgumentsLimits().MaxArrayElems), false},
		{"mixed_64KiB", mixedArgsNear64KiB(), false},
		{"duplicate_key", []byte(`{"a":1,"a":2}`), true},
		{"truncated", []byte(`{"a":1`), true},
	}
	for _, tc := range payloads {
		benchParseAlone(b, tc.name, tc.payload, tc.wantErr)
	}
}

func BenchmarkOrderedPreflightPlusParse(b *testing.B) {
	argsDepth := jsonshape.ToolArgumentsLimits().MaxDepth
	cases := []struct {
		name             string
		payload          []byte
		wantPreflightErr bool
		wantParseErr     bool
	}{
		{"depth1", nestedArrayJSON(1), false, false},
		{"depth16", nestedArrayJSON(16), false, false},
		{"depth32", nestedArrayJSON(32), false, false},
		{"depth64", nestedArrayJSON(argsDepth), false, false},
		{"wide_object_1024", wideObjectJSON(jsonshape.ToolArgumentsLimits().MaxObjectKeys), false, false},
		{"wide_array_4096", wideArrayJSON(jsonshape.ToolArgumentsLimits().MaxArrayElems), false, false},
		{"mixed_64KiB", mixedArgsNear64KiB(), false, false},
		{"duplicate_key", []byte(`{"a":1,"a":2}`), true, false},
		{"truncated", []byte(`{"a":1`), true, false},
	}
	for _, tc := range cases {
		benchPreflightPlusParse(b, tc.name, tc.payload, tc.wantPreflightErr, tc.wantParseErr)
	}
}

func BenchmarkEngineRepair_parserContribution(b *testing.B) {
	schema := []byte(`{"type":"object","properties":{"items":{"type":"array"}},"additionalProperties":true}`)
	catalog := []aipapi.ToolDef{{Name: "bulk", Parameters: schema}}
	engine := repair.NewEngine()
	payload := mixedArgsNear64KiB()
	in := repair.Input{ToolName: "bulk", ArgsJSON: payload, Catalog: catalog}
	if _, err := engine.Repair(in); err != nil {
		b.Fatal(err)
	}
	reportInputBytes(b, len(payload))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		out, err := engine.Repair(in)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkOutcome = out
	}
}
