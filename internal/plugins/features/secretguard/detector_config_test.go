package secretguard

import (
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Detector keys are known configuration, not ignored extension data. Rejecting
// malformed enablement is necessary before presence-aware policy resolution.
func TestDetectorConfig_InvalidEnablement(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"auto_discovered_local_keys", "betterleaks"} {
		for _, value := range []string{"not-a-boolean", "[]", "{}", "null"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				t.Parallel()
				_, err := DecodeConfig(mustYAML(t, "action: block\n"+key+":\n  enabled: "+value+"\n"))
				if err == nil {
					t.Fatal("malformed detector enablement must fail configuration decode")
				}
			})
		}
	}
}

// Requirements 1.8 and 1.9: accepting both-off is deliberate, but does not
// weaken the existing required-action contract or implicitly choose an action.
func TestDetectorConfig_BothOffKeepsActionRequired(t *testing.T) {
	t.Parallel()
	const switches = "auto_discovered_local_keys:\n  enabled: false\nbetterleaks:\n  enabled: false\n"
	if _, err := DecodeConfig(mustYAML(t, switches)); err == nil {
		t.Fatal("both-off still requires an explicit action")
	}
	for _, action := range []string{ActionBlock, ActionRedact, ActionLog} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			cfg, err := DecodeConfig(mustYAML(t, "action: "+action+"\n"+switches))
			if err != nil {
				t.Fatal("both-off configuration must be accepted with an explicit valid action")
			}
			if cfg.Action != action {
				t.Error("detector switches changed the selected action")
			}
		})
	}
}

func TestComposeRuntimeConfig_ResolvesBetterLeaksPolicyDefaultsAndBounds(t *testing.T) {
	t.Parallel()

	regs := []aipsdk.Registration{{
		Kind:    aipsdk.PluginKindFeature,
		ID:      ID,
		Enabled: true,
		Config: aipsdk.ConfigPayload{Node: mustYAML(t, `
action: block
betterleaks:
  max_decode_depth: 0
  workers: 1
  disable_rules: [" zeta ", alpha, alpha]
`)},
	}}
	got, err := ComposeRuntimeConfig("single_user", regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LocalAutoDiscoveryEnabled || !got.BetterLeaksEnabled {
		t.Fatalf("resolved detector toggles: local=%t betterleaks=%t", got.LocalAutoDiscoveryEnabled, got.BetterLeaksEnabled)
	}
	if got.BetterLeaks.MinimumConfidence != "medium" {
		t.Fatalf("minimum confidence: got %q", got.BetterLeaks.MinimumConfidence)
	}
	if got.BetterLeaks.MaxDecodeDepth != 0 || got.BetterLeaks.Workers != 1 {
		t.Fatalf("resolved bounds: %#v", got.BetterLeaks)
	}
	if !reflect.DeepEqual(got.BetterLeaks.DisableRules, []string{"alpha", "zeta"}) {
		t.Fatalf("disable rules: %#v", got.BetterLeaks.DisableRules)
	}
	if len(got.BetterLeaks.IsolateRules) != 0 {
		t.Fatalf("isolate rules: %#v", got.BetterLeaks.IsolateRules)
	}
}

func TestComposeRuntimeConfig_RejectsInvalidBetterLeaksPolicy(t *testing.T) {
	t.Parallel()
	cases := []string{
		"minimum_confidence: none",
		"minimum_confidence: null",
		"minimum_confidence: ''",
		"max_decode_depth: -1",
		"max_decode_depth: 4",
		"max_decode_depth: null",
		"workers: -1",
		"workers: 65",
		"workers: null",
		"disable_rules: [alpha]\n  isolate_rules: [beta]",
		"disable_rules: ['   ']",
	}
	for _, betterLeaks := range cases {
		t.Run(betterLeaks, func(t *testing.T) {
			t.Parallel()
			regs := []aipsdk.Registration{{
				Kind:    aipsdk.PluginKindFeature,
				ID:      ID,
				Enabled: true,
				Config:  aipsdk.ConfigPayload{Node: mustYAML(t, "action: block\nbetterleaks:\n  "+betterLeaks+"\n")},
			}}
			if _, err := ComposeRuntimeConfig("single_user", regs); err == nil {
				t.Fatal("expected invalid BetterLeaks policy to fail composition")
			}
		})
	}
}

func TestComposeRuntimeConfig_CanonicalSelectorsShareConfigVersion(t *testing.T) {
	t.Parallel()
	compose := func(t *testing.T, selectors string) RuntimeConfig {
		t.Helper()
		regs := []aipsdk.Registration{{
			Kind:    aipsdk.PluginKindFeature,
			ID:      ID,
			Enabled: true,
			Config:  aipsdk.ConfigPayload{Node: mustYAML(t, "action: block\nbetterleaks:\n  disable_rules: "+selectors+"\n")},
		}}
		got, err := ComposeRuntimeConfig("single_user", regs)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := compose(t, "[zeta, alpha]")
	second := compose(t, "[' alpha ', zeta, alpha]")
	if first.AuditConfigVersion == "" {
		t.Fatal("canonical selector forms produced an empty config version")
	}
	if first.AuditConfigVersion != second.AuditConfigVersion {
		t.Fatalf("canonical selector forms produced different config versions: %q vs %q", first.AuditConfigVersion, second.AuditConfigVersion)
	}
	// Negative control: the version must fingerprint the effective policy, not
	// merely be stable across spellings. A constant version would make every
	// generation inherit the previous guard config.
	other := compose(t, "[alpha]")
	if other.AuditConfigVersion == first.AuditConfigVersion {
		t.Fatalf("different effective policies share config version %q", first.AuditConfigVersion)
	}
}
