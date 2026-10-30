package secretguard

import (
	"bytes"
	"testing"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// These benchmarks measure the complete guard, including the redact working
// clone. Every iteration starts with the same immutable canonical fixture.
func BenchmarkGuardEvaluateJSON(b *testing.B) {
	for _, tc := range betterLeaksBenchmarkCases(b) {
		if tc.kind != FragmentJSON || tc.capBounded || tc.payloadBytes == benchmarkTenKiB {
			continue
		}
		for _, action := range []string{ActionBlock, ActionLog, ActionRedact} {
			b.Run(tc.name+"/"+action, func(b *testing.B) {
				original := bytes.Clone(tc.call.Messages[0].Parts[0].Content)
				guard := NewGuard(Config{Action: action, ScanMaxBytes: tc.payloadBytes})
				services := sdk.Services{Capability: tc.generation}
				if tc.matcher != nil {
					services.MatcherResolver = staticResolver{m: tc.matcher}
				}
				b.ReportAllocs()
				b.SetBytes(int64(tc.payloadBytes))
				for b.Loop() {
					call := tc.call
					decision, err := guard.Evaluate(b.Context(), &call, sdk.Meta{}, services)
					want := sdk.OutcomePass
					if tc.hit {
						want = sdk.OutcomeBlock
						switch action {
						case ActionLog:
							want = sdk.OutcomeLog
						case ActionRedact:
							want = sdk.OutcomeRedacted
						}
					}
					if err != nil || decision.Outcome != want || decision.Validate() != nil {
						b.Fatal("guard benchmark did not exercise the expected decision")
					}
					if !bytes.Equal(tc.call.Messages[0].Parts[0].Content, original) || want != sdk.OutcomeRedacted && !bytes.Equal(call.Messages[0].Parts[0].Content, original) {
						b.Fatal("read-only guard changed canonical JSON")
					}
				}
			})
		}
	}
}

func BenchmarkGuardEvaluateJSONConcurrent(b *testing.B) {
	for _, tc := range betterLeaksBenchmarkCases(b) {
		if tc.kind != FragmentJSON || tc.detector != "hybrid" || tc.payloadBytes != benchmarkTwoMiB {
			continue
		}
		b.Run(tc.name, func(b *testing.B) {
			original := bytes.Clone(tc.call.Messages[0].Parts[0].Content)
			guard := NewGuard(Config{Action: ActionLog, ScanMaxBytes: tc.payloadBytes})
			services := sdk.Services{MatcherResolver: staticResolver{m: tc.matcher}, Capability: tc.generation}
			b.ReportAllocs()
			b.SetBytes(int64(tc.payloadBytes))
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					call := tc.call
					decision, err := guard.Evaluate(b.Context(), &call, sdk.Meta{}, services)
					want := sdk.OutcomePass
					if tc.hit {
						want = sdk.OutcomeLog
					}
					if err != nil || decision.Outcome != want || decision.Validate() != nil || !bytes.Equal(call.Messages[0].Parts[0].Content, original) {
						b.Error("concurrent guard benchmark lost its decision")
					}
				}
			})
		})
	}
}
