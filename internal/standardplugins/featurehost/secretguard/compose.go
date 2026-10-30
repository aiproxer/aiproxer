package secretguard

import (
	"fmt"
	"log/slog"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	featsecretguard "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// Environment is the composition-injected process environment port.
type Environment = engine.Environment

// SingleUserOptions configures single-user environment catalog loading.
type SingleUserOptions = engine.SingleUserOptions

// MatcherOptions controls redaction presentation for the composed static matcher.
type MatcherOptions = engine.MatcherOptions

// GenerationServices is the immutable feature-private capability carried by
// the frozen secret-guard execution plane.
type GenerationServices = featsecretguard.GenerationServices

// SecretGuardInputs carries single-user catalog / matcher composition overrides.
type SecretGuardInputs struct {
	SingleUser SingleUserOptions
}

// Input specifies explicit composition inputs for secret-guard runtime assembly.
// It avoids passing BuildOptions, ProcessServices, full config, or registry bags.
type Input struct {
	AccessMode    accessmode.Mode
	Registrations []aipsdk.Registration
	RuntimeConfig *featsecretguard.RuntimeConfig
	Guards        []sdk.Guard
	Environment   Environment
	Inputs        SecretGuardInputs
	SingleUser    SingleUserOptions
	// HostInputs carries bound secret-guard host inputs when a host binding was
	// supplied (nil when absent). Explicitly-set host fields take precedence
	// over YAML-decoded catalog options.
	HostInputs       *SecretGuardInputs
	DecisionObserver sdk.Observer
	Logger           *slog.Logger
}

// Output carries assembled runtime extension planes and diagnostic inventory.
type Output struct {
	Plane     extensions.SecretGuardPlane
	Inventory *diag.InventoryExtras
	Services  *featsecretguard.GenerationServices
}

// Compose assembles runtime secret-guard components from explicit typed inputs.
func Compose(in Input) (*Output, error) {
	mode := in.AccessMode
	if mode == "" {
		mode = accessmode.ModeSingleUser
	}
	normMode, err := accessmode.NormalizeMode(string(mode))
	if err != nil {
		return nil, fmt.Errorf("secretguardcompose: access mode: %w", err)
	}

	var engineMode engine.AccessMode
	var accessModeStr string
	switch normMode {
	case accessmode.ModeSingleUser:
		engineMode = engine.ModeSingleUser
		accessModeStr = string(accessmode.ModeSingleUser)
	case accessmode.ModeMultiUser:
		engineMode = engine.ModeMultiUser
		accessModeStr = string(accessmode.ModeMultiUser)
	default:
		return nil, fmt.Errorf("secretguardcompose: unsupported access mode %q", in.AccessMode)
	}

	var runtimeCfg featsecretguard.RuntimeConfig
	if in.RuntimeConfig != nil {
		runtimeCfg = *in.RuntimeConfig
	} else {
		var err error
		runtimeCfg, err = featsecretguard.ComposeRuntimeConfig(accessModeStr, in.Registrations)
		if err != nil {
			return nil, err
		}
	}
	if engineMode == engine.ModeMultiUser && runtimeCfg.LocalAutoDiscoveryEnabled {
		return nil, fmt.Errorf("runtimebundle: secrets-guard config: auto_discovered_local_keys.enabled cannot be true in multi_user mode")
	}

	inputs := in.Inputs.SingleUser
	if !inputs.MatcherConfigured && in.SingleUser.MatcherConfigured {
		inputs = in.SingleUser
	} else if in.SingleUser.IncludePopularEnv || len(in.SingleUser.IncludeEnv) > 0 || len(in.SingleUser.ExcludeEnv) > 0 || in.SingleUser.MinSecretBytes > 0 {
		inputs = in.SingleUser
	}
	if in.HostInputs != nil {
		runtimeCfg = applySecretGuardHostOverride(runtimeCfg, in.HostInputs.SingleUser)
	}
	singleUser := composeSingleUser(runtimeCfg, inputs)

	featureEnabled := runtimeCfg.Enabled
	sourceEnv := in.Environment
	if engineMode == engine.ModeSingleUser && featureEnabled && !runtimeCfg.LocalAutoDiscoveryEnabled {
		// Keep the exact source path available while preventing the local
		// environment port from being consulted when policy disables discovery.
		sourceEnv = nil
	}
	src, err := engine.ComposeSource(engineMode, featureEnabled, sourceEnv, singleUser)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: secret guard source: %w", err)
	}
	detectorPolicy := featsecretguard.DetectorPolicy{}
	if featureEnabled {
		detectorPolicy = featsecretguard.DetectorPolicy{
			LocalAutoDiscoveryEnabled: runtimeCfg.LocalAutoDiscoveryEnabled,
			BetterLeaks:               runtimeCfg.BetterLeaks,
		}
	}
	services, err := featsecretguard.BuildGenerationServices(detectorPolicy, src, singleUser.Matcher)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: betterleaks generation scanner: %w", err)
	}

	var guards []sdk.Guard
	if len(in.Guards) > 0 {
		guards = append([]sdk.Guard(nil), in.Guards...)
	}

	accessPolicy := sdk.AuditFailClosed
	if runtimeCfg.Enabled {
		accessPolicy = sdk.AuditFailurePolicy(runtimeCfg.AuditFailurePolicy)
	}

	observer := in.DecisionObserver
	if featureEnabled || len(guards) > 0 {
		if !sdk.IsNilObserver(observer) {
			observer = sdk.ChainObservers(accessPolicy, observer)
		} else {
			if in.Logger == nil {
				return nil, fmt.Errorf("runtimebundle: secrets-guard audit requires a non-nil logger")
			}
			slogObs, err := NewSlogObserver(in.Logger)
			if err != nil {
				return nil, fmt.Errorf("runtimebundle: secret guard audit: %w", err)
			}
			observer = sdk.ChainObservers(accessPolicy, slogObs)
		}
	}

	var inventory *diag.InventoryExtras
	if featureEnabled || len(guards) > 0 {
		posture := services.Posture()
		facts := posture.BetterLeaksFacts
		inventory = &diag.InventoryExtras{
			SecretGuardCatalogEntryCount:      src.EntryCount(),
			SecretGuardSourceCategories:       append([]string(nil), src.SourceCategories()...),
			SecretGuardAccessMode:             accessModeStr,
			SecretGuardAction:                 runtimeCfg.Action,
			SecretGuardLocalAutoDiscovery:     posture.LocalAutoDiscoveryEnabled,
			SecretGuardBetterLeaksEnabled:     posture.BetterLeaksEnabled,
			SecretGuardBetterLeaksVersion:     facts.Version,
			SecretGuardBetterLeaksConfigHash:  facts.ConfigHash,
			SecretGuardBetterLeaksRuleCount:   facts.ActiveRuleCount,
			SecretGuardBetterLeaksConfidence:  facts.MinimumConfidence,
			SecretGuardBetterLeaksDecodeDepth: facts.MaxDecodeDepth,
			SecretGuardBetterLeaksWorkers:     facts.Workers,
			SecretGuardDiscoveryDetectorCount: posture.DiscoveryDetectorCount,
		}
	}

	return &Output{
		Plane: extensions.SecretGuardPlane{
			Guards:             guards,
			MatcherResolver:    src.MatcherResolver(),
			DecisionObserver:   observer,
			AuditFailurePolicy: accessPolicy,
			AccessMode:         accessModeStr,
			ConfigVersion:      runtimeCfg.AuditConfigVersion,
		},
		Inventory: inventory,
		Services:  services,
	}, nil
}

