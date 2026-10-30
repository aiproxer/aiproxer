package secretguard_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	sgcompose "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"gopkg.in/yaml.v3"
)

type panicEnv struct{ calls int }

func (p *panicEnv) Lookup(string) (string, bool) {
	p.calls++
	panic("unexpected environment lookup")
}

func (p *panicEnv) Snapshot() []string {
	p.calls++
	panic("unexpected environment snapshot")
}

type mapEnv struct {
	vals map[string]string
}

func (m *mapEnv) Lookup(name string) (string, bool) {
	v, ok := m.vals[name]
	return v, ok
}

func (m *mapEnv) Snapshot() []string {
	out := make([]string, 0, len(m.vals))
	for k, v := range m.vals {
		out = append(out, k+"="+v)
	}
	return out
}

type stubGuard struct {
	id string
}

func (g stubGuard) ID() string                   { return g.id }
func (g stubGuard) Order() int                   { return 1 }
func (g stubGuard) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (g stubGuard) Evaluate(context.Context, *aipapi.Call, sdk.Meta, sdk.Services) (sdk.Decision, error) {
	return sdk.Decision{Outcome: sdk.OutcomePass}, nil
}

type customObserver struct {
	called bool
}

func (o *customObserver) OnSecretDecision(context.Context, sdk.DecisionEvent) error {
	o.called = true
	return nil
}

func mustYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSecretGuardCompose_UnknownAccessModeFailsClosed(t *testing.T) {
	t.Parallel()

	unknownModes := []accessmode.Mode{
		"invalid",
		"single-user",
		"cluster",
		"root",
		"unknown",
	}

	for _, mode := range unknownModes {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			_, err := sgcompose.Compose(sgcompose.Input{
				AccessMode: mode,
				Logger:     discardLogger(),
			})
			if err == nil {
				t.Fatalf("expected error for unknown access mode %q, got nil", mode)
			}
			if !strings.Contains(err.Error(), "access mode") {
				t.Fatalf("expected error to mention access mode, got: %v", err)
			}
		})
	}
}

func TestSecretGuardCompose_MultiUserZeroEnvironmentCalls(t *testing.T) {
	t.Parallel()
	env := &panicEnv{}
	raw := mustYAMLNode(t, "action: block\n")
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: raw},
	}}

	out, err := sgcompose.Compose(sgcompose.Input{
		AccessMode:    accessmode.ModeMultiUser,
		Registrations: regs,
		Environment:   env,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.calls != 0 {
		t.Fatalf("expected 0 env calls in multi_user mode, got %d", env.calls)
	}
	if out.Plane.AccessMode != "multi_user" {
		t.Fatalf("plane access mode: got %q, want multi_user", out.Plane.AccessMode)
	}
}

// TestSecretGuardCompose_MultiUserExplicitLocalDiscoveryRejectedBeforeEnvironment
// is the RED characterization for the access-mode-safe local discovery switch.
// Validation must reject the explicit request before any environment source is
// consulted, even though multi-user composition otherwise uses the request
// credential matcher only.
func TestSecretGuardCompose_MultiUserExplicitLocalDiscoveryRejectedBeforeEnvironment(t *testing.T) {
	t.Parallel()
	env := &panicEnv{}
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "detector-policy",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config: aipsdk.ConfigPayload{Node: mustYAMLNode(t, `
action: block
auto_discovered_local_keys:
  enabled: true
`)},
	}}

	out, err := sgcompose.Compose(sgcompose.Input{
		AccessMode:    accessmode.ModeMultiUser,
		Registrations: regs,
		Environment:   env,
		Logger:        discardLogger(),
	})
	if err == nil || out != nil {
		t.Fatalf("expected explicit multi_user local discovery rejection, out=%#v err=%v", out, err)
	}
	if !strings.Contains(err.Error(), "auto_discovered_local_keys") {
		t.Fatalf("rejection must identify the invalid detector switch: %v", err)
	}
	if env.calls != 0 {
		t.Fatalf("invalid multi_user detector configuration read environment %d times", env.calls)
	}
}

