package secretguard_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	featuresecretguard "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	sgcompose "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// detectorSelectionEnv permits inventory only when the resolved local policy permits it.
// Panics identify forbidden calls without including environment names or values.
type detectorSelectionEnv struct {
	allow bool
	value string
	calls int
}

func (e *detectorSelectionEnv) Lookup(string) (string, bool) {
	e.calls++
	if !e.allow {
		panic("detector policy performed a forbidden environment lookup")
	}
	return "", false
}

func (e *detectorSelectionEnv) Snapshot() []string {
	e.calls++
	if !e.allow {
		panic("detector policy performed a forbidden environment snapshot")
	}
	return []string{"OPENAI_API_KEY=" + e.value}
}

func detectorSelectionRegistration(t *testing.T, raw string) aipsdk.Registration {
	t.Helper()
	return aipsdk.Registration{
		Kind: aipsdk.PluginKindFeature, ID: "detector-selection", FactoryKind: "secrets-guard", Enabled: true,
		Config: aipsdk.ConfigPayload{Node: mustYAMLNode(t, raw)},
	}
}

func detectorSelectionCompose(t *testing.T, in sgcompose.Input) (*sgcompose.Output, error) {
	t.Helper()
	defer func() {
		if recover() != nil {
			t.Fatal("composition accessed the panic environment contrary to detector policy")
		}
	}()
	return sgcompose.Compose(in)
}

func detectorSwitchYAML(key, setting string) string {
	if setting == "absent" {
		return ""
	}
	return fmt.Sprintf("%s:\n  enabled: %s\n", key, setting)
}

// Requirements 1.2-1.8: every combination must be resolved, not silently ignored.
// Diagnostics are checked through the operator projection rather than by coupling
// tests to the policy resolver's private representation.
func TestDetectorSelection_CompositionMatrix(t *testing.T) {
	t.Parallel()
	for _, mode := range []accessmode.Mode{accessmode.ModeSingleUser, accessmode.ModeMultiUser} {
		for _, local := range []string{"absent", "true", "false"} {
			for _, betterleaks := range []string{"absent", "true", "false"} {
				t.Run(string(mode)+"/local_"+local+"/betterleaks_"+betterleaks, func(t *testing.T) {
					t.Parallel()
					wantLocal := mode == accessmode.ModeSingleUser && local != "false"
					wantBetterLeaks := betterleaks != "false"
					env := &detectorSelectionEnv{allow: wantLocal, value: strings.Repeat("q7", 16)}
					reg := detectorSelectionRegistration(t, "action: block\n"+
						detectorSwitchYAML("auto_discovered_local_keys", local)+detectorSwitchYAML("betterleaks", betterleaks))
					out, err := detectorSelectionCompose(t, sgcompose.Input{
						AccessMode: mode, Registrations: []aipsdk.Registration{reg}, Environment: env, Logger: discardLogger(),
					})
					if mode == accessmode.ModeMultiUser && local == "true" {
						if env.calls != 0 {
							t.Errorf("rejected policy read environment %d times", env.calls)
						}
						if err == nil || out != nil {
							t.Fatal("explicit multi_user local discovery must reject the candidate with no output")
						}
						if len(err.Error()) > 512 || !strings.Contains(err.Error(), "auto_discovered_local_keys") {
							t.Fatal("rejection must identify the local policy key with a bounded error")
						}
						return
					}
					if err != nil || out == nil || out.Inventory == nil {
						t.Fatal("valid detector policy must compose an enabled feature with inventory")
					}
					if wantLocal && env.calls == 0 {
						t.Error("single_user local discovery must load the catalog")
					}
					if !wantLocal && env.calls != 0 {
						t.Errorf("disabled local discovery read environment %d times", env.calls)
					}
					if got := out.Inventory.SecretGuardCatalogEntryCount; (got > 0) != wantLocal {
						t.Errorf("catalog presence = %t, want %t", got > 0, wantLocal)
					}
					assertDetectorSelectionExact(t, out, env.value, wantLocal)
					if mode == accessmode.ModeMultiUser {
						assertDetectorSelectionRequestCredential(t, out)
					}
					facts := detectorSelectionFacts(t, reg, out)
					assertDetectorSelectionBool(t, facts, "local_auto_discovery_enabled", wantLocal)
					assertDetectorSelectionBool(t, facts, "betterleaks_enabled", wantBetterLeaks)
					if wantBetterLeaks {
						for _, key := range []string{"betterleaks_version", "betterleaks_config_hash", "betterleaks_active_rule_count", "betterleaks_minimum_confidence", "betterleaks_max_decode_depth", "betterleaks_workers"} {
							if value, present := facts[key]; !present || value == "" || value == float64(0) {
								t.Errorf("enabled BetterLeaks posture must expose bounded %s", key)
							}
						}
					}
					if !wantLocal && !wantBetterLeaks {
						count, present := facts["discovery_detector_count"]
						if !present || count != float64(0) {
							t.Error("both-off must explicitly diagnose zero discovery detectors")
						}
					}
					if !wantLocal && env.calls != 0 {
						t.Error("request activity or diagnostics consulted environment")
					}
				})
			}
		}
	}
}

