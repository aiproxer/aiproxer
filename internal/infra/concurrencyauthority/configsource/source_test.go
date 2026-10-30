package configsource_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/configsource"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestSourceSnapshot(t *testing.T) {
	t.Parallel()
	src, err := configsource.New(config.ConcurrencyAuthorityConfig{
		Enabled:         true,
		SnapshotVersion: "conc-v2",
		Rules: []config.ConcurrencyAuthorityRuleConfig{{
			ID:                "max-active",
			Mode:              "strict",
			MaxActiveRequests: 5,
			Match: config.AccountingAuthorityDimensionsConfig{
				Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("p1")},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Rules) != 1 || snap.Rules[0].Limit != 5 {
		t.Fatalf("snap=%+v", snap)
	}
	if snap.ID != "concurrency" || snap.Version != "conc-v2" || snap.State != "ready" {
		t.Fatalf("versioned snap=%+v", snap)
	}
}
