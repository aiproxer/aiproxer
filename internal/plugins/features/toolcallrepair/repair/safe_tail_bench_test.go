package repair_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func BenchmarkSafeTailRepair(b *testing.B) {
	schema := json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"},"mode":{"const":"safe"},"enabled":{"type":"boolean","default":true}},"required":["location"],"additionalProperties":false}`)
	catalog := []aipapi.ToolDef{{Name: "run", Parameters: schema}}
	cases := []struct {
		name string
		args []byte
	}{
		{"valid_pass_through", []byte(`{"location":"NYC"}`)},
		{"append_only", []byte(`{"location":"NYC"`)},
		{"terminal_comma", []byte(`{"location":"NYC",`)},
		{"pending_const", []byte(`{"mode":`)},
		{"pending_default", []byte(`{"enabled":`)},
		{"near_limit_refusal", []byte(`{"location":"` + strings.Repeat("x", 64<<10) + `"`)},
	}
	engine := repair.NewEngine()
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.args)))
			// One untimed call so the near-limit case is proven to take the
			// refusal path before any timing is reported.
			out, err := engine.Repair(repair.Input{ToolName: "run", ArgsJSON: tc.args, Catalog: catalog})
			if err != nil {
				b.Fatal(err)
			}
			if tc.name == "near_limit_refusal" && out.Kind != repair.OutcomeUnrepairable {
				b.Fatalf("near-limit case must be unrepairable, got kind=%v", out.Kind)
			}
			b.ResetTimer()
			for b.Loop() {
				out, err := engine.Repair(repair.Input{ToolName: "run", ArgsJSON: tc.args, Catalog: catalog})
				if err != nil {
					b.Fatal(err)
				}
				benchmarkOutcome = out
			}
		})
	}
}
