package runtimebundle

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
)

// BenchmarkInventorySnapshotCompatibleProjectionOnce measures the operator
// snapshot assembly path and guards its projection contract: one compatible
// backend projection is performed for each snapshot, then reused for both the
// compatible_backends and instance_diagnostics views.
func BenchmarkInventorySnapshotCompatibleProjectionOnce(b *testing.B) {
	cfg := &config.Config{}
	projector := func(*config.Config) []diag.CompatibleBackendRow {
		return []diag.CompatibleBackendRow{{
			Origin:      "built_in_compatible",
			InstanceID:  "bench-compatible",
			FactoryKind: "custom-openai-legacy-compatible",
			Enabled:     true,
		}}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		calls := 0
		countingProjector := func(c *config.Config) []diag.CompatibleBackendRow {
			calls++
			return projector(c)
		}
		snapshot, err := inventorySnapshotForOperatorWithProjector(
			context.Background(), cfg, nil, nil, countingProjector,
		)
		if err != nil {
			b.Fatalf("operator snapshot: %v", err)
		}
		if calls != 1 {
			b.Fatalf("compatible projection calls=%d, want 1", calls)
		}
		if len(snapshot.CompatibleBackends) != 1 || len(snapshot.InstanceDiagnostics) != 1 {
			b.Fatalf("snapshot lost shared compatible projection: compatible=%d diagnostics=%d", len(snapshot.CompatibleBackends), len(snapshot.InstanceDiagnostics))
		}
	}
}
