package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type StreamRecoveryPostOutputPolicy string

type StreamRecoveryConfig struct {
	AutoResume AutoResumeConfig `yaml:"auto_resume"`
}

type AutoResumeConfig struct {
	Enabled           *bool  `yaml:"enabled"`
	IdleTimeout       string `yaml:"idle_timeout"`
	GracePeriod       string `yaml:"grace_period"`
	PostOutputPolicy  string `yaml:"post_output_policy"`
	EmitWarning       *bool  `yaml:"emit_warning"`
	KeepaliveInterval string `yaml:"keepalive_interval"`
}

const (
	StreamRecoveryPostOutputFinishWithWarning StreamRecoveryPostOutputPolicy = "finish_with_warning"
	StreamRecoveryPostOutputFail              StreamRecoveryPostOutputPolicy = "fail"
)

const (
	defaultStreamRecoveryIdleTimeout       = 45 * time.Second
	defaultStreamRecoveryGracePeriod       = 3 * time.Second
	defaultStreamRecoveryKeepaliveInterval = 12 * time.Second
)

type EffectiveAutoResumeConfig struct {
	Enabled           bool
	IdleTimeout       time.Duration
	GracePeriod       time.Duration
	PostOutputPolicy  StreamRecoveryPostOutputPolicy
	EmitWarning       bool
	KeepaliveInterval time.Duration
}

type StreamRecoveryOverrides struct {
	EnvEnabled          *bool
	CLIEnabled          *bool
	EnvIdleTimeout      time.Duration
	CLIIdleTimeout      time.Duration
	EnvGracePeriod      time.Duration
	CLIGracePeriod      time.Duration
	EnvPostOutputPolicy StreamRecoveryPostOutputPolicy
	CLIPostOutputPolicy StreamRecoveryPostOutputPolicy
	EnvEmitWarning      *bool
	CLIEmitWarning      *bool
}

func StreamRecoveryOverridesFromEnv() (StreamRecoveryOverrides, error) {
	var out StreamRecoveryOverrides
	if raw, ok := os.LookupEnv("AIP_AUTO_RESUME"); ok && strings.TrimSpace(raw) != "" {
		v, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return out, fmt.Errorf("aip auto resume: %w", err)
		}
		out.EnvEnabled = &v
	}
	if raw := strings.TrimSpace(os.Getenv("AIP_AUTO_RESUME_IDLE_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return out, fmt.Errorf("aip auto resume idle timeout: invalid positive duration %q", raw)
		}
		out.EnvIdleTimeout = d
	}
	if raw := strings.TrimSpace(os.Getenv("AIP_AUTO_RESUME_GRACE_PERIOD")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return out, fmt.Errorf("aip auto resume grace period: invalid positive duration %q", raw)
		}
		out.EnvGracePeriod = d
	}
	if raw := strings.TrimSpace(os.Getenv("AIP_AUTO_RESUME_POST_OUTPUT_POLICY")); raw != "" {
		pol, err := parseStreamRecoveryPostOutputPolicy(raw)
		if err != nil {
			return out, fmt.Errorf("aip auto resume post output policy: %w", err)
		}
		out.EnvPostOutputPolicy = pol
	}
	return out, nil
}

func EffectiveStreamRecoveryAutoResume(cfg *Config, overrides StreamRecoveryOverrides) (EffectiveAutoResumeConfig, error) {
	eff := EffectiveAutoResumeConfig{
		IdleTimeout:       defaultStreamRecoveryIdleTimeout,
		GracePeriod:       defaultStreamRecoveryGracePeriod,
		PostOutputPolicy:  StreamRecoveryPostOutputFinishWithWarning,
		EmitWarning:       true,
		KeepaliveInterval: defaultStreamRecoveryKeepaliveInterval,
	}
	if cfg != nil {
		ar := cfg.StreamRecovery.AutoResume
		if ar.Enabled != nil {
			eff.Enabled = *ar.Enabled
		}
		if strings.TrimSpace(ar.IdleTimeout) != "" {
			d, err := parsePositiveDuration("stream_recovery.auto_resume.idle_timeout", ar.IdleTimeout)
			if err != nil {
				return eff, err
			}
			eff.IdleTimeout = d
		}
		if strings.TrimSpace(ar.GracePeriod) != "" {
			d, err := parsePositiveDuration("stream_recovery.auto_resume.grace_period", ar.GracePeriod)
			if err != nil {
				return eff, err
			}
			eff.GracePeriod = d
		}
		if strings.TrimSpace(ar.PostOutputPolicy) != "" {
			pol, err := parseStreamRecoveryPostOutputPolicy(ar.PostOutputPolicy)
			if err != nil {
				return eff, fmt.Errorf("stream_recovery.auto_resume.post_output_policy: %w", err)
			}
			eff.PostOutputPolicy = pol
		}
		if ar.EmitWarning != nil {
			eff.EmitWarning = *ar.EmitWarning
		}
		if strings.TrimSpace(ar.KeepaliveInterval) != "" {
			d, err := parsePositiveDuration("stream_recovery.auto_resume.keepalive_interval", ar.KeepaliveInterval)
			if err != nil {
				return eff, err
			}
			eff.KeepaliveInterval = d
		}
	}
	if overrides.EnvEnabled != nil {
		eff.Enabled = *overrides.EnvEnabled
	}
	if overrides.EnvIdleTimeout > 0 {
		eff.IdleTimeout = overrides.EnvIdleTimeout
	}
	if overrides.EnvGracePeriod > 0 {
		eff.GracePeriod = overrides.EnvGracePeriod
	}
	if overrides.EnvPostOutputPolicy != "" {
		eff.PostOutputPolicy = overrides.EnvPostOutputPolicy
	}
	if overrides.EnvEmitWarning != nil {
		eff.EmitWarning = *overrides.EnvEmitWarning
	}
	if overrides.CLIEnabled != nil {
		eff.Enabled = *overrides.CLIEnabled
	}
	if overrides.CLIIdleTimeout > 0 {
		eff.IdleTimeout = overrides.CLIIdleTimeout
	}
	if overrides.CLIGracePeriod > 0 {
		eff.GracePeriod = overrides.CLIGracePeriod
	}
	if overrides.CLIPostOutputPolicy != "" {
		eff.PostOutputPolicy = overrides.CLIPostOutputPolicy
	}
	if overrides.CLIEmitWarning != nil {
		eff.EmitWarning = *overrides.CLIEmitWarning
	}
	return eff, nil
}

func parsePositiveDuration(name, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s: invalid positive duration %q", name, raw)
	}
	return d, nil
}

func parseStreamRecoveryPostOutputPolicy(raw string) (StreamRecoveryPostOutputPolicy, error) {
	switch pol := StreamRecoveryPostOutputPolicy(strings.ToLower(strings.TrimSpace(raw))); pol {
	case StreamRecoveryPostOutputFinishWithWarning, StreamRecoveryPostOutputFail:
		return pol, nil
	default:
		return "", fmt.Errorf("want finish_with_warning or fail, got %q", raw)
	}
}
