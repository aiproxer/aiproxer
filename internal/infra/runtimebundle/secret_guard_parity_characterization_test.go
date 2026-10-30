package runtimebundle

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdksg "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Stubs for Secret Guard Parity Characterization ---

type charSGStubGuard struct {
	id  string
	ord int
}

func (g charSGStubGuard) ID() string                   { return g.id }
func (g charSGStubGuard) Order() int                   { return g.ord }
func (charSGStubGuard) FailureMode() sdksg.FailureMode { return sdksg.FailClosed }
func (charSGStubGuard) Evaluate(context.Context, *aipapi.Call, sdksg.Meta, sdksg.Services) (sdksg.Decision, error) {
	return sdksg.Decision{Outcome: sdksg.OutcomePass}, nil
}

type charSGCountingEnv struct {
	vals      map[string]string
	lookups   int
	snapshots int
}

func (e *charSGCountingEnv) Lookup(name string) (string, bool) {
	e.lookups++
	if e.vals == nil {
		return "", false
	}
	v, ok := e.vals[name]
	return v, ok
}

func (e *charSGCountingEnv) Snapshot() []string {
	e.snapshots++
	if e.vals == nil {
		return nil
	}
	out := make([]string, 0, len(e.vals))
	for k, v := range e.vals {
		out = append(out, k+"="+v)
	}
	return out
}

type charSGPanicEnv struct {
	calls int
}

func (p *charSGPanicEnv) Lookup(string) (string, bool) {
	p.calls++
	panic("secret guard environment must not be consulted")
}

func (p *charSGPanicEnv) Snapshot() []string {
	p.calls++
	panic("secret guard environment must not be consulted")
}

type charSGCustomObserver struct {
	events []sdksg.DecisionEvent
}

func (o *charSGCustomObserver) OnSecretDecision(_ context.Context, ev sdksg.DecisionEvent) error {
	o.events = append(o.events, ev)
	return nil
}

// --- Acceptance Criteria 1: Secret-Guard Uniqueness Rules & Operator-Visible Errors ---