func TestDetectorSelection_EnabledZeroFactsRemainPresentInInventory(t *testing.T) {
	t.Parallel()

	baseline, err := featuresecretguard.BuildGenerationServices(featuresecretguard.DetectorPolicy{
		BetterLeaks: featuresecretguard.BetterLeaksPolicy{
			Enabled:           true,
			MinimumConfidence: featuresecretguard.DefaultBetterLeaksConfidence,
			MaxDecodeDepth:    0,
			Workers:           1,
			MaxFindings:       featuresecretguard.DefaultBetterLeaksMaxFindings,
		},
	}, engine.NewDisabledSource())
	if err != nil {
		t.Fatalf("build baseline BetterLeaks policy: %v", err)
	}
	runtimeCfg := &featuresecretguard.RuntimeConfig{
		Enabled: true,
		Action:  "block",
		BetterLeaks: featuresecretguard.BetterLeaksPolicy{
			Enabled:           true,
			MinimumConfidence: featuresecretguard.DefaultBetterLeaksConfidence,
			MaxDecodeDepth:    0,
			Workers:           1,
			DisableRules:      append([]string(nil), baseline.DetectorFacts().RuleIDs...),
			MaxFindings:       featuresecretguard.DefaultBetterLeaksMaxFindings,
		},
	}
	out, err := sgcompose.Compose(sgcompose.Input{RuntimeConfig: runtimeCfg, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("compose zero-fact BetterLeaks policy: %v", err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{{
		ID: "zero-facts", Kind: "secrets-guard", Enabled: true,
	}}}}
	snapshot, err := diag.InventorySnapshotForConfig(t.Context(), cfg, finishedSecretGuardInventory("zero-facts", out.Inventory))
	if err != nil {
		t.Fatalf("project zero-fact inventory: %v", err)
	}
	raw, err := json.Marshal(snapshot.Extensions.Features[0].SecretGuard)
	if err != nil {
		t.Fatalf("serialize zero-fact inventory: %v", err)
	}
	var facts map[string]any
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("decode zero-fact inventory: %v", err)
	}
	for _, key := range []string{"betterleaks_active_rule_count", "betterleaks_max_decode_depth"} {
		value, present := facts[key]
		if !present || value != float64(0) {
			t.Errorf("enabled zero-valued %s must remain present as 0, got present=%t value=%v", key, present, value)
		}
	}
}

