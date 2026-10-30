package config

import (
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
)

func TestCompileSelfDefenseDefaultsWhenOmitted(t *testing.T) {
	t.Parallel()

	compiled, err := CompileSelfDefense(SelfDefenseConfig{})
	if err != nil {
		t.Fatalf("CompileSelfDefense: %v", err)
	}
	if !compiled.Enabled() {
		t.Error("omitted enabled must default to true")
	}
	if !compiled.ImpossiblePaths() {
		t.Error("omitted impossible_paths must default to true")
	}
	if got := compiled.AuthFailures(); got != 5 {
		t.Errorf("auth failures = %d, want 5", got)
	}
	if got := compiled.FailureWindow(); got != time.Minute {
		t.Errorf("failure window = %s, want 1m", got)
	}
	if got := compiled.InitialQuarantine(); got != time.Minute {
		t.Errorf("initial quarantine = %s, want 1m", got)
	}
	if got := compiled.MaxQuarantine(); got != 2*time.Hour {
		t.Errorf("max quarantine = %s, want 2h", got)
	}
	if got := compiled.StateTTL(); got != 24*time.Hour {
		t.Errorf("state ttl = %s, want 24h", got)
	}
	if got := compiled.MaxEntries(); got != 100000 {
		t.Errorf("max entries = %d, want 100000", got)
	}
	if got := compiled.AdaptiveExemptCIDRs(); len(got) != 0 {
		t.Errorf("exempt cidrs = %v, want empty", got)
	}
}

func TestCompileSelfDefensePreservesExplicitFalse(t *testing.T) {
	t.Parallel()

	disabled := false
	compiled, err := CompileSelfDefense(SelfDefenseConfig{
		Enabled:         &disabled,
		ImpossiblePaths: &disabled,
	})
	if err != nil {
		t.Fatalf("CompileSelfDefense: %v", err)
	}
	if compiled.Enabled() {
		t.Error("explicit enabled=false must be preserved")
	}
	if compiled.ImpossiblePaths() {
		t.Error("explicit impossible_paths=false must be preserved")
	}
}

