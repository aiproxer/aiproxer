package config_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// TestEffectiveSeamDigestsEnabled proves the executor gate needs both
// switches: the explicit digest flag and a running metrics detector. A flag
// without metrics must not pay the per-request hash pass.
func TestEffectiveSeamDigestsEnabled(t *testing.T) {
	t.Parallel()
	newCfg := func(metrics, seam bool) *config.Config {
		cfg := &config.Config{}
		cfg.Observability.Metrics.Enabled = metrics
		cfg.Observability.Metrics.PromptCacheCollapse.SeamDigestsEnabled = seam
		return cfg
	}
	for _, tc := range []struct {
		name    string
		metrics bool
		seam    bool
		want    bool
	}{
		{"both off", false, false, false},
		{"seam without metrics", false, true, false},
		{"metrics without seam", true, false, false},
		{"both on", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := config.EffectiveSeamDigestsEnabled(newCfg(tc.metrics, tc.seam)); got != tc.want {
				t.Fatalf("enabled=%v want %v", got, tc.want)
			}
		})
	}
	if config.EffectiveSeamDigestsEnabled(nil) {
		t.Fatal("nil config must disable the feed")
	}
	bad := &config.Config{}
	bad.Observability.Metrics.Enabled = true
	zero := 0
	bad.Observability.Metrics.PromptCacheCollapse.MinPromptTokens = &zero
	if config.EffectiveSeamDigestsEnabled(bad) {
		t.Fatal("invalid collapse block must disable the feed")
	}
}