func assertDetectorSelectionExact(t *testing.T, out *sgcompose.Output, value string, want bool) {
	t.Helper()
	if out.Plane.MatcherResolver == nil {
		if want || out.Plane.AccessMode == "multi_user" {
			t.Fatal("composition must retain an applicable exact matcher resolver")
		}
		return
	}
	matcher, err := out.Plane.MatcherResolver.Resolve(t.Context())
	if err != nil {
		t.Fatal("exact matcher resolution failed")
	}
	found := false
	if matcher != nil {
		findings, err := matcher.ScanString(t.Context(), "key="+value)
		if err != nil {
			t.Fatal("local exact scan failed")
		}
		found = len(findings) > 0
	}
	if found != want {
		t.Errorf("local exact protection = %t, want %t", found, want)
	}
}

func assertDetectorSelectionRequestCredential(t *testing.T, out *sgcompose.Output) {
	t.Helper()
	value := strings.Repeat("r9", 16)
	catalog, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name: "request_credential", Value: value, SourceCategory: sdk.SourceCategoryRequestCred,
	}}, 8)
	if err != nil {
		t.Fatal("request credential fixture construction failed")
	}
	requestMatcher := engine.AsMatcher(engine.NewMatcher(catalog))
	ctx := sdk.WithRequestMatcher(t.Context(), requestMatcher)
	matcher, err := out.Plane.MatcherResolver.Resolve(ctx)
	if err != nil || matcher != requestMatcher {
		t.Fatal("multi_user composition must preserve the accepted request-scoped matcher")
	}
	findings, err := matcher.ScanString(ctx, "credential="+value)
	if err != nil || len(findings) != 1 {
		t.Fatal("request credential exact protection must remain active")
	}
	withoutCredential, err := out.Plane.MatcherResolver.Resolve(t.Context())
	if err != nil || withoutCredential != nil {
		t.Fatal("request credential matcher escaped its request context")
	}
}

// finishedSecretGuardInventory projects a composed secret-guard posture into
// the finished operator inventory rows the diagnostics snapshot consumes. In
// production the featurehost inventory authority computes this read model
// (including the secret-guard instance association); detector fixtures supply
// the same finished row instead of relying on diagnostics-side hydration.
func finishedSecretGuardInventory(instanceID string, extras *diag.InventoryExtras) *diag.InventoryExtras {
	ruleCount, decodeDepth := extras.SecretGuardBetterLeaksRuleCount, extras.SecretGuardBetterLeaksDecodeDepth
	row := &diag.InventorySecretGuard{
		InstanceID:                instanceID,
		Action:                    strings.TrimSpace(extras.SecretGuardAction),
		CatalogEntryCount:         extras.SecretGuardCatalogEntryCount,
		SourceCategories:          append([]string(nil), extras.SecretGuardSourceCategories...),
		AccessMode:                extras.SecretGuardAccessMode,
		LocalAutoDiscoveryEnabled: extras.SecretGuardLocalAutoDiscovery,
		BetterLeaksEnabled:        extras.SecretGuardBetterLeaksEnabled,
		BetterLeaksVersion:        extras.SecretGuardBetterLeaksVersion,
		BetterLeaksConfigHash:     extras.SecretGuardBetterLeaksConfigHash,
		BetterLeaksConfidence:     extras.SecretGuardBetterLeaksConfidence,
		BetterLeaksWorkers:        extras.SecretGuardBetterLeaksWorkers,
		DiscoveryDetectorCount:    extras.SecretGuardDiscoveryDetectorCount,
	}
	if extras.SecretGuardBetterLeaksEnabled {
		row.BetterLeaksRuleCount, row.BetterLeaksDecodeDepth = &ruleCount, &decodeDepth
	}
	return &diag.InventoryExtras{FeatureMetadata: []diag.InventoryFeatureMetadata{{
		InstanceID:     instanceID,
		StageOccupancy: []diag.InventoryStageOccupancy{},
		SecretGuard:    row,
	}}}
}