func TestCompileSelfDefenseRejectsOutOfBounds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		adaptive SelfDefenseAdaptiveConfig
		wantPath string
	}{
		{name: "auth failures zero", adaptive: SelfDefenseAdaptiveConfig{AuthFailures: new(0)}, wantPath: "access.self_defense.adaptive.auth_failures"},
		{name: "auth failures below min", adaptive: SelfDefenseAdaptiveConfig{AuthFailures: new(1)}, wantPath: "access.self_defense.adaptive.auth_failures"},
		{name: "auth failures above max", adaptive: SelfDefenseAdaptiveConfig{AuthFailures: new(101)}, wantPath: "access.self_defense.adaptive.auth_failures"},
		{name: "window below min", adaptive: SelfDefenseAdaptiveConfig{Window: "999ms"}, wantPath: "access.self_defense.adaptive.window"},
		{name: "window above max", adaptive: SelfDefenseAdaptiveConfig{Window: "1h1s"}, wantPath: "access.self_defense.adaptive.window"},
		{name: "window malformed", adaptive: SelfDefenseAdaptiveConfig{Window: "soon"}, wantPath: "access.self_defense.adaptive.window"},
		{name: "window not positive", adaptive: SelfDefenseAdaptiveConfig{Window: "0s"}, wantPath: "access.self_defense.adaptive.window"},
		{name: "window negative", adaptive: SelfDefenseAdaptiveConfig{Window: "-1s"}, wantPath: "access.self_defense.adaptive.window"},
		{name: "initial below min", adaptive: SelfDefenseAdaptiveConfig{InitialQuarantine: "999ms"}, wantPath: "access.self_defense.adaptive.initial_quarantine"},
		{name: "initial above max", adaptive: SelfDefenseAdaptiveConfig{InitialQuarantine: "1h1s"}, wantPath: "access.self_defense.adaptive.initial_quarantine"},
		{name: "initial not positive", adaptive: SelfDefenseAdaptiveConfig{InitialQuarantine: "0s"}, wantPath: "access.self_defense.adaptive.initial_quarantine"},
		{name: "max below initial", adaptive: SelfDefenseAdaptiveConfig{InitialQuarantine: "1h", MaxQuarantine: "30m"}, wantPath: "access.self_defense.adaptive.max_quarantine"},
		{name: "max above bound", adaptive: SelfDefenseAdaptiveConfig{MaxQuarantine: "24h1s"}, wantPath: "access.self_defense.adaptive.max_quarantine"},
		{name: "max not positive", adaptive: SelfDefenseAdaptiveConfig{MaxQuarantine: "0s"}, wantPath: "access.self_defense.adaptive.max_quarantine"},
		{name: "max negative", adaptive: SelfDefenseAdaptiveConfig{MaxQuarantine: "-1s"}, wantPath: "access.self_defense.adaptive.max_quarantine"},
		{name: "max below implied one second floor", adaptive: SelfDefenseAdaptiveConfig{MaxQuarantine: "500ms"}, wantPath: "access.self_defense.adaptive.max_quarantine"},
		{name: "state ttl below min", adaptive: SelfDefenseAdaptiveConfig{StateTTL: "59s"}, wantPath: "access.self_defense.adaptive.state_ttl"},
		{name: "state ttl above max", adaptive: SelfDefenseAdaptiveConfig{StateTTL: "169h"}, wantPath: "access.self_defense.adaptive.state_ttl"},
		{name: "state ttl not positive", adaptive: SelfDefenseAdaptiveConfig{StateTTL: "0s"}, wantPath: "access.self_defense.adaptive.state_ttl"},
		{name: "state ttl unparsable day unit", adaptive: SelfDefenseAdaptiveConfig{StateTTL: "7d"}, wantPath: "access.self_defense.adaptive.state_ttl"},
		{name: "max entries below min", adaptive: SelfDefenseAdaptiveConfig{MaxEntries: new(1023)}, wantPath: "access.self_defense.adaptive.max_entries"},
		{name: "max entries above max", adaptive: SelfDefenseAdaptiveConfig{MaxEntries: new(1000001)}, wantPath: "access.self_defense.adaptive.max_entries"},
		{name: "invalid exempt cidr", adaptive: SelfDefenseAdaptiveConfig{ExemptCIDRs: []string{"not-a-cidr"}}, wantPath: "access.self_defense.adaptive.exempt_cidrs[0]"},
		{name: "empty exempt cidr", adaptive: SelfDefenseAdaptiveConfig{ExemptCIDRs: []string{""}}, wantPath: "access.self_defense.adaptive.exempt_cidrs[0]"},
		{name: "out of range exempt prefix", adaptive: SelfDefenseAdaptiveConfig{ExemptCIDRs: []string{"10.0.0.0/33"}}, wantPath: "access.self_defense.adaptive.exempt_cidrs[0]"},
		{name: "second exempt entry reports its index", adaptive: SelfDefenseAdaptiveConfig{ExemptCIDRs: []string{"192.0.2.0/24", "bogus"}}, wantPath: "access.self_defense.adaptive.exempt_cidrs[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := CompileSelfDefense(SelfDefenseConfig{Adaptive: tc.adaptive})
			if err == nil {
				t.Fatal("CompileSelfDefense succeeded, want validation error")
			}
			if !strings.Contains(err.Error(), tc.wantPath) {
				t.Fatalf("error = %v, want offending field path %q", err, tc.wantPath)
			}
		})
	}
}

func TestCompileSelfDefenseAcceptsInclusiveBounds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		adaptive SelfDefenseAdaptiveConfig
	}{
		{name: "auth at min", adaptive: SelfDefenseAdaptiveConfig{AuthFailures: new(2)}},
		{name: "auth at max", adaptive: SelfDefenseAdaptiveConfig{AuthFailures: new(100)}},
		{name: "window at min", adaptive: SelfDefenseAdaptiveConfig{Window: "1s"}},
		{name: "window at max", adaptive: SelfDefenseAdaptiveConfig{Window: "1h"}},
		{name: "initial at min", adaptive: SelfDefenseAdaptiveConfig{InitialQuarantine: "1s"}},
		{name: "initial at max with equal max", adaptive: SelfDefenseAdaptiveConfig{InitialQuarantine: "1h", MaxQuarantine: "1h"}},
		{name: "max at bound", adaptive: SelfDefenseAdaptiveConfig{MaxQuarantine: "24h"}},
		{name: "max at implied one second floor with equal initial", adaptive: SelfDefenseAdaptiveConfig{InitialQuarantine: "1s", MaxQuarantine: "1s"}},
		{name: "state ttl at min", adaptive: SelfDefenseAdaptiveConfig{StateTTL: "1m"}},
		{name: "state ttl at max", adaptive: SelfDefenseAdaptiveConfig{StateTTL: "168h"}},
		{name: "max entries at min", adaptive: SelfDefenseAdaptiveConfig{MaxEntries: new(1024)}},
		{name: "max entries at max", adaptive: SelfDefenseAdaptiveConfig{MaxEntries: new(1000000)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := CompileSelfDefense(SelfDefenseConfig{Adaptive: tc.adaptive}); err != nil {
				t.Fatalf("CompileSelfDefense: %v", err)
			}
		})
	}
}

