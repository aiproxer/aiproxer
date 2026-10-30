package secretguard

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestDecodeConfig_DetectorControlsPreservePresence characterizes the
// presence-aware detector configuration described by the BetterLeaks
// secret-guard design. Ordinary bool zero values cannot distinguish an omitted
// switch from an explicit false; composition needs that distinction to resolve
// access-mode-dependent defaults. The decoded *bool is that contract: nil means
// omitted, a non-nil pointer carries the operator's explicit choice.
func TestDecodeConfig_DetectorControlsPreservePresence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		yaml        string
		wantPresent bool
		wantValue   bool
	}{
		{name: "absent", yaml: "action: block\n", wantPresent: false},
		{name: "true", yaml: "action: block\nauto_discovered_local_keys:\n  enabled: true\n", wantPresent: true, wantValue: true},
		{name: "false", yaml: "action: block\nauto_discovered_local_keys:\n  enabled: false\n", wantPresent: true, wantValue: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := DecodeConfig(mustYAML(t, tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			assertDetectorEnabledPresence(t, "AutoDiscoveredLocalKeys", cfg.AutoDiscoveredLocalKeys.Enabled, tc.wantPresent, tc.wantValue)

			cfg, err = DecodeConfig(mustYAML(t, strings.Replace(tc.yaml, "auto_discovered_local_keys", "betterleaks", 1)))
			if err != nil {
				t.Fatal(err)
			}
			assertDetectorEnabledPresence(t, "BetterLeaks", cfg.BetterLeaks.Enabled, tc.wantPresent, tc.wantValue)
		})
	}
}

// TestComposeRuntimeConfig_ResolvesDetectorDefaultsByAccessMode records the
// effective detector posture required at generation composition.
func TestComposeRuntimeConfig_ResolvesDetectorDefaultsByAccessMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		accessMode    string
		local         string
		betterLeaks   string
		wantLocal     bool
		wantBetter    bool
		wantErrorPart string
	}{
		{name: "single_absent_absent", accessMode: "single_user", local: "", betterLeaks: "", wantLocal: true, wantBetter: true},
		{name: "single_false_true", accessMode: "single_user", local: "false", betterLeaks: "true", wantLocal: false, wantBetter: true},
		{name: "single_true_false", accessMode: "single_user", local: "true", betterLeaks: "false", wantLocal: true, wantBetter: false},
		{name: "multi_absent_absent", accessMode: "multi_user", local: "", betterLeaks: "", wantLocal: false, wantBetter: true},
		{name: "multi_false_false", accessMode: "multi_user", local: "false", betterLeaks: "false", wantLocal: false, wantBetter: false},
		{name: "multi_true_absent_rejected", accessMode: "multi_user", local: "true", betterLeaks: "", wantErrorPart: "auto_discovered_local_keys"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := "action: block\n"
			if tc.local != "" {
				raw += "auto_discovered_local_keys:\n  enabled: " + tc.local + "\n"
			}
			if tc.betterLeaks != "" {
				raw += "betterleaks:\n  enabled: " + tc.betterLeaks + "\n"
			}
			regs := []aipsdk.Registration{{
				Kind: aipsdk.PluginKindFeature, ID: "detector-policy", FactoryKind: ID, Enabled: true,
				Config: aipsdk.ConfigPayload{Node: mustYAML(t, raw)},
			}}
			got, err := ComposeRuntimeConfig(tc.accessMode, regs)
			if tc.wantErrorPart != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrorPart) {
					t.Fatalf("expected bounded configuration error containing %q, got %v", tc.wantErrorPart, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.LocalAutoDiscoveryEnabled != tc.wantLocal {
				t.Fatalf("RuntimeConfig.LocalAutoDiscoveryEnabled=%t, want %t", got.LocalAutoDiscoveryEnabled, tc.wantLocal)
			}
			if got.BetterLeaksEnabled != tc.wantBetter {
				t.Fatalf("RuntimeConfig.BetterLeaksEnabled=%t, want %t", got.BetterLeaksEnabled, tc.wantBetter)
			}
		})
	}
}

// assertDetectorEnabledPresence asserts the decoded presence contract directly:
// nil means the operator omitted the switch, a non-nil pointer carries the
// explicit value.
func assertDetectorEnabledPresence(t *testing.T, fieldName string, enabled *bool, wantPresent, wantValue bool) {
	t.Helper()
	if (enabled != nil) != wantPresent {
		t.Fatalf("Config.%s.Enabled presence=%t, want %t", fieldName, enabled != nil, wantPresent)
	}
	if wantPresent && *enabled != wantValue {
		t.Fatalf("Config.%s.Enabled=%t, want %t", fieldName, *enabled, wantValue)
	}
}