func TestSecretGuardCompose_DisabledZeroEnvironmentCalls(t *testing.T) {
	t.Parallel()
	env := &panicEnv{}
	out, err := sgcompose.Compose(sgcompose.Input{
		AccessMode:  accessmode.ModeSingleUser,
		Environment: env,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.calls != 0 {
		t.Fatalf("expected 0 env calls when feature disabled, got %d", env.calls)
	}
	if out.Inventory != nil {
		t.Fatalf("expected nil inventory when feature disabled and no guards, got %#v", out.Inventory)
	}
}

func TestSecretGuardCompose_SingleUserReadsEnvironment(t *testing.T) {
	t.Parallel()
	secret := "super-secret-password-1234"
	env := &mapEnv{vals: map[string]string{
		"MY_APP_SECRET": secret,
	}}
	raw := mustYAMLNode(t, `
action: redact
min_secret_bytes: 8
single_user:
  include_popular_env: false
  include_env: [MY_APP_SECRET]
`)
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: raw},
	}}

	out, err := sgcompose.Compose(sgcompose.Input{
		AccessMode:    accessmode.ModeSingleUser,
		Registrations: regs,
		Environment:   env,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Plane.MatcherResolver == nil {
		t.Fatal("expected non-nil MatcherResolver")
	}
	matcher, err := out.Plane.MatcherResolver.Resolve(t.Context())
	if err != nil || matcher == nil {
		t.Fatalf("failed to resolve matcher: %v", err)
	}
	findings, err := matcher.ScanString(t.Context(), "key="+secret)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected finding for secret in single_user mode")
	}
	if out.Inventory == nil || out.Inventory.SecretGuardCatalogEntryCount < 1 {
		t.Fatalf("expected catalog entries in inventory, got %#v", out.Inventory)
	}
}