// ComposeSingleUser merges YAML runtime config onto single-user options.
func ComposeSingleUser(runtimeCfg featsecretguard.RuntimeConfig, inputs SingleUserOptions) SingleUserOptions {
	return composeSingleUser(runtimeCfg, inputs)
}

// applySecretGuardHostOverride overlays explicitly host-supplied catalog fields
// onto the YAML-decoded runtime config. Only explicitly set host fields win:
// MinSecretBytes above zero, non-empty include/exclude lists, a true
// IncludePopularEnv, and a configured matcher. A present-but-empty host binding
// (such as the default env-only binding) changes nothing, so YAML composition
// is undisturbed unless the host explicitly set a field.
func applySecretGuardHostOverride(runtimeCfg featsecretguard.RuntimeConfig, host SingleUserOptions) featsecretguard.RuntimeConfig {
	if host.MinSecretBytes > 0 {
		runtimeCfg.MinSecretBytes = host.MinSecretBytes
	}
	if len(host.IncludeEnv) > 0 {
		runtimeCfg.IncludeEnv = append([]string(nil), host.IncludeEnv...)
	}
	if len(host.ExcludeEnv) > 0 {
		runtimeCfg.ExcludeEnv = append([]string(nil), host.ExcludeEnv...)
	}
	if host.IncludePopularEnv {
		runtimeCfg.IncludePopularEnv = true
	}
	if host.MatcherConfigured {
		runtimeCfg.PreserveKnownPrefixes = host.Matcher.PreserveKnownPrefixes
		runtimeCfg.MaskByte = host.Matcher.MaskByte
	}
	return runtimeCfg
}

// composeSingleUser merges YAML runtime config onto composition-seam inputs.
// YAML wins for catalog fields when the feature is enabled. Matcher options from
// inputs win when MatcherConfigured is already set (test/composition override);
// otherwise YAML stamps matcher options.
func composeSingleUser(runtimeCfg featsecretguard.RuntimeConfig, inputs SingleUserOptions) engine.SingleUserOptions {
	out := inputs
	out.IncludeEnv = append([]string(nil), out.IncludeEnv...)
	out.ExcludeEnv = append([]string(nil), out.ExcludeEnv...)
	if !runtimeCfg.Enabled {
		return out
	}
	matcherOverride := out.MatcherConfigured
	matcher := out.Matcher
	out.IncludePopularEnv = runtimeCfg.IncludePopularEnv
	out.IncludeEnv = append([]string(nil), runtimeCfg.IncludeEnv...)
	out.ExcludeEnv = append([]string(nil), runtimeCfg.ExcludeEnv...)
	out.MinSecretBytes = runtimeCfg.MinSecretBytes
	if matcherOverride {
		out.Matcher = matcher
		out.MatcherConfigured = true
		return out
	}
	out.Matcher = engine.MatcherOptions{
		PreserveKnownPrefixes: runtimeCfg.PreserveKnownPrefixes,
		MaskByte:              runtimeCfg.MaskByte,
	}
	out.MatcherConfigured = true
	return out
}

// ValidateRegistrations validates feature registration uniqueness for secrets-guard.
func ValidateRegistrations(regs []aipsdk.Registration) error {
	_, err := featsecretguard.EnabledRegistrations(regs)
	if err != nil {
		return fmt.Errorf("runtimebundle: secrets-guard composition: %w", err)
	}
	return nil
}

// EnabledRegistrations returns enabled secrets-guard feature registrations in config order.
func EnabledRegistrations(regs []aipsdk.Registration) ([]aipsdk.Registration, error) {
	return featsecretguard.EnabledRegistrations(regs)
}
