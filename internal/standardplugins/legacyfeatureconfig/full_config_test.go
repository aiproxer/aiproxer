package legacyfeatureconfig_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/standardplugins/legacyfeatureconfig"
)

const baseValidConfig = `
server:
  address: "127.0.0.1:0"
logging:
  level: "info"
`

func TestLoadEffective_LegacyInterleavedNormalized(t *testing.T) {
	t.Parallel()

	rawYAML := baseValidConfig + `
plugins:
  backends:
    - id: "test-backend"
      enabled: true
interleaved:
  enabled: true
  stream_to_client: visible
  regular_turns_remaining: 3
  max_memo_bytes: 8192
`

	eff, err := config.LoadEffective(context.Background(), []byte(rawYAML), config.LoadEffectiveOptions{
		NormalizeYAML: legacyfeatureconfig.NormalizeYAML,
	})
	if err != nil {
		t.Fatalf("LoadEffective failed: %v", err)
	}

	// Verify synthesized plugins.features entry
	var foundInterleaved bool
	for _, feat := range eff.Config.Plugins.Features {
		if feat.ID == interleavedthinking.ID {
			foundInterleaved = true
			if !feat.Enabled {
				t.Error("expected synthesized interleaved-thinking feature to be enabled")
			}
			cfg, err := interleavedthinking.DecodeConfig(feat.Config)
			if err != nil {
				t.Fatalf("DecodeConfig on synthesized node failed: %v", err)
			}
			if cfg.StreamToClient != "visible" {
				t.Errorf("StreamToClient = %q, want visible", cfg.StreamToClient)
			}
			if cfg.RegularTurnsRemaining != 3 {
				t.Errorf("RegularTurnsRemaining = %d, want 3", cfg.RegularTurnsRemaining)
			}
			if cfg.MaxMemoBytes != 8192 {
				t.Errorf("MaxMemoBytes = %d, want 8192", cfg.MaxMemoBytes)
			}
			break
		}
	}
	if !foundInterleaved {
		t.Fatal("expected synthesized interleaved-thinking feature in Plugins.Features")
	}
}

func TestLoadEffective_LegacyKeepwarmNormalized(t *testing.T) {
	t.Parallel()

	rawYAML := baseValidConfig + `
plugins:
  backends:
    - id: "test-backend"
      enabled: true
prompt_cache:
  keepwarm:
    enabled: true
    max_idle_duration: "2h"
    renew_timeout: "5s"
    max_refreshes_per_idle_epoch: 4
`

	eff, err := config.LoadEffective(context.Background(), []byte(rawYAML), config.LoadEffectiveOptions{
		NormalizeYAML: legacyfeatureconfig.NormalizeYAML,
	})
	if err != nil {
		t.Fatalf("LoadEffective failed: %v", err)
	}

	var foundKeepwarm bool
	for _, feat := range eff.Config.Plugins.Features {
		if feat.ID == keepwarm.ID {
			foundKeepwarm = true
			if !feat.Enabled {
				t.Error("expected synthesized keepwarm feature to be enabled")
			}
			cfg, err := keepwarm.DecodeConfig(feat.Config)
			if err != nil {
				t.Fatalf("DecodeConfig on synthesized node failed: %v", err)
			}
			if cfg.MaxIdleDuration != 2*time.Hour {
				t.Errorf("MaxIdleDuration = %v, want 2h", cfg.MaxIdleDuration)
			}
			if cfg.RenewTimeout != 5*time.Second {
				t.Errorf("RenewTimeout = %v, want 5s", cfg.RenewTimeout)
			}
			if cfg.MaxRefreshesPerIdleEpoch != 4 {
				t.Errorf("MaxRefreshesPerIdleEpoch = %d, want 4", cfg.MaxRefreshesPerIdleEpoch)
			}
			break
		}
	}
	if !foundKeepwarm {
		t.Fatal("expected synthesized keepwarm feature in Plugins.Features")
	}
}

func TestLoadEffective_LegacyAndCanonicalInterleavedConflict(t *testing.T) {
	t.Parallel()

	rawYAML := baseValidConfig + `
interleaved:
  enabled: true
plugins:
  backends:
    - id: "test-backend"
      enabled: true
  features:
    - id: interleaved-thinking
      enabled: true
`

	_, err := config.LoadEffective(context.Background(), []byte(rawYAML), config.LoadEffectiveOptions{
		NormalizeYAML: legacyfeatureconfig.NormalizeYAML,
	})
	if err == nil {
		t.Fatal("expected conflict error when both legacy and canonical interleaved are configured")
	}
	if !strings.Contains(err.Error(), "both legacy top-level 'interleaved' and canonical 'plugins.features' entry") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadEffective_LegacyAndCanonicalKeepwarmConflict(t *testing.T) {
	t.Parallel()

	rawYAML := baseValidConfig + `
prompt_cache:
  keepwarm:
    enabled: true
plugins:
  backends:
    - id: "test-backend"
      enabled: true
  features:
    - id: keepwarm
      enabled: true
`

	_, err := config.LoadEffective(context.Background(), []byte(rawYAML), config.LoadEffectiveOptions{
		NormalizeYAML: legacyfeatureconfig.NormalizeYAML,
	})
	if err == nil {
		t.Fatal("expected conflict error when both legacy and canonical keepwarm are configured")
	}
	if !strings.Contains(err.Error(), "both legacy top-level 'prompt_cache.keepwarm' and canonical 'plugins.features' entry") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadEffective_WithoutNormalizeYAML_RejectsLegacyPromptCache(t *testing.T) {
	t.Parallel()

	rawYAML := baseValidConfig + `
plugins:
  backends:
    - id: "test-backend"
      enabled: true
prompt_cache:
  keepwarm:
    enabled: true
`

	_, err := config.LoadEffective(context.Background(), []byte(rawYAML), config.LoadEffectiveOptions{
		NormalizeYAML: nil, // Do not normalize
	})
	if err == nil {
		t.Fatal("expected strict decode error for prompt_cache when NormalizeYAML is omitted")
	}
	loadErr, ok := err.(*config.LoadError)
	if !ok {
		t.Fatalf("expected *config.LoadError, got %T: %v", err, err)
	}
	if loadErr.Category != config.CategoryUnknownCoreField {
		t.Fatalf("expected CategoryUnknownCoreField, got %q", loadErr.Category)
	}
}

func TestLoadEffective_WithoutNormalizeYAML_RejectsLegacyInterleavedExtraFields(t *testing.T) {
	t.Parallel()

	rawYAML := baseValidConfig + `
plugins:
  backends:
    - id: "test-backend"
      enabled: true
interleaved:
  enabled: true
  stream_to_client: visible
`

	_, err := config.LoadEffective(context.Background(), []byte(rawYAML), config.LoadEffectiveOptions{
		NormalizeYAML: nil, // Do not normalize
	})
	if err == nil {
		t.Fatal("expected strict decode error for stream_to_client in interleaved when NormalizeYAML is omitted")
	}
	loadErr, ok := err.(*config.LoadError)
	if !ok {
		t.Fatalf("expected *config.LoadError, got %T: %v", err, err)
	}
	if loadErr.Category != config.CategoryUnknownCoreField {
		t.Fatalf("expected CategoryUnknownCoreField, got %q", loadErr.Category)
	}
}
