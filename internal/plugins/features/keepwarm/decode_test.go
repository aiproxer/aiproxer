package keepwarm_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"gopkg.in/yaml.v3"
)

func TestDecodeConfig_Defaults(t *testing.T) {
	t.Parallel()

	// Zero / empty node should return DefaultConfig()
	var emptyNode yaml.Node
	cfg, err := keepwarm.DecodeConfig(emptyNode)
	if err != nil {
		t.Fatalf("DecodeConfig(empty): %v", err)
	}
	expected := keepwarm.DefaultConfig()
	if !reflect.DeepEqual(cfg, expected) {
		t.Fatalf("expected defaults:\n got: %+v\n want: %+v", cfg, expected)
	}

	// Explicit null node
	var nullNode yaml.Node
	if err := yaml.Unmarshal([]byte("null"), &nullNode); err != nil {
		t.Fatalf("unmarshal null: %v", err)
	}
	cfgNull, err := keepwarm.DecodeConfig(nullNode)
	if err != nil {
		t.Fatalf("DecodeConfig(null): %v", err)
	}
	if !reflect.DeepEqual(cfgNull, expected) {
		t.Fatalf("expected defaults for null node:\n got: %+v\n want: %+v", cfgNull, expected)
	}
}

func TestDecodeConfig_FullCustomParity(t *testing.T) {
	t.Parallel()

	rawYAML := `
enabled: true
max_refreshes_per_idle_epoch: 10
max_idle_duration: 2h
max_active_targets: 512
max_concurrent_renewals: 8
renew_timeout: 30s
continue_after_cold_recreate: true
max_cold_recreates_per_idle_epoch: 3
heuristic_overrides:
  - backend_instance: openai-prod
    canonical_model: gpt-4o
    interval: 45m
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(rawYAML), &node); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cfg, err := keepwarm.DecodeConfig(node)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}

	if !cfg.Enabled {
		t.Error("expected Enabled: true")
	}
	if cfg.MaxRefreshesPerIdleEpoch != 10 {
		t.Errorf("MaxRefreshesPerIdleEpoch = %d, want 10", cfg.MaxRefreshesPerIdleEpoch)
	}
	if cfg.MaxIdleDuration != 2*time.Hour {
		t.Errorf("MaxIdleDuration = %v, want 2h", cfg.MaxIdleDuration)
	}
	if cfg.MaxActiveTargets != 512 {
		t.Errorf("MaxActiveTargets = %d, want 512", cfg.MaxActiveTargets)
	}
	if cfg.MaxConcurrentRenewals != 8 {
		t.Errorf("MaxConcurrentRenewals = %d, want 8", cfg.MaxConcurrentRenewals)
	}
	if cfg.RenewTimeout != 30*time.Second {
		t.Errorf("RenewTimeout = %v, want 30s", cfg.RenewTimeout)
	}
	if !cfg.ContinueAfterColdRecreate {
		t.Error("expected ContinueAfterColdRecreate: true")
	}
	if cfg.MaxColdRecreatesPerIdleEpoch != 3 {
		t.Errorf("MaxColdRecreatesPerIdleEpoch = %d, want 3", cfg.MaxColdRecreatesPerIdleEpoch)
	}
	if len(cfg.HeuristicOverrides) != 1 {
		t.Fatalf("expected 1 heuristic override, got %d", len(cfg.HeuristicOverrides))
	}
	h := cfg.HeuristicOverrides[0]
	if h.BackendInstance != "openai-prod" || h.CanonicalModel != "gpt-4o" || h.Interval != 45*time.Minute {
		t.Errorf("unexpected heuristic override: %+v", h)
	}
}

func TestDecodeConfig_ValidationErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{
			name: "invalid duration format",
			raw: `
renew_timeout: "not-a-duration"
`,
			wantErr: true,
		},
		{
			name: "zero max refreshes",
			raw: `
max_refreshes_per_idle_epoch: 0
`,
			wantErr: true,
		},
		{
			name: "contradictory cold recreate",
			raw: `
continue_after_cold_recreate: false
max_cold_recreates_per_idle_epoch: 5
`,
			wantErr: true,
		},
		{
			name: "duplicate heuristic override",
			raw: `
heuristic_overrides:
  - backend_instance: b1
    canonical_model: m1
    interval: 10m
  - backend_instance: b1
    canonical_model: m1
    interval: 20m
`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var n yaml.Node
			if err := yaml.Unmarshal([]byte(tc.raw), &n); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			_, err := keepwarm.DecodeConfig(n)
			if (err != nil) != tc.wantErr {
				t.Fatalf("DecodeConfig err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestDecodeConfig_UnknownKeysRejected(t *testing.T) {
	t.Parallel()

	raw := `
enabled: true
unknown_setting: 123
`
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	_, err := keepwarm.DecodeConfig(n)
	if err == nil {
		t.Fatal("expected error for unknown config key in keepwarm configuration")
	}
	if !errors.Is(err, keepwarm.ErrInvalidConfig) {
		t.Fatalf("expected error wrapping ErrInvalidConfig, got: %v", err)
	}
}