// TestSecretGuard_UniquenessCompositionRootAndCompileGeneration pins Requirement 1.2, 1.4, 4.2:
//   - Duplicate enabled secrets-guard registrations fail with exact error text:
//     "runtimebundle: multiple enabled secrets-guard registrations"
//   - Matching is case-insensitive on RegistryFactoryKey / ID (e.g. "SECRETS-GUARD", "Secrets-Guard").
//   - Distinct plugin IDs with the same factory kind "secrets-guard" fail uniqueness when both are enabled.
//   - Multiple registrations where only ONE is enabled succeed.
//   - Zero enabled secrets-guard registrations succeed with empty runtime.
//   - End-to-end CompileGeneration fails closed before publication when multiple enabled registrations exist.
func TestSecretGuard_UniquenessCompositionRootAndCompileGeneration(t *testing.T) {
	t.Parallel()

	t.Run("duplicate_enabled_registrations_exact_error", func(t *testing.T) {
		t.Parallel()
		opts := &BuildOptions{
			Production: ProductionOptions{
				FeatureHostRegistrations: []featurehost.Registration{
					(&secretguardhost.Binding{Environment: &charSGPanicEnv{}}).Registration(),
				},
			},
		}
		regs := []aipsdk.Registration{
			{Kind: aipsdk.PluginKindFeature, ID: "sg-1", FactoryKind: "secrets-guard", Enabled: true, Config: aipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: log\n")}},
			{Kind: aipsdk.PluginKindFeature, ID: "sg-2", FactoryKind: "secrets-guard", Enabled: true, Config: aipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: redact\n")}},
		}

		res, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
		require.Error(t, err)
		assert.Nil(t, res)
		assert.Equal(t, "runtimebundle: multiple enabled secrets-guard registrations", err.Error())
	})

	t.Run("case_insensitive_factory_key_duplicate_rejected", func(t *testing.T) {
		t.Parallel()
		opts := &BuildOptions{}
		cases := []struct {
			name string
			k1   string
			k2   string
		}{
			{"uppercase_duplicate", "SECRETS-GUARD", "secrets-guard"},
			{"mixed_case_duplicate", "Secrets-Guard", "SECRETS-GUARD"},
			{"whitespace_padded", " secrets-guard ", "secrets-guard"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				regs := []aipsdk.Registration{
					{Kind: aipsdk.PluginKindFeature, ID: "sg-a", FactoryKind: tc.k1, Enabled: true, Config: aipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: log\n")}},
					{Kind: aipsdk.PluginKindFeature, ID: "sg-b", FactoryKind: tc.k2, Enabled: true, Config: aipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: log\n")}},
				}
				_, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
				require.Error(t, err)
				assert.Equal(t, "runtimebundle: multiple enabled secrets-guard registrations", err.Error())
			})
		}
	})

	t.Run("one_enabled_one_disabled_succeeds", func(t *testing.T) {
		t.Parallel()
		opts := &BuildOptions{}
		regs := []aipsdk.Registration{
			{Kind: aipsdk.PluginKindFeature, ID: "sg-disabled", FactoryKind: "secrets-guard", Enabled: false, Config: aipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: log\n")}},
			{Kind: aipsdk.PluginKindFeature, ID: "sg-enabled", FactoryKind: "secrets-guard", Enabled: true, Config: aipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: block\n")}},
		}
		res, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, "block", res.Inventory.Action)
	})

	t.Run("zero_enabled_registrations_succeeds_empty", func(t *testing.T) {
		t.Parallel()
		opts := &BuildOptions{}
		regs := []aipsdk.Registration{
			{Kind: aipsdk.PluginKindFeature, ID: "sg-disabled", FactoryKind: "secrets-guard", Enabled: false},
		}
		res, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Empty(t, res.Plane.Guards)
		assert.Nil(t, res.Inventory)
	})

	t.Run("compile_generation_end_to_end_rejects_duplicate_secrets_guard", func(t *testing.T) {
		t.Parallel()
		reg := pluginreg.NewRegistry()
		require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))
		node := mustYAMLNode(t, "action: log\n")
		cfg := &config.Config{
			Routing:    config.RoutingConfig{MaxAttempts: 3},
			Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
			Server:     config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
			Diagnostics: config.DiagnosticsConfig{
				Enabled:    true,
				HealthPath: "/healthz",
			},
			Plugins: config.PluginsConfig{
				Features: []config.PluginConfig{
					{ID: "sg-inst-1", Kind: "secrets-guard", Enabled: true, Config: node},
					{ID: "sg-inst-2", Kind: "secrets-guard", Enabled: true, Config: node},
				},
			},
		}
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg:  cfg,
			Log:  slog.Default(),
			Opts: &BuildOptions{PluginRegistry: reg},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process: ps,
			Compose: func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
				return http.NotFoundHandler(), nil
			},
		})
		require.Error(t, err)
		assert.Nil(t, gen)
		assert.Contains(t, err.Error(), "multiple enabled secrets-guard registrations")
	})
}