func detectorSelectionFacts(t *testing.T, reg aipsdk.Registration, out *sgcompose.Output) map[string]any {
	t.Helper()
	cfg := &config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{{
		ID: reg.ID, Kind: reg.FactoryKind, Enabled: reg.Enabled, Config: reg.Config.Node,
	}}}}
	snapshot, err := diag.InventorySnapshotForConfig(t.Context(), cfg, finishedSecretGuardInventory(reg.ID, out.Inventory))
	if err != nil || len(snapshot.Extensions.Features) != 1 || snapshot.Extensions.Features[0].SecretGuard == nil {
		t.Fatal("enabled detector posture must project to operator inventory")
	}
	raw, err := json.Marshal(snapshot.Extensions.Features[0].SecretGuard)
	if err != nil {
		t.Fatal("detector inventory must be serializable")
	}
	if len(raw) > 4096 {
		t.Fatal("detector posture must remain bounded")
	}
	var facts map[string]any
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatal("detector posture JSON is invalid")
	}
	return facts
}

func assertDetectorSelectionBool(t *testing.T, facts map[string]any, key string, want bool) {
	t.Helper()
	got, present := facts[key]
	if !present || got != want {
		t.Errorf("diagnostic %s must explicitly resolve to %t", key, want)
	}
}

// Requirements 1.1 and 1.9: new detector keys cannot opt an unregistered or
// explicitly disabled feature in, and the action never defaults implicitly.
func TestDetectorSelection_ActivationCharacterization(t *testing.T) {
	t.Parallel()
	for _, mode := range []accessmode.Mode{accessmode.ModeSingleUser, accessmode.ModeMultiUser} {
		for _, activation := range []string{"absent", "disabled", "unrelated", "missing_action"} {
			t.Run(string(mode)+"/"+activation, func(t *testing.T) {
				t.Parallel()
				env := &panicEnv{}
				var regs []aipsdk.Registration
				if activation != "absent" {
					reg := detectorSelectionRegistration(t, "auto_discovered_local_keys:\n  enabled: true\nbetterleaks:\n  enabled: true\n")
					reg.Enabled = activation != "disabled"
					if activation == "unrelated" {
						reg.FactoryKind = "other-feature"
					}
					regs = []aipsdk.Registration{reg}
				}
				out, err := detectorSelectionCompose(t, sgcompose.Input{
					AccessMode: mode, Registrations: regs, Environment: env, Logger: discardLogger(),
				})
				if activation == "missing_action" {
					if err == nil || out != nil || !strings.Contains(err.Error(), "action") {
						t.Fatal("enabled registration must require explicit action before environment access")
					}
				} else if err != nil || out == nil || out.Inventory != nil || len(out.Plane.Guards) != 0 {
					t.Fatal("unconfigured/disabled feature must retain disabled composition")
				}
				if env.calls != 0 {
					t.Error("inactive/invalid feature read environment")
				}
			})
		}
	}
}

// Requirement 10.2: even invalid legacy options must fail before environment
// discovery, independently of the new BetterLeaks switch.
func TestDetectorSelection_MultiUserMalformedOptionsNoEnvironment(t *testing.T) {
	t.Parallel()
	for _, betterleaks := range []string{"absent", "true", "false"} {
		for _, options := range []struct {
			name string
			yaml string
		}{
			{name: "non_mapping", yaml: "single_user: invalid\n"},
			{name: "non_list_include", yaml: "single_user:\n  include_env: invalid\n"},
			{name: "non_boolean_popular", yaml: "single_user:\n  include_popular_env: invalid\n"},
		} {
			t.Run(betterleaks+"/"+options.name, func(t *testing.T) {
				t.Parallel()
				env := &panicEnv{}
				reg := detectorSelectionRegistration(t, "action: block\n"+options.yaml+detectorSwitchYAML("betterleaks", betterleaks))
				out, err := detectorSelectionCompose(t, sgcompose.Input{
					AccessMode: accessmode.ModeMultiUser, Registrations: []aipsdk.Registration{reg}, Environment: env, Logger: discardLogger(),
				})
				if err == nil || out != nil {
					t.Error("malformed single_user options must reject the candidate")
				}
				if env.calls != 0 {
					t.Error("malformed multi_user candidate read environment")
				}
			})
		}
	}
}