func TestSecretGuardCompose_NilLoggerFailsClosedWhenAuditRequired(t *testing.T) {
	t.Parallel()

	t.Run("feature_enabled", func(t *testing.T) {
		t.Parallel()
		raw := mustYAMLNode(t, "action: log\n")
		regs := []aipsdk.Registration{{
			Kind:        aipsdk.PluginKindFeature,
			ID:          "secrets-guard",
			FactoryKind: "secrets-guard",
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: raw},
		}}

		_, err := sgcompose.Compose(sgcompose.Input{
			AccessMode:    accessmode.ModeSingleUser,
			Registrations: regs,
			Logger:        nil,
		})
		if err == nil {
			t.Fatal("expected error with nil logger, got nil")
		}
		if err.Error() != "runtimebundle: secrets-guard audit requires a non-nil logger" {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("injected_guards", func(t *testing.T) {
		t.Parallel()
		_, err := sgcompose.Compose(sgcompose.Input{
			AccessMode: accessmode.ModeSingleUser,
			Guards:     []sdk.Guard{stubGuard{id: "custom"}},
			Logger:     nil,
		})
		if err == nil {
			t.Fatal("expected error with nil logger, got nil")
		}
		if err.Error() != "runtimebundle: secrets-guard audit requires a non-nil logger" {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestSecretGuardCompose_InjectedGuardsWithExplicitObserverAndNilLoggerSucceeds(t *testing.T) {
	t.Parallel()
	obs := &customObserver{}
	out, err := sgcompose.Compose(sgcompose.Input{
		AccessMode:       accessmode.ModeSingleUser,
		Guards:           []sdk.Guard{stubGuard{id: "custom"}},
		DecisionObserver: obs,
		Logger:           nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Plane.DecisionObserver == nil {
		t.Fatal("expected non-nil DecisionObserver")
	}
}

func TestSecretGuardCompose_SingleUserHostOverrides(t *testing.T) {
	t.Parallel()
	runtimeCfg := secretguard.RuntimeConfig{
		Enabled:               true,
		PreserveKnownPrefixes: true,
		MaskByte:              '*',
		MinSecretBytes:        8,
	}
	inputs := sgcompose.SecretGuardInputs{
		SingleUser: sgcompose.SingleUserOptions{
			Matcher:           sgcompose.MatcherOptions{PreserveKnownPrefixes: false, MaskByte: 'X'},
			MatcherConfigured: true,
		},
	}
	out, err := sgcompose.Compose(sgcompose.Input{
		AccessMode:    accessmode.ModeSingleUser,
		RuntimeConfig: &runtimeCfg,
		Inputs:        inputs,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil output")
	}
}

func TestSecretGuardCompose_ValidateRegistrations_RejectsDuplicates(t *testing.T) {
	t.Parallel()
	regs := []aipsdk.Registration{
		{
			Kind:        aipsdk.PluginKindFeature,
			ID:          "secrets-guard-1",
			FactoryKind: "secrets-guard",
			Enabled:     true,
		},
		{
			Kind:        aipsdk.PluginKindFeature,
			ID:          "secrets-guard-2",
			FactoryKind: "secrets-guard",
			Enabled:     true,
		},
	}
	err := sgcompose.ValidateRegistrations(regs)
	if err == nil {
		t.Fatal("expected error on duplicate enabled secrets-guard, got nil")
	}
	if !strings.Contains(err.Error(), "multiple enabled secrets-guard registrations") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSecretGuardCompose_EnabledRegistrations(t *testing.T) {
	t.Parallel()
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
	}}
	matches, err := sgcompose.EnabledRegistrations(regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches=%+v", matches)
	}
	regs = []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "other-feature",
		Enabled:     true,
	}}
	matches, err = sgcompose.EnabledRegistrations(regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("unrelated factory must not match secrets-guard, got %+v", matches)
	}
}

func TestSecretGuardCompose_ComposeSingleUser_PreservationRules(t *testing.T) {
	t.Parallel()

	t.Run("feature_disabled_preserves_inputs_directly", func(t *testing.T) {
		t.Parallel()
		inputs := sgcompose.SingleUserOptions{
			IncludePopularEnv: true,
			IncludeEnv:        []string{"INPUT_A", "INPUT_B"},
			ExcludeEnv:        []string{"EXCLUDE_A"},
			MinSecretBytes:    12,
		}
		runtimeCfg := secretguard.RuntimeConfig{Enabled: false}
		out := sgcompose.ComposeSingleUser(runtimeCfg, inputs)
		if !out.IncludePopularEnv {
			t.Fatal("IncludePopularEnv want true")
		}
		if len(out.IncludeEnv) != 2 || out.IncludeEnv[0] != "INPUT_A" || out.IncludeEnv[1] != "INPUT_B" {
			t.Fatalf("IncludeEnv: got %#v", out.IncludeEnv)
		}
		if len(out.ExcludeEnv) != 1 || out.ExcludeEnv[0] != "EXCLUDE_A" {
			t.Fatalf("ExcludeEnv: got %#v", out.ExcludeEnv)
		}
		if out.MinSecretBytes != 12 {
			t.Fatalf("MinSecretBytes: got %d want 12", out.MinSecretBytes)
		}
	})

	t.Run("feature_enabled_yaml_overrides_catalog_options", func(t *testing.T) {
		t.Parallel()
		inputs := sgcompose.SingleUserOptions{
			IncludePopularEnv: false,
			IncludeEnv:        []string{"INPUT_A"},
			ExcludeEnv:        []string{"EXCLUDE_A"},
			MinSecretBytes:    8,
		}
		runtimeCfg := secretguard.RuntimeConfig{
			Enabled:           true,
			IncludePopularEnv: true,
			IncludeEnv:        []string{"YAML_A", "YAML_B"},
			ExcludeEnv:        []string{"YAML_EXCLUDE"},
			MinSecretBytes:    16,
			MaskByte:          '*',
		}
		out := sgcompose.ComposeSingleUser(runtimeCfg, inputs)
		if !out.IncludePopularEnv {
			t.Fatal("IncludePopularEnv want true")
		}
		if len(out.IncludeEnv) != 2 || out.IncludeEnv[0] != "YAML_A" || out.IncludeEnv[1] != "YAML_B" {
			t.Fatalf("IncludeEnv: got %#v", out.IncludeEnv)
		}
		if len(out.ExcludeEnv) != 1 || out.ExcludeEnv[0] != "YAML_EXCLUDE" {
			t.Fatalf("ExcludeEnv: got %#v", out.ExcludeEnv)
		}
		if out.MinSecretBytes != 16 {
			t.Fatalf("MinSecretBytes: got %d want 16", out.MinSecretBytes)
		}
		if !out.MatcherConfigured {
			t.Fatal("MatcherConfigured want true")
		}
		if out.Matcher.MaskByte != '*' {
			t.Fatalf("Matcher.MaskByte: got %q want *", out.Matcher.MaskByte)
		}
	})

	t.Run("feature_enabled_matcher_override_preserved_when_configured", func(t *testing.T) {
		t.Parallel()
		customMatcher := sgcompose.MatcherOptions{
			PreserveKnownPrefixes: false,
			MaskByte:              '#',
		}
		inputs := sgcompose.SingleUserOptions{
			MatcherConfigured: true,
			Matcher:           customMatcher,
		}
		runtimeCfg := secretguard.RuntimeConfig{
			Enabled:               true,
			PreserveKnownPrefixes: true,
			MaskByte:              '*',
		}
		out := sgcompose.ComposeSingleUser(runtimeCfg, inputs)
		if !out.MatcherConfigured {
			t.Fatal("MatcherConfigured want true")
		}
		if out.Matcher.MaskByte != '#' {
			t.Fatalf("Matcher.MaskByte: got %q want #", out.Matcher.MaskByte)
		}
		if out.Matcher.PreserveKnownPrefixes {
			t.Fatal("Matcher.PreserveKnownPrefixes want false")
		}
	})

	t.Run("decoded_yaml_controls_single_user_options", func(t *testing.T) {
		t.Parallel()
		raw := mustYAMLNode(t, `
action: redact
min_secret_bytes: 12
audit_failure_policy: best_effort
single_user:
  include_popular_env: false
  include_env: [AIP_TEST_SECRETGUARD_INCLUDE]
  exclude_env: [OPENAI_API_KEY]
redaction:
  mask_byte: "X"
  preserve_known_prefixes: false
`)
		regs := []aipsdk.Registration{{
			Kind:        aipsdk.PluginKindFeature,
			ID:          "secrets-guard",
			FactoryKind: "secrets-guard",
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: raw},
		}}
		runtimeCfg, err := secretguard.ComposeRuntimeConfig("single_user", regs)
		if err != nil {
			t.Fatal(err)
		}
		su := sgcompose.ComposeSingleUser(runtimeCfg, sgcompose.SingleUserOptions{})
		if !su.MatcherConfigured || su.Matcher.MaskByte != 'X' {
			t.Fatalf("composed matcher: %#v", su.Matcher)
		}
		if su.Matcher.PreserveKnownPrefixes {
			t.Fatal("composed preserve_known_prefixes want false")
		}
		if su.MinSecretBytes != 12 {
			t.Fatalf("composed min_secret_bytes: %d", su.MinSecretBytes)
		}
	})
}

func TestSecretGuardCompose_FeatureOffSkipsDetectorConstruction(t *testing.T) {
	t.Parallel()

	runtimeCfg := secretguard.RuntimeConfig{
		Enabled: false,
		BetterLeaks: secretguard.BetterLeaksPolicy{
			Enabled: true,
			Workers: 0,
		},
	}
	out, err := sgcompose.Compose(sgcompose.Input{
		RuntimeConfig: &runtimeCfg,
		Environment:   &panicEnv{},
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("feature-off composition: %v", err)
	}
	if out == nil || out.Services == nil {
		t.Fatal("feature-off composition must return the frozen service capability")
	}
	posture := out.Services.Posture()
	if posture.LocalAutoDiscoveryEnabled || posture.BetterLeaksEnabled || posture.DiscoveryDetectorCount != 0 {
		t.Fatalf("feature-off detector posture: %+v", posture)
	}
	if facts := out.Services.DetectorFacts(); facts.Version != "" || facts.ConfigHash != "" || facts.RuleInventoryHash != "" || facts.ActiveRuleCount != 0 || facts.MinimumConfidence != "" || facts.MaxDecodeDepth != 0 || facts.Workers != 0 || len(facts.RuleIDs) != 0 {
		t.Fatalf("feature-off composition constructed detector facts: %+v", facts)
	}
	if out.Inventory != nil {
		t.Fatal("feature-off composition must not publish detector inventory")
	}
}

func TestSecretGuardCompose_MultiUserRejectsSingleUserKey(t *testing.T) {
	t.Parallel()
	raw := mustYAMLNode(t, "action: block\nsingle_user:\n  include_env: [FOO]")
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: raw},
	}}
	_, err := sgcompose.Compose(sgcompose.Input{
		AccessMode:    accessmode.ModeMultiUser,
		Registrations: regs,
		Logger:        discardLogger(),
	})
	if err == nil {
		t.Fatal("expected multi_user + single_user key rejection")
	}
	if !strings.Contains(err.Error(), "single_user is invalid in multi_user mode") {
		t.Fatalf("error: %v", err)
	}
}
