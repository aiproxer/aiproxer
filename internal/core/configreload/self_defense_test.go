package configreload

import (
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestClassifySelfDefensePolicyFieldsReloadable(t *testing.T) {
	t.Parallel()

	active := &config.Config{}
	candidate := &config.Config{}
	disabled := false
	impossible := false
	authFailures := 8
	candidate.Access.SelfDefense.Enabled = &disabled
	candidate.Access.SelfDefense.ImpossiblePaths = &impossible
	candidate.Access.SelfDefense.Adaptive.AuthFailures = &authFailures
	candidate.Access.SelfDefense.Adaptive.Window = "30s"
	candidate.Access.SelfDefense.Adaptive.InitialQuarantine = "5m"
	candidate.Access.SelfDefense.Adaptive.MaxQuarantine = "4h"
	candidate.Access.SelfDefense.Adaptive.ExemptCIDRs = []string{"203.0.113.0/24"}

	changes, err := Classify(active, candidate)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := []string{
		"access.self_defense.adaptive.auth_failures",
		"access.self_defense.adaptive.exempt_cidrs",
		"access.self_defense.adaptive.initial_quarantine",
		"access.self_defense.adaptive.max_quarantine",
		"access.self_defense.adaptive.window",
		"access.self_defense.enabled",
		"access.self_defense.impossible_paths",
	}
	got := make([]string, 0, len(changes))
	for _, change := range changes {
		if change.Disposition != ChangeReloadable {
			t.Fatalf("change %q disposition = %q, want reloadable", change.Path, change.Disposition)
		}
		got = append(got, change.Path)
	}
	if len(got) != len(want) {
		t.Fatalf("reloadable changes = %v, want exactly %d paths %v", got, len(want), want)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("reloadable change set = %v, want exactly %v", got, want)
	}
}

func TestClassifySelfDefenseStateFieldsRestartRequired(t *testing.T) {
	t.Parallel()

	active := &config.Config{}
	candidate := &config.Config{}
	maxEntries := 200000
	candidate.Access.SelfDefense.Adaptive.StateTTL = "48h"
	candidate.Access.SelfDefense.Adaptive.MaxEntries = &maxEntries

	_, err := Classify(active, candidate)
	if err == nil {
		t.Fatal("Classify succeeded, want restart-required error")
	}
	restart, ok := err.(*RestartRequiredError)
	if !ok {
		t.Fatalf("error type = %T, want *RestartRequiredError", err)
	}
	for _, path := range []string{
		"access.self_defense.adaptive.state_ttl",
		"access.self_defense.adaptive.max_entries",
	} {
		if !slices.Contains(restart.RestartRequiredFields, path) {
			t.Errorf("restart fields %v missing %q", restart.RestartRequiredFields, path)
		}
	}
}

// TestClassifySelfDefenseEquivalentStateLimitsAreNotRestartRequired pins the
// equivalence the operator documentation promises: omitting the whole
// access.self_defense block, spelling the documented default, and spelling an
// equivalent duration all compile to the same process-owned limits, so none of
// them may be classified as restart-required. These are the same effective
// process limits, and a default-on feature whose documentation says "omitted is
// identical to the default" must not reject a reload that only spells the
// default out.
func TestClassifySelfDefenseEquivalentStateLimitsAreNotRestartRequired(t *testing.T) {
	t.Parallel()

	defaultEntries := 100000
	omitted := func() *config.Config { return &config.Config{} }
	explicit := func() *config.Config {
		cfg := &config.Config{}
		cfg.Access.SelfDefense.Adaptive.StateTTL = "24h"
		cfg.Access.SelfDefense.Adaptive.MaxEntries = &defaultEntries
		return cfg
	}
	equivalent := func() *config.Config {
		cfg := &config.Config{}
		cfg.Access.SelfDefense.Adaptive.StateTTL = "1440m"
		cfg.Access.SelfDefense.Adaptive.MaxEntries = &defaultEntries
		return cfg
	}

	for _, tc := range []struct {
		name              string
		active, candidate *config.Config
	}{
		{"omitted to explicit defaults", omitted(), explicit()},
		{"explicit defaults to omitted", explicit(), omitted()},
		{"equivalent duration spelling", explicit(), equivalent()},
		{"omitted to equivalent duration", omitted(), equivalent()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			changes, err := Classify(tc.active, tc.candidate)
			if err != nil {
				t.Fatalf("equivalent effective state limits must not be restart-required: %v", err)
			}
			for _, change := range changes {
				if change.Path == "access.self_defense.adaptive.state_ttl" ||
					change.Path == "access.self_defense.adaptive.max_entries" {
					t.Errorf("published a change for %q, want none: the effective limits are identical", change.Path)
				}
			}
		})
	}
}