// TestSecretGuard_ConfigValidationErrorsPinned pins operator-visible configuration
// error classifications and exact error text shapes:
// - Multi-user mode + single_user block: "runtimebundle: secrets-guard config: secrets-guard: single_user is invalid in multi_user mode"
// - Missing action: "runtimebundle: secrets-guard config: secrets-guard: action is required"
// - Unknown action: "runtimebundle: secrets-guard config: secrets-guard: unknown action <q> (want block|redact|log)"
// - Unknown audit_failure_policy: "runtimebundle: secrets-guard config: secrets-guard: unknown audit_failure_policy <q> (want fail_closed|best_effort)"
// - Negative order: "runtimebundle: secrets-guard config: secrets-guard: order must be non-negative"
// - Invalid mask byte: "runtimebundle: secrets-guard config: secrets-guard: redaction.mask_byte must be a single ASCII byte"
// - ScanMaxBytes exceeding 64MB: "runtimebundle: secrets-guard config: secrets-guard: scan_max_bytes exceeds maximum 67108864"
func TestSecretGuard_ConfigValidationErrorsPinned(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		accessMode string
		yamlConfig string
		wantError  string
	}{
		{
			name:       "multi_user_with_single_user_config",
			accessMode: "multi_user",
			yamlConfig: "action: log\nsingle_user:\n  include_env: [\"API_KEY\"]\n",
			wantError:  "runtimebundle: secrets-guard config: secrets-guard: single_user is invalid in multi_user mode",
		},
		{
			name:       "missing_action_empty_mapping",
			accessMode: "single_user",
			yamlConfig: "min_secret_bytes: 8\n",
			wantError:  "runtimebundle: secrets-guard config: secrets-guard: action is required",
		},
		{
			name:       "unknown_action",
			accessMode: "single_user",
			yamlConfig: "action: inspect\n",
			wantError:  "runtimebundle: secrets-guard config: secrets-guard: unknown action \"inspect\" (want block|redact|log)",
		},
		{
			name:       "unknown_audit_failure_policy",
			accessMode: "single_user",
			yamlConfig: "action: log\naudit_failure_policy: silent_drop\n",
			wantError:  "runtimebundle: secrets-guard config: secrets-guard: unknown audit_failure_policy \"silent_drop\" (want fail_closed|best_effort)",
		},
		{
			name:       "negative_order",
			accessMode: "single_user",
			yamlConfig: "action: log\norder: -5\n",
			wantError:  "runtimebundle: secrets-guard config: secrets-guard: order must be non-negative",
		},
		{
			name:       "multi_byte_mask_byte",
			accessMode: "single_user",
			yamlConfig: "action: redact\nredaction:\n  mask_byte: \"***\"\n",
			wantError:  "runtimebundle: secrets-guard config: secrets-guard: redaction.mask_byte must be a single ASCII byte",
		},
		{
			name:       "non_ascii_mask_byte",
			accessMode: "single_user",
			yamlConfig: "action: redact\nredaction:\n  mask_byte: \"€\"\n",
			wantError:  "runtimebundle: secrets-guard config: secrets-guard: redaction.mask_byte must be a single ASCII byte",
		},
		{
			name:       "scan_max_bytes_exceeds_64mb",
			accessMode: "single_user",
			yamlConfig: "action: log\nscan_max_bytes: 70000000\n",
			wantError:  "runtimebundle: secrets-guard config: secrets-guard: scan_max_bytes exceeds maximum 67108864",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{}
			if tt.accessMode == "multi_user" {
				cfg.Access = config.AccessConfig{Mode: "multi_user"}
			}
			opts := &BuildOptions{}
			regs := []aipsdk.Registration{{
				Kind:        aipsdk.PluginKindFeature,
				ID:          "secrets-guard",
				FactoryKind: "secrets-guard",
				Enabled:     true,
				Config:      aipsdk.ConfigPayload{Node: mustYAMLNode(t, tt.yamlConfig)},
			}}

			res, err := testBuildSecretGuardRuntime(cfg, slog.Default(), opts, regs)
			require.Error(t, err)
			assert.Nil(t, res)
			assert.Equal(t, tt.wantError, err.Error())
		})
	}
}

// TestSecretGuard_NilLoggerErrorPinned pins the fail-closed error when feature is enabled
// or guards are present without an explicit observer and log is nil.
func TestSecretGuard_NilLoggerErrorPinned(t *testing.T) {
	t.Parallel()

	t.Run("feature_enabled_nil_logger_fails", func(t *testing.T) {
		t.Parallel()
		opts := &BuildOptions{}
		regs := []aipsdk.Registration{{
			Kind:        aipsdk.PluginKindFeature,
			ID:          "secrets-guard",
			FactoryKind: "secrets-guard",
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: log\n")},
		}}
		res, err := testBuildSecretGuardRuntime(&config.Config{}, nil, opts, regs)
		require.Error(t, err)
		assert.Nil(t, res)
		assert.Equal(t, "runtimebundle: secrets-guard audit requires a non-nil logger", err.Error())
	})

	t.Run("injected_guards_nil_logger_fails", func(t *testing.T) {
		t.Parallel()
		opts := &BuildOptions{
			FeaturePlanes: frozenSecretGuards(charSGStubGuard{id: "injected-guard", ord: 1}),
		}
		res, err := testBuildSecretGuardRuntime(&config.Config{}, nil, opts, nil)
		require.Error(t, err)
		assert.Nil(t, res)
		assert.Equal(t, "runtimebundle: secrets-guard audit requires a non-nil logger", err.Error())
	})

	t.Run("injected_guards_with_explicit_observer_and_nil_logger_succeeds", func(t *testing.T) {
		t.Parallel()
		obs := &charSGCustomObserver{}
		// An explicit observer enters composition through the pinned
		// GenerationInput port, so no logger fallback is required.
		fh := &featurehost.Runtime{}
		out, err := fh.CompileGeneration(context.Background(), featurehost.GenerationInput{
			Planes:           frozenSecretGuards(charSGStubGuard{id: "injected-guard", ord: 1}),
			DecisionObserver: obs,
		})
		require.NoError(t, err)
		plane, _ := secretGuardFromPlanes(out.Planes)
		assert.NotNil(t, plane.DecisionObserver)
	})
}

// --- Acceptance Criteria 2: Secret-Guard Source Policy & Capabilities Flow ---

