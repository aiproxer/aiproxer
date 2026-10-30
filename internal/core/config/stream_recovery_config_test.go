package config_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestStreamRecoveryEffectiveAutoResumeDefaultsDisabled(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	eff, err := config.EffectiveStreamRecoveryAutoResume(cfg, config.StreamRecoveryOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Enabled {
		t.Fatal("auto-resume must be disabled by default")
	}
	if eff.IdleTimeout != 45*time.Second {
		t.Fatalf("IdleTimeout: got %s", eff.IdleTimeout)
	}
	if eff.GracePeriod != 3*time.Second {
		t.Fatalf("GracePeriod: got %s", eff.GracePeriod)
	}
	if eff.PostOutputPolicy != config.StreamRecoveryPostOutputFinishWithWarning {
		t.Fatalf("PostOutputPolicy: got %q", eff.PostOutputPolicy)
	}
	if eff.KeepaliveInterval != 12*time.Second {
		t.Fatalf("KeepaliveInterval: got %s", eff.KeepaliveInterval)
	}
}

func TestStreamRecoveryEffectiveAutoResumePrecedenceCLIEnvConfig(t *testing.T) {
	t.Parallel()
	configEnabled := true
	cfg := &config.Config{
		StreamRecovery: config.StreamRecoveryConfig{
			AutoResume: config.AutoResumeConfig{
				Enabled:     &configEnabled,
				IdleTimeout: "30s",
			},
		},
	}
	cliEnabled := false
	envEnabled := true
	cliIdle := 10 * time.Second
	envIdle := 20 * time.Second
	eff, err := config.EffectiveStreamRecoveryAutoResume(cfg, config.StreamRecoveryOverrides{
		EnvEnabled:          &envEnabled,
		CLIEnabled:          &cliEnabled,
		EnvIdleTimeout:      envIdle,
		CLIIdleTimeout:      cliIdle,
		EnvGracePeriod:      6 * time.Second,
		CLIPostOutputPolicy: config.StreamRecoveryPostOutputFail,
	})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Enabled {
		t.Fatal("CLI false must override env/config true")
	}
	if eff.IdleTimeout != cliIdle {
		t.Fatalf("CLI idle timeout should win: got %s", eff.IdleTimeout)
	}
	if eff.GracePeriod != 6*time.Second {
		t.Fatalf("env grace period should win over default: got %s", eff.GracePeriod)
	}
	if eff.PostOutputPolicy != config.StreamRecoveryPostOutputFail {
		t.Fatalf("CLI post-output policy should win: got %q", eff.PostOutputPolicy)
	}
}

func TestStreamRecoveryEnvOverridesConfigWhenCLIUnset(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		StreamRecovery: config.StreamRecoveryConfig{
			AutoResume: config.AutoResumeConfig{
				Enabled:     new(bool),
				IdleTimeout: "30s",
			},
		},
	}
	envEnabled := true
	eff, err := config.EffectiveStreamRecoveryAutoResume(cfg, config.StreamRecoveryOverrides{
		EnvEnabled:     &envEnabled,
		EnvIdleTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Enabled {
		t.Fatal("env true must override config false")
	}
	if eff.IdleTimeout != 20*time.Second {
		t.Fatalf("env idle timeout should win: got %s", eff.IdleTimeout)
	}
}

func TestStreamRecoveryInvalidDurationFailsValidation(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		StreamRecovery: config.StreamRecoveryConfig{
			AutoResume: config.AutoResumeConfig{
				IdleTimeout: "not-a-duration",
			},
		},
	}
	if _, err := config.EffectiveStreamRecoveryAutoResume(cfg, config.StreamRecoveryOverrides{}); err == nil {
		t.Fatal("expected invalid duration error")
	}
}

func TestStreamRecoveryKeepaliveIntervalFromYAML(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		StreamRecovery: config.StreamRecoveryConfig{
			AutoResume: config.AutoResumeConfig{KeepaliveInterval: "5s"},
		},
	}
	eff, err := config.EffectiveStreamRecoveryAutoResume(cfg, config.StreamRecoveryOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.KeepaliveInterval != 5*time.Second {
		t.Fatalf("KeepaliveInterval: got %s, want 5s", eff.KeepaliveInterval)
	}
}
