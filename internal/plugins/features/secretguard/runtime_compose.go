package secretguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// RuntimeConfig is the decoded secrets-guard feature config used by runtimebundle.
type RuntimeConfig struct {
	Enabled                   bool
	Action                    string
	AuditFailurePolicy        string
	AuditConfigVersion        string
	LocalAutoDiscoveryEnabled bool
	BetterLeaksEnabled        bool
	BetterLeaks               BetterLeaksPolicy
	ScanMaxBytes              int
	IncludePopularEnv         bool
	IncludeEnv                []string
	ExcludeEnv                []string
	MinSecretBytes            int
	PreserveKnownPrefixes     bool
	MaskByte                  byte
}

// DetectorPolicy is the immutable detector posture resolved for one runtime
// generation. It contains concrete values; request processing never consults
// the raw YAML configuration.
type DetectorPolicy struct {
	LocalAutoDiscoveryEnabled bool
	BetterLeaks               BetterLeaksPolicy
}

// BetterLeaksPolicy is the bounded, feature-private detector tuning policy.
type BetterLeaksPolicy struct {
	Enabled           bool
	MinimumConfidence string
	MaxDecodeDepth    int
	Workers           int
	DisableRules      []string
	IsolateRules      []string
	MaxFindings       int
}

// ComposeRuntimeConfig decodes enabled secrets-guard feature YAML.
func ComposeRuntimeConfig(accessMode string, regs []aipsdk.Registration) (RuntimeConfig, error) {
	multiUser := strings.TrimSpace(accessMode) == "multi_user"
	out := RuntimeConfig{}
	matches, err := EnabledRegistrations(regs)
	if err != nil {
		return RuntimeConfig{}, err
	}
	if len(matches) == 0 {
		return out, nil
	}
	decoded, err := DecodeConfig(matches[0].Config.Node)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("runtimebundle: secrets-guard config: %w", err)
	}
	if err := ValidateAccessMode(decoded, multiUser, matches[0].Config.Node); err != nil {
		return RuntimeConfig{}, fmt.Errorf("runtimebundle: secrets-guard config: %w", err)
	}
	policy, err := ResolveDetectorPolicy(accessMode, decoded)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("runtimebundle: secrets-guard config: %w", err)
	}
	out.Enabled = true
	out.Action = decoded.Action
	out.AuditFailurePolicy = decoded.AuditFailurePolicy
	out.ScanMaxBytes = decoded.ScanMaxBytes
	out.IncludePopularEnv, out.IncludeEnv, out.ExcludeEnv, out.MinSecretBytes, _, out.MaskByte, out.PreserveKnownPrefixes = CompositionOptions(decoded)
	out.LocalAutoDiscoveryEnabled = policy.LocalAutoDiscoveryEnabled
	out.BetterLeaksEnabled = policy.BetterLeaks.Enabled
	out.BetterLeaks = policy.BetterLeaks
	out.AuditConfigVersion = runtimeConfigVersion(out)
	return out, nil
}

// ResolveDetectorPolicy applies access-mode defaults and validates bounded
// detector settings before a candidate can be composed.
func ResolveDetectorPolicy(accessMode string, cfg Config) (DetectorPolicy, error) {
	mode := strings.TrimSpace(accessMode)
	if mode == "" {
		mode = "single_user"
	}
	if mode != "single_user" && mode != "multi_user" {
		return DetectorPolicy{}, fmt.Errorf("%s: unsupported access mode %q", ID, mode)
	}

	normalized, err := normalizeBetterLeaksConfig(cfg.BetterLeaks)
	if err != nil {
		return DetectorPolicy{}, err
	}
	localEnabled := mode == "single_user"
	if cfg.AutoDiscoveredLocalKeys.Enabled != nil {
		localEnabled = *cfg.AutoDiscoveredLocalKeys.Enabled
	}
	if mode == "multi_user" && localEnabled {
		return DetectorPolicy{}, fmt.Errorf("%s: auto_discovered_local_keys.enabled cannot be true in multi_user mode", ID)
	}

	confidence := normalized.MinimumConfidence
	if confidence == "" {
		confidence = DefaultBetterLeaksConfidence
	}
	maxDecodeDepth := DefaultBetterLeaksDecodeDepth
	if normalized.MaxDecodeDepth != nil {
		maxDecodeDepth = *normalized.MaxDecodeDepth
	}
	workers := 0
	if normalized.Workers != nil {
		workers = *normalized.Workers
	}
	if workers == 0 {
		workers = max(min(runtime.GOMAXPROCS(0), 4), 1)
	}

	return DetectorPolicy{
		LocalAutoDiscoveryEnabled: localEnabled,
		BetterLeaks: BetterLeaksPolicy{
			Enabled:           normalized.Enabled == nil || *normalized.Enabled,
			MinimumConfidence: confidence,
			MaxDecodeDepth:    maxDecodeDepth,
			Workers:           workers,
			DisableRules:      append([]string(nil), normalized.DisableRules...),
			IsolateRules:      append([]string(nil), normalized.IsolateRules...),
			MaxFindings:       DefaultBetterLeaksMaxFindings,
		},
	}, nil
}

// EnabledRegistrations returns the enabled secrets-guard feature registrations in config order.
// It rejects more than one enabled secrets-guard registration so startup cannot silently pick an
// arbitrary instance.
func EnabledRegistrations(regs []aipsdk.Registration) ([]aipsdk.Registration, error) {
	out := make([]aipsdk.Registration, 0, len(regs))
	for _, r := range regs {
		if r.Kind != aipsdk.PluginKindFeature || !r.Enabled || !isSecretsGuardRegistration(r) {
			continue
		}
		out = append(out, r)
		if len(out) > 1 {
			return nil, fmt.Errorf("runtimebundle: multiple enabled secrets-guard registrations")
		}
	}
	return out, nil
}

func isSecretsGuardRegistration(r aipsdk.Registration) bool {
	return strings.EqualFold(strings.TrimSpace(r.RegistryFactoryKey()), ID)
}

func runtimeConfigVersion(cfg RuntimeConfig) string {
	// Hash only the effective, canonical policy. This keeps equivalent selector
	// spellings on one generation identity and excludes YAML presentation noise.
	fingerprint := struct {
		Enabled                   bool
		Action                    string
		AuditFailurePolicy        string
		ScanMaxBytes              int
		LocalAutoDiscoveryEnabled bool
		BetterLeaks               BetterLeaksPolicy
		IncludePopularEnv         bool
		IncludeEnv                []string
		ExcludeEnv                []string
		MinSecretBytes            int
		PreserveKnownPrefixes     bool
		MaskByte                  byte
	}{
		Enabled:                   cfg.Enabled,
		Action:                    cfg.Action,
		AuditFailurePolicy:        cfg.AuditFailurePolicy,
		ScanMaxBytes:              cfg.ScanMaxBytes,
		LocalAutoDiscoveryEnabled: cfg.LocalAutoDiscoveryEnabled,
		BetterLeaks:               cfg.BetterLeaks,
		IncludePopularEnv:         cfg.IncludePopularEnv,
		IncludeEnv:                append([]string(nil), cfg.IncludeEnv...),
		ExcludeEnv:                append([]string(nil), cfg.ExcludeEnv...),
		MinSecretBytes:            cfg.MinSecretBytes,
		PreserveKnownPrefixes:     cfg.PreserveKnownPrefixes,
		MaskByte:                  cfg.MaskByte,
	}
	raw, err := json.Marshal(fingerprint)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sg-" + hex.EncodeToString(sum[:8])
}