// TestSecretGuard_SourcePolicyFeatureAndHostCapabilities pins Requirement 4.5 and 5.1:
//   - Feature-contributed SecretGuards are appended in registration order.
//   - SecretGuards are cloned defensively into SecretGuardPlane.Guards without sorting
//     (RequestRuntimeSnapshot owns sorting via secretguard.MaterializeSorted).
//   - Mutating caller slice does not mutate plane.
//   - SecretGuardEnvironment is consulted ONLY in single-user mode with feature enabled.
//   - SecretGuardEnvironment is NOT consulted in multi-user mode or when feature is disabled.
//   - SecretDecisionObserver: non-nil is chained; typed nil falls back to slog; untyped nil falls back to slog.
func TestSecretGuard_SourcePolicyFeatureAndHostCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("feature_guards_appended_in_registration_order_and_isolated", func(t *testing.T) {
		t.Parallel()
		b1 := testkit.FeatureBundle(t, "b1", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "b1", []sdksg.Guard{charSGStubGuard{id: "guard-z", ord: 10}, charSGStubGuard{id: "guard-a", ord: 1}})
		}, nil)
		b2 := testkit.FeatureBundle(t, "b2", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "b2", []sdksg.Guard{charSGStubGuard{id: "guard-m", ord: 5}})
		}, nil)
		gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
		require.NoError(t, err)

		frozenGuards := aipfeature.Get(gen.Frozen, aipfeature.PlaneSecretGuards)
		require.Len(t, frozenGuards, 3)
		assert.Equal(t, "guard-z", frozenGuards[0].ID())
		assert.Equal(t, "guard-a", frozenGuards[1].ID())
		assert.Equal(t, "guard-m", frozenGuards[2].ID())

		opts := &BuildOptions{FeaturePlanes: gen.Frozen}
		res, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, nil)
		require.NoError(t, err)
		require.NotNil(t, res)

		// The engine plane carries no guards by construction: the snapshot
		// overlays guards from its feature planes on every access, while the
		// planes preserve registration order (unsorted clone).
		require.Empty(t, res.Plane.Guards)
		planeGuards := aipfeature.Get(gen.Frozen, aipfeature.PlaneSecretGuards)
		require.Len(t, planeGuards, 3)
		assert.Equal(t, "guard-z", planeGuards[0].ID())
		assert.Equal(t, "guard-a", planeGuards[1].ID())
		assert.Equal(t, "guard-m", planeGuards[2].ID())

		// Snapshot materializes in sorted order (ord ascending, then ID)
		snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
			SecretGuardPlane: res.Plane,
			FeaturePlanes:    gen.Frozen,
		})
		execGuards := snap.SecretGuardExecutionPlane().Guards
		require.Len(t, execGuards, 3)
		assert.Equal(t, "guard-a", execGuards[0].ID())
		assert.Equal(t, "guard-m", execGuards[1].ID())
		assert.Equal(t, "guard-z", execGuards[2].ID())
	})

	t.Run("environment_consulted_only_when_feature_enabled_in_single_user", func(t *testing.T) {
		t.Parallel()
		env := &charSGCountingEnv{
			vals: map[string]string{
				"OPENAI_API_KEY": testkit.SyntheticOpenAIAPIKey,
			},
		}
		opts := &BuildOptions{
			Production: ProductionOptions{
				FeatureHostRegistrations: []featurehost.Registration{
					(&secretguardhost.Binding{Environment: env}).Registration(),
				},
			},
		}
		regs := []aipsdk.Registration{{
			Kind:        aipsdk.PluginKindFeature,
			ID:          "secrets-guard",
			FactoryKind: "secrets-guard",
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: redact\n")},
		}}

		res, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Greater(t, env.lookups+env.snapshots, 0, "env must be consulted in single-user enabled mode")
		assert.Equal(t, "single_user", res.Plane.AccessMode)
		assert.NotNil(t, res.Inventory)
		assert.Greater(t, res.Inventory.CatalogEntryCount, 0)
	})

	t.Run("environment_not_consulted_when_feature_disabled", func(t *testing.T) {
		t.Parallel()
		env := &charSGPanicEnv{}
		opts := &BuildOptions{
			FeaturePlanes: frozenSecretGuards(charSGStubGuard{id: "injected", ord: 1}),
			Production: ProductionOptions{
				FeatureHostRegistrations: []featurehost.Registration{
					(&secretguardhost.Binding{Environment: env}).Registration(),
				},
			},
		}
		regs := []aipsdk.Registration{{
			Kind:        aipsdk.PluginKindFeature,
			ID:          "secrets-guard",
			FactoryKind: "secrets-guard",
			Enabled:     false,
		}}

		res, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, 0, env.calls, "env must not be consulted when feature is disabled")
	})

	t.Run("environment_not_consulted_in_multi_user_mode", func(t *testing.T) {
		t.Parallel()
		env := &charSGPanicEnv{}
		opts := &BuildOptions{
			Production: ProductionOptions{
				FeatureHostRegistrations: []featurehost.Registration{
					(&secretguardhost.Binding{Environment: env}).Registration(),
				},
			},
		}
		regs := []aipsdk.Registration{{
			Kind:        aipsdk.PluginKindFeature,
			ID:          "secrets-guard",
			FactoryKind: "secrets-guard",
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: block\n")},
		}}
		cfg := &config.Config{Access: config.AccessConfig{Mode: "multi_user"}}

		res, err := testBuildSecretGuardRuntime(cfg, slog.Default(), opts, regs)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, 0, env.calls, "env must not be consulted in multi_user mode")
		assert.Equal(t, "multi_user", res.Plane.AccessMode)
		assert.Equal(t, "multi_user", res.Inventory.AccessMode)
	})

	t.Run("observer_chaining_and_slog_fallback", func(t *testing.T) {
		t.Parallel()
		var logBuf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

		// Observers enter composition through the pinned GenerationInput port;
		// the composed plane is read back purely via plane access.
		composeWithObserver := func(observer sdksg.Observer) *secretGuardTestRuntime {
			fh, err := featurehost.NewProcess(context.Background(), featurehost.ProcessInput{Logger: log})
			require.NoError(t, err)
			out, err := fh.CompileGeneration(context.Background(), featurehost.GenerationInput{
				Planes:           frozenSecretGuards(charSGStubGuard{id: "g1", ord: 1}),
				DecisionObserver: observer,
			})
			require.NoError(t, err)
			plane, inv := secretGuardFromPlanes(out.Planes)
			return &secretGuardTestRuntime{Plane: plane, Inventory: inv}
		}

		// 1. Explicit non-nil observer is chained
		customObs := &charSGCustomObserver{}
		res1 := composeWithObserver(customObs)
		require.NotNil(t, res1.Plane.DecisionObserver)
		err := res1.Plane.DecisionObserver.OnSecretDecision(context.Background(), sdksg.DecisionEvent{EventID: "ev-1"})
		require.NoError(t, err)
		require.Len(t, customObs.events, 1)

		// 2. Typed-nil observer falls back to slog observer
		var typedNilObs *charSGCustomObserver
		res2 := composeWithObserver(typedNilObs)
		require.NotNil(t, res2.Plane.DecisionObserver)
		logBuf.Reset()
		err = res2.Plane.DecisionObserver.OnSecretDecision(context.Background(), sdksg.DecisionEvent{EventID: "ev-typed-nil"})
		require.NoError(t, err)
		assert.Contains(t, logBuf.String(), "aip.secret_guard.decision")

		// 3. Feature disabled and 0 guards leaves observer uninitialized (nil)
		optsDisabled := &BuildOptions{}
		res3, err := testBuildSecretGuardRuntime(&config.Config{}, log, optsDisabled, nil)
		require.NoError(t, err)
		assert.Nil(t, res3.Plane.DecisionObserver)
	})
}

// TestSecretGuard_PlanesConvergencePreservation pins that secret-guard posture
// converges through ordinary planes: the featurehost-composed execution plane
// extracts back to an identical plane and inventory purely via plane access.
func TestSecretGuard_PlanesConvergencePreservation(t *testing.T) {
	t.Parallel()

	t.Run("composed_execution_plane_extracts_identically", func(t *testing.T) {
		t.Parallel()
		fh, err := featurehost.NewProcess(context.Background(), featurehost.ProcessInput{Logger: slog.Default()})
		require.NoError(t, err)
		out, err := fh.CompileGeneration(context.Background(), featurehost.GenerationInput{
			Planes: frozenSecretGuards(charSGStubGuard{id: "g1", ord: 1}),
		})
		require.NoError(t, err)
		plane, inv := secretGuardFromPlanes(out.Planes)
		require.NotNil(t, plane.DecisionObserver)
		require.NotNil(t, inv)
		assert.Equal(t, inv.CatalogEntryCount, 0)
		assert.Equal(t, "single_user", plane.AccessMode)
		assert.Equal(t, plane.AccessMode, inv.AccessMode)
	})
}