func TestCompileSelfDefenseNormalizesExemptCIDRs(t *testing.T) {
	t.Parallel()

	compiled, err := CompileSelfDefense(SelfDefenseConfig{Adaptive: SelfDefenseAdaptiveConfig{
		ExemptCIDRs: []string{"192.0.2.0/24", "2001:db8::/32", "203.0.113.7", "::ffff:198.51.100.0/120"},
	}})
	if err != nil {
		t.Fatalf("CompileSelfDefense: %v", err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("203.0.113.7/32"),
		netip.MustParsePrefix("198.51.100.0/24"),
	}
	if got := compiled.AdaptiveExemptCIDRs(); !slices.Equal(got, want) {
		t.Fatalf("exempt cidrs = %v, want %v", got, want)
	}
}

func TestCompiledSelfDefenseExemptCIDRsAreDefensiveCopy(t *testing.T) {
	t.Parallel()

	compiled, err := CompileSelfDefense(SelfDefenseConfig{Adaptive: SelfDefenseAdaptiveConfig{
		ExemptCIDRs: []string{"192.0.2.0/24"},
	}})
	if err != nil {
		t.Fatalf("CompileSelfDefense: %v", err)
	}
	first := compiled.AdaptiveExemptCIDRs()
	first[0] = netip.MustParsePrefix("10.0.0.0/8")
	if got := compiled.AdaptiveExemptCIDRs(); got[0] != netip.MustParsePrefix("192.0.2.0/24") {
		t.Fatalf("accessor did not return a defensive copy: %v", got)
	}
}

func TestCompileSelfDefenseProjectsCorePolicyAndStateLimits(t *testing.T) {
	t.Parallel()

	compiled, err := CompileSelfDefense(SelfDefenseConfig{Adaptive: SelfDefenseAdaptiveConfig{
		ExemptCIDRs: []string{"192.0.2.0/24"},
	}})
	if err != nil {
		t.Fatalf("CompileSelfDefense: %v", err)
	}
	policy := compiled.Policy()
	if !policy.Enabled {
		t.Error("projected policy must carry the enabled default")
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("projected policy must satisfy the core domain invariants: %v", err)
	}
	if policy.AuthFailures != 5 || policy.FailureWindow != time.Minute ||
		policy.InitialQuarantine != time.Minute || policy.MaxQuarantine != 2*time.Hour {
		t.Fatalf("projected policy scalars = %+v", policy)
	}
	if !policy.AdaptiveExempt(netip.MustParseAddr("192.0.2.9")) {
		t.Error("projected policy must carry the adaptive exemption allowlist")
	}
	policyType := reflect.TypeFor[ingressdefense.Policy]()
	for _, processOnly := range []string{"StateTTL", "MaxEntries"} {
		if _, ok := policyType.FieldByName(processOnly); ok {
			t.Fatalf("Policy must not carry process-state field %s; state sizing belongs to StateLimits", processOnly)
		}
	}

	limits := compiled.StateLimits()
	if err := limits.Validate(); err != nil {
		t.Fatalf("projected state limits must satisfy the core domain invariants: %v", err)
	}
	if limits.MaxEntries != 100000 || limits.StateTTL != 24*time.Hour {
		t.Fatalf("projected state limits = %+v", limits)
	}
	if limits.MaxEntries != compiled.MaxEntries() || limits.StateTTL != compiled.StateTTL() {
		t.Fatal("projected state limits must match the compiled process-state fields")
	}

	policy.AdaptiveExemptCIDRs[0] = netip.MustParsePrefix("10.0.0.0/8")
	if compiled.Policy().AdaptiveExempt(netip.MustParseAddr("192.0.2.9")) != true {
		t.Fatal("projected policy must not alias the compiled exemption allowlist")
	}
}

func TestCompiledSelfDefenseNilReceiverAccessors(t *testing.T) {
	t.Parallel()

	var compiled *CompiledSelfDefense
	if compiled.Enabled() || compiled.ImpossiblePaths() {
		t.Fatal("nil receiver must report disabled")
	}
	if compiled.AuthFailures() != 0 || compiled.MaxEntries() != 0 {
		t.Fatal("nil receiver must report zero counts")
	}
	if compiled.FailureWindow() != 0 || compiled.InitialQuarantine() != 0 ||
		compiled.MaxQuarantine() != 0 || compiled.StateTTL() != 0 {
		t.Fatal("nil receiver must report zero durations")
	}
	if compiled.AdaptiveExemptCIDRs() != nil {
		t.Fatal("nil receiver must report no exempt cidrs")
	}
	if policy := compiled.Policy(); policy.Enabled || policy.AuthFailures != 0 || policy.AdaptiveExemptCIDRs != nil {
		t.Fatalf("nil receiver must project a zero policy, got %+v", policy)
	}
	if limits := compiled.StateLimits(); limits.MaxEntries != 0 || limits.StateTTL != 0 {
		t.Fatalf("nil receiver must project zero state limits, got %+v", limits)
	}
}

func TestStrictDecodeSelfDefensePresenceAware(t *testing.T) {
	t.Parallel()

	omitted, _, err := StrictDecode([]byte("access:\n  self_defense:\n    adaptive:\n      auth_failures: 5\n"))
	if err != nil {
		t.Fatalf("StrictDecode omitted: %v", err)
	}
	if omitted.Access.SelfDefense.Enabled != nil {
		t.Fatal("omitted enabled must decode as nil (presence-aware)")
	}
	if omitted.Access.SelfDefense.ImpossiblePaths != nil {
		t.Fatal("omitted impossible_paths must decode as nil (presence-aware)")
	}
	compiledOmitted, err := CompileSelfDefense(omitted.Access.SelfDefense)
	if err != nil {
		t.Fatalf("CompileSelfDefense omitted: %v", err)
	}
	if !compiledOmitted.Enabled() || !compiledOmitted.ImpossiblePaths() {
		t.Fatal("omitted booleans must resolve to enabled defaults")
	}

	explicit, _, err := StrictDecode([]byte("access:\n  self_defense:\n    enabled: false\n    impossible_paths: false\n"))
	if err != nil {
		t.Fatalf("StrictDecode explicit: %v", err)
	}
	if explicit.Access.SelfDefense.Enabled == nil || *explicit.Access.SelfDefense.Enabled {
		t.Fatal("explicit enabled=false must decode as a present false pointer")
	}
	if explicit.Access.SelfDefense.ImpossiblePaths == nil || *explicit.Access.SelfDefense.ImpossiblePaths {
		t.Fatal("explicit impossible_paths=false must decode as a present false pointer")
	}
	compiledExplicit, err := CompileSelfDefense(explicit.Access.SelfDefense)
	if err != nil {
		t.Fatalf("CompileSelfDefense explicit: %v", err)
	}
	if compiledExplicit.Enabled() || compiledExplicit.ImpossiblePaths() {
		t.Fatal("explicit false must compile disabled")
	}
}

func TestValidateRejectsInvalidSelfDefense(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Continuity: ContinuityConfig{InMemory: true},
		Plugins:    PluginsConfig{Backends: []PluginConfig{{ID: "b1", Enabled: true}}},
		Access: AccessConfig{SelfDefense: SelfDefenseConfig{
			Adaptive: SelfDefenseAdaptiveConfig{AuthFailures: new(1)},
		}},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("Validate succeeded, want self-defense validation error")
	}
	if !strings.Contains(err.Error(), "self_defense") {
		t.Fatalf("error = %v, want access.self_defense path", err)
	}
}

func TestValidateAcceptsOmittedSelfDefense(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Continuity: ContinuityConfig{InMemory: true},
		Plugins:    PluginsConfig{Backends: []PluginConfig{{ID: "b1", Enabled: true}}},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
