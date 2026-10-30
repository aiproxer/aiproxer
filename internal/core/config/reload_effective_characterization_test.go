package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

func TestEffectiveStreamRecovery_CLIEnvPrecedenceCharacterization(t *testing.T) {
	t.Parallel()
	enabled := true
	cfg := &config.Config{
		StreamRecovery: config.StreamRecoveryConfig{
			AutoResume: config.AutoResumeConfig{
				Enabled:     &enabled,
				IdleTimeout: "30s",
			},
		},
	}
	cliOff := false
	envOn := true
	eff, err := config.EffectiveStreamRecoveryAutoResume(cfg, config.StreamRecoveryOverrides{
		EnvEnabled:     &envOn,
		CLIEnabled:     &cliOff,
		EnvIdleTimeout: 20 * time.Second,
		CLIIdleTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Enabled {
		t.Fatal("CLI must win over env/config for enabled")
	}
	if eff.IdleTimeout != 10*time.Second {
		t.Fatalf("CLI idle timeout must win: got %s", eff.IdleTimeout)
	}
}

func TestEffectiveAliasValidationCharacterization(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{{Pattern: `(`, Replacement: "stub:x"}},
	}
	err := routing.ValidateModelAliasesConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "pattern") {
		t.Fatalf("want pattern error, got %v", err)
	}
}