// TestClassifySelfDefenseChangedStateLimitsRemainRestartRequired keeps the
// effective-value comparison honest: a candidate whose COMPILED limits really do
// differ must still be rejected, so comparing compiled values cannot silently
// downgrade a genuine resize to a reload.
func TestClassifySelfDefenseChangedStateLimitsRemainRestartRequired(t *testing.T) {
	t.Parallel()

	maxEntries200k := 200000
	for _, tc := range []struct {
		name      string
		candidate *config.Config
		want      string
	}{
		{"state ttl", selfDefenseConfigWith(func(a *config.SelfDefenseAdaptiveConfig) { a.StateTTL = "48h" }), "access.self_defense.adaptive.state_ttl"},
		{"max entries", selfDefenseConfigWith(func(a *config.SelfDefenseAdaptiveConfig) { a.MaxEntries = &maxEntries200k }), "access.self_defense.adaptive.max_entries"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Classify(&config.Config{}, tc.candidate)
			restart, ok := err.(*RestartRequiredError)
			if !ok {
				t.Fatalf("error = %v, want *RestartRequiredError", err)
			}
			if !slices.Contains(restart.RestartRequiredFields, tc.want) {
				t.Fatalf("restart fields %v missing %q", restart.RestartRequiredFields, tc.want)
			}
		})
	}
}

// selfDefenseConfigWith builds a config whose only non-default content is the
// self-defense adaptive block the callback sets, so each case changes exactly one
// restart-required field.
func selfDefenseConfigWith(apply func(*config.SelfDefenseAdaptiveConfig)) *config.Config {
	cfg := &config.Config{}
	apply(&cfg.Access.SelfDefense.Adaptive)
	return cfg
}

func TestClassifySelfDefenseMixedCandidateRejectsAtomically(t *testing.T) {
	t.Parallel()

	active := &config.Config{}
	candidate := &config.Config{}
	disabled := false
	candidate.Access.SelfDefense.Enabled = &disabled
	candidate.Access.SelfDefense.Adaptive.StateTTL = "48h"

	changes, err := Classify(active, candidate)
	if err == nil {
		t.Fatalf("mixed candidate must reject, got changes=%v", changes)
	}
	if changes != nil {
		t.Fatalf("mixed reject must not publish reloadable changes, got %v", changes)
	}
	restart, ok := err.(*RestartRequiredError)
	if !ok {
		t.Fatalf("error type = %T, want *RestartRequiredError", err)
	}
	if !slices.Contains(restart.RestartRequiredFields, "access.self_defense.adaptive.state_ttl") {
		t.Fatalf("want state_ttl restart-required, got %v", restart.RestartRequiredFields)
	}
	if slices.Contains(restart.RestartRequiredFields, "access.self_defense.enabled") {
		t.Fatalf("reloadable enabled published as restart-required: %v", restart.RestartRequiredFields)
	}
}

func TestClassifySelfDefenseNoop(t *testing.T) {
	t.Parallel()

	active := &config.Config{}
	candidate := &config.Config{}

	changes, err := Classify(active, candidate)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if changes != nil {
		t.Fatalf("noop must return nil changes, got %v", changes)
	}
}
