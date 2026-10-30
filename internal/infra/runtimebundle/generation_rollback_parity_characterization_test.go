package runtimebundle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stubs for Generation Rollback Parity Tests ---

type charRollbackTerminalProvider struct {
	id string
}

func (p *charRollbackTerminalProvider) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

func (p *charRollbackTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

func parseTestYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(raw), &n))
	return n
}

// publishTestGeneration publishes gen as a Generation on mgr and returns the Generation.
func publishTestGeneration(t *testing.T, mgr *runtimehost.Manager, bundle runtimebundle.GenerationRuntime, label string) *runtimehost.Generation {
	t.Helper()
	gen := mgr.PrepareRequestPlane(label, bundle)
	gen.SetMetaHints(runtimehost.MetaHints{TriggerKind: label, LoadedAt: time.Now().UTC()})
	if err := mgr.Publish(gen); err != nil {
		_ = gen.Discard()
		t.Fatalf("publish %s: %v", label, err)
	}
	return gen
}

func newProcessWithRegistry(t *testing.T, reg *pluginreg.Registry) *runtimebundle.ProcessServices {
	t.Helper()
	cfg := processBaseConfig()
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate process cfg: %v", err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

// TestCompileGeneration_NilProcessServicesFailsClosed pins the compile
// precondition: a nil ProcessServices is rejected before any candidate
// resource is touched, never dereferenced.
func TestCompileGeneration_NilProcessServicesFailsClosed(t *testing.T) {
	t.Parallel()
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   nil,
		Candidate: processBaseConfig(),
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err == nil || !strings.Contains(err.Error(), "nil ProcessServices") {
		t.Fatalf("CompileGeneration(nil process) err = %v, want nil ProcessServices rejection", err)
	}
	if bundle != nil {
		t.Fatalf("CompileGeneration(nil process) returned a bundle: %T", bundle)
	}
}

// TestCompileGeneration_ClosedProcessServicesFailsClosed pins the lifecycle
// precondition: compiling into a shut-down process fails closed instead of
// building a generation over released process-owned resources.
func TestCompileGeneration_ClosedProcessServicesFailsClosed(t *testing.T) {
	t.Parallel()
	ps := newProcessWithRegistry(t, stdFactoryCatalog(t))
	if err := ps.Close(); err != nil {
		t.Fatalf("close process: %v", err)
	}
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: processBaseConfig(),
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err == nil || !strings.Contains(err.Error(), "ProcessServices is closed") {
		t.Fatalf("CompileGeneration(closed process) err = %v, want closed-process rejection", err)
	}
	if bundle != nil {
		t.Fatalf("CompileGeneration(closed process) returned a bundle: %T", bundle)
	}
}

// detectorSelectionRollbackPanicEnv covers startup, candidate validation,
// request activity, and recovery without revealing any environment contents.
type detectorSelectionRollbackPanicEnv struct{}

func (detectorSelectionRollbackPanicEnv) Lookup(string) (string, bool) {
	panic("generation validation must not look up environment secrets")
}

func (detectorSelectionRollbackPanicEnv) Snapshot() []string {
	panic("generation validation must not enumerate environment secrets")
}

func detectorSelectionMultiUserConfig(cfg *config.Config) {
	cfg.Access = config.AccessConfig{Mode: "multi_user"}
	cfg.Auth = config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"}
	cfg.Server.AuthMode = config.AuthModeExternal
}

// --- Acceptance Criteria 5: Generation Rollback Parity Characterization ---

// TestTerminalDecision_GenerationRollback_InvalidContributionRetainsPublishedGeneration pins Requirement 1.2, 1.4, 4.2:
//   - Proves that when an invalid contribution (exclusive terminal decision provider conflict,
//     typed-nil provider, duplicate secrets-guard, invalid secrets-guard config, or candidate fault)
//     fails during candidate compilation:
//     1. Candidate compilation fails closed and returns the exact error.
//     2. The previously published generation remains serving unchanged on the runtimehost.Manager.
//     3. Inflight/new requests to the published generation continue to succeed.
//     4. Process-owned services survive the candidate compile failure.
func TestTerminalDecision_GenerationRollback_InvalidContributionRetainsPublishedGeneration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		setupRegistry    func(t *testing.T, reg *pluginreg.Registry)
		mutateProcessCfg func(cfg *config.Config)
		mutateCandCfg    func(t *testing.T, cand *config.Config)
		fault            runtimebundle.CandidateFaultInject
		wantErrSubstr    string
		wantSentinel     error
	}{
		{
			name: "exclusive_terminal_provider_conflict",
			setupRegistry: func(t *testing.T, reg *pluginreg.Registry) {
				t.Helper()
				provA := &charRollbackTerminalProvider{id: "term-provider-a"}
				provB := &charRollbackTerminalProvider{id: "term-provider-b"}

				require.NoError(t, reg.RegisterFeature("term-feat-a", func(yaml.Node) (aipfeature.FeatureBundle, error) {
					return testkit.FeatureBundle(t, "term-feat-a", func(cs *aipfeature.ContributionSet) error {
						return aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "term-provider-a", terminaldecision.Provider(provA))
					}, nil), nil
				}))
				require.NoError(t, reg.RegisterFeature("term-feat-b", func(yaml.Node) (aipfeature.FeatureBundle, error) {
					return testkit.FeatureBundle(t, "term-feat-b", func(cs *aipfeature.ContributionSet) error {
						return aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "term-provider-b", terminaldecision.Provider(provB))
					}, nil), nil
				}))
			},
			mutateCandCfg: func(t *testing.T, cand *config.Config) {
				t.Helper()
				cand.Plugins.Features = append(cand.Plugins.Features,
					config.PluginConfig{ID: "term-feat-a", Kind: "term-feat-a", Enabled: true},
					config.PluginConfig{ID: "term-feat-b", Kind: "term-feat-b", Enabled: true},
				)
			},
			wantErrSubstr: `"term-provider-a" and "term-provider-b"`,
			wantSentinel:  aipfeature.ErrExclusiveConflict,
		},
		{
			name: "typed_nil_terminal_provider",
			setupRegistry: func(t *testing.T, reg *pluginreg.Registry) {
				t.Helper()
				require.NoError(t, reg.RegisterFeature("term-feat-nil", func(yaml.Node) (aipfeature.FeatureBundle, error) {
					cs := aipfeature.NewContributionSet()
					if err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "term-feat-nil", terminaldecision.Provider((*charRollbackTerminalProvider)(nil))); err != nil {
						return aipfeature.FeatureBundle{}, err
					}
					return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
				}))
			},
			mutateCandCfg: func(t *testing.T, cand *config.Config) {
				t.Helper()
				cand.Plugins.Features = append(cand.Plugins.Features,
					config.PluginConfig{ID: "term-feat-nil", Kind: "term-feat-nil", Enabled: true},
				)
			},
			wantErrSubstr: "terminaldecision: invalid provider",
			wantSentinel:  terminaldecision.ErrInvalidProvider,
		},
		{
			name: "duplicate_enabled_secrets_guard_registrations",
			mutateCandCfg: func(t *testing.T, cand *config.Config) {
				t.Helper()
				cand.Plugins.Features = append(cand.Plugins.Features,
					config.PluginConfig{ID: "sg-1", Kind: "secrets-guard", Enabled: true, Config: parseTestYAMLNode(t, "action: log\n")},
					config.PluginConfig{ID: "sg-2", Kind: "secrets-guard", Enabled: true, Config: parseTestYAMLNode(t, "action: redact\n")},
				)
			},
			wantErrSubstr: "multiple enabled secrets-guard registrations",
		},
		{
			name: "invalid_secrets_guard_config_unknown_action",
			mutateCandCfg: func(t *testing.T, cand *config.Config) {
				t.Helper()
				cand.Plugins.Features = append(cand.Plugins.Features,
					config.PluginConfig{ID: "sg-bad", Kind: "secrets-guard", Enabled: true, Config: parseTestYAMLNode(t, "action: bogus_action\n")},
				)
			},
			wantErrSubstr: "secrets-guard: unknown action \"bogus_action\"",
		},
		{
			name: "invalid_secrets_guard_config_multi_user_with_single_user_block",
			mutateProcessCfg: func(cfg *config.Config) {
				cfg.Access = config.AccessConfig{Mode: "multi_user"}
				cfg.Auth = config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"}
				cfg.Server.AuthMode = config.AuthModeExternal
			},
			mutateCandCfg: func(t *testing.T, cand *config.Config) {
				t.Helper()
				cand.Access = config.AccessConfig{Mode: "multi_user"}
				cand.Auth = config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"}
				cand.Server.AuthMode = config.AuthModeExternal
				cand.Plugins.Features = append(cand.Plugins.Features,
					config.PluginConfig{ID: "sg-mu-bad", Kind: "secrets-guard", Enabled: true, Config: parseTestYAMLNode(t, "action: log\nsingle_user:\n  include_env: [\"FOO\"]\n")},
				)
			},
			wantErrSubstr: "secrets-guard: single_user is invalid in multi_user mode",
		},
		{
			name:             "multi_user_local_discovery_true_betterleaks_absent",
			mutateProcessCfg: detectorSelectionMultiUserConfig,
			mutateCandCfg: func(t *testing.T, cand *config.Config) {
				t.Helper()
				detectorSelectionMultiUserConfig(cand)
				cand.Plugins.Features = append(cand.Plugins.Features,
					config.PluginConfig{ID: "sg-local-invalid", Kind: "secrets-guard", Enabled: true, Config: parseTestYAMLNode(t, "action: block\nauto_discovered_local_keys:\n  enabled: true\n")},
				)
			},
			wantErrSubstr: "auto_discovered_local_keys",
		},
		{
			name:             "multi_user_local_discovery_true_betterleaks_enabled",
			mutateProcessCfg: detectorSelectionMultiUserConfig,
			mutateCandCfg: func(t *testing.T, cand *config.Config) {
				t.Helper()
				detectorSelectionMultiUserConfig(cand)
				cand.Plugins.Features = append(cand.Plugins.Features,
					config.PluginConfig{ID: "sg-local-invalid", Kind: "secrets-guard", Enabled: true, Config: parseTestYAMLNode(t, "action: block\nauto_discovered_local_keys:\n  enabled: true\nbetterleaks:\n  enabled: true\n")},
				)
			},
			wantErrSubstr: "auto_discovered_local_keys",
		},
		{
			name:             "multi_user_local_discovery_true_betterleaks_disabled",
			mutateProcessCfg: detectorSelectionMultiUserConfig,
			mutateCandCfg: func(t *testing.T, cand *config.Config) {
				t.Helper()
				detectorSelectionMultiUserConfig(cand)
				cand.Plugins.Features = append(cand.Plugins.Features,
					config.PluginConfig{ID: "sg-local-invalid", Kind: "secrets-guard", Enabled: true, Config: parseTestYAMLNode(t, "action: block\nauto_discovered_local_keys:\n  enabled: true\nbetterleaks:\n  enabled: false\n")},
				)
			},
			wantErrSubstr: "auto_discovered_local_keys",
		},
		{
			name: "betterleaks_unknown_selector",
			mutateCandCfg: func(t *testing.T, cand *config.Config) {
				t.Helper()
				cand.Plugins.Features = append(cand.Plugins.Features,
					config.PluginConfig{ID: "sg-selector-invalid", Kind: "secrets-guard", Enabled: true, Config: parseTestYAMLNode(t, "action: block\nauto_discovered_local_keys:\n  enabled: false\nbetterleaks:\n  disable_rules: [unknown-rule-for-reload-test]\n")},
				)
			},
			wantErrSubstr: "unknown rule selector",
		},
		{
			name: "betterleaks_required_component_selector",
			mutateCandCfg: func(t *testing.T, cand *config.Config) {
				t.Helper()
				cand.Plugins.Features = append(cand.Plugins.Features,
					config.PluginConfig{ID: "sg-selector-dependent", Kind: "secrets-guard", Enabled: true, Config: parseTestYAMLNode(t, "action: block\nauto_discovered_local_keys:\n  enabled: false\nbetterleaks:\n  disable_rules: [aws-secret-access-key]\n")},
				)
			},
			wantErrSubstr: "cannot remove a required component",
		},
		{
			name:          "candidate_fault_inject_handler",
			fault:         runtimebundle.CandidateFaultInject{After: "handler"},
			wantErrSubstr: "candidate fault injected: after handler",
		},
		{
			name:          "candidate_fault_inject_composer_clone",
			fault:         runtimebundle.CandidateFaultInject{After: "composer-clone"},
			wantErrSubstr: "candidate fault injected: after composer-clone",
		},
		{
			name:          "candidate_fault_inject_ledger_transfer",
			fault:         runtimebundle.CandidateFaultInject{After: "ledger-transfer"},
			wantErrSubstr: "ledger unavailable for transfer",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// 1. Establish ProcessServices with standard catalog
			reg := stdFactoryCatalog(t)
			if tc.setupRegistry != nil {
				tc.setupRegistry(t, reg)
			}
			processCfg := processBaseConfig()
			if tc.mutateProcessCfg != nil {
				tc.mutateProcessCfg(processCfg)
			}
			require.NoError(t, config.Validate(processCfg))

			ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
				Cfg: processCfg,
				Log: testkit.DiscardLogger(),
				Opts: &runtimebundle.BuildOptions{
					PluginRegistry: reg,
					Production: runtimebundle.ProductionOptions{
						FeatureHostRegistrations: []featurehost.Registration{
							(&secretguardhost.Binding{Environment: detectorSelectionRollbackPanicEnv{}}).Registration(),
						},
					},
					Auth: runtimebundle.AuthOptions{
						RemoteDecider: &testkit.StubRemoteDecider{
							Decision: sdkauth.Decision{
								Outcome:   sdkauth.OutcomeAllow,
								Principal: execview.PrincipalView{ID: "char-user"},
							},
						},
					},
				},
				Tracing: runtimebundle.ProcessTracing{
					Shutdown: func(context.Context) error { return nil },
				},
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = ps.Close() })

			// 2. Compile and Publish Initial Generation 1
			gen1Cfg := stubCandidateConfig(t, "gen-1", "serving-gen1-response", "gen-1:stub-default", []config.PluginConfig{
				{ID: "openai-responses", Enabled: true},
			})
			if tc.mutateProcessCfg != nil {
				tc.mutateProcessCfg(gen1Cfg)
			}
			gen1Bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
				Process:   ps,
				Candidate: gen1Cfg,
				Compose:   stdhttp.ComposeStandardHTTP,
			})
			require.NoError(t, err)

			mgr := runtimehost.NewManager(8, nil)
			t.Cleanup(func() { _ = mgr.ShutdownDetached(context.Background()) })

			publishedGen1 := publishTestGeneration(t, mgr, gen1Bundle, "startup")
			require.Equal(t, int64(1), publishedGen1.ID())
			require.Equal(t, int64(1), mgr.Active().ID())

			// Verify Gen 1 is serving successfully
			res1 := postResponses(t, mgr.Active().Handler(), "gen-1:stub-default")
			assert.Contains(t, res1, "serving-gen1-response")

			// 3. Prepare Candidate Generation Config
			candCfg := stubCandidateConfig(t, "gen-2", "candidate-gen2-response", "gen-2:stub-default", []config.PluginConfig{
				{ID: "openai-responses", Enabled: true},
			})
			if tc.mutateCandCfg != nil {
				tc.mutateCandCfg(t, candCfg)
			}

			// 4. Attempt Candidate Compilation (must fail closed)
			compileInput := runtimebundle.GenerationCompileInput{
				Process:     ps,
				Candidate:   candCfg,
				Compose:     stdhttp.ComposeStandardHTTP,
				FaultInject: tc.fault,
			}
			failedBundle, compileErr := runtimebundle.CompileGeneration(context.Background(), compileInput)
			if failedBundle != nil {
				t.Cleanup(func() { _ = failedBundle.Close() })
			}
			require.Error(t, compileErr)
			assert.Nil(t, failedBundle)

			if tc.wantErrSubstr != "" {
				assert.Contains(t, compileErr.Error(), tc.wantErrSubstr)
			}
			if tc.wantSentinel != nil {
				assert.True(t, errors.Is(compileErr, tc.wantSentinel), "expected errors.Is match for sentinel %v", tc.wantSentinel)
			}

			// 5. Verify Rollback & Retention of Generation 1:
			// Process services must remain open
			assert.False(t, ps.Closed(), "process-owned services must survive candidate compile rejection")

			// Active generation on manager must remain Generation 1
			require.NotNil(t, mgr.Active())
			assert.Equal(t, int64(1), mgr.Active().ID(), "manager must retain published generation 1 as active")

			// Generation 1 handler continues serving requests identically
			res1Again := postResponses(t, mgr.Active().Handler(), "gen-1:stub-default")
			assert.Contains(t, res1Again, "serving-gen1-response", "published generation 1 must continue serving requests unchanged")

			// 6. Prove ProcessServices remains fully capable of compiling a valid follow-up candidate
			recoveryCfg := stubCandidateConfig(t, "gen-recovery", "recovery-response", "gen-recovery:stub-default", []config.PluginConfig{
				{ID: "openai-responses", Enabled: true},
			})
			if tc.mutateProcessCfg != nil {
				tc.mutateProcessCfg(recoveryCfg)
			}
			recoveryBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
				Process:   ps,
				Candidate: recoveryCfg,
				Compose:   stdhttp.ComposeStandardHTTP,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = recoveryBundle.Close() })

			publishedRecovery := publishTestGeneration(t, mgr, recoveryBundle, "recovery")
			assert.Equal(t, int64(2), publishedRecovery.ID())
			assert.Equal(t, int64(2), mgr.Active().ID())

			resRecovery := postResponses(t, mgr.Active().Handler(), "gen-recovery:stub-default")
			assert.Contains(t, resRecovery, "recovery-response")
		})
	}
}
