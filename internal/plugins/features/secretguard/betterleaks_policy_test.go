package secretguard

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// These values are review ratchets for the pinned default detection policy.
// They intentionally describe only safe configuration metadata and contain no
// request or finding data.
const (
	betterLeaksDefaultVersion           = "v2.0.0-rc.2"
	betterLeaksDefaultConfigHash        = "86c537923c8763b0e1bfd5bc74280d33fabbdec98b629a4e9581f44d3bd4fad1"
	betterLeaksDefaultRuleCount         = 466
	betterLeaksDefaultRuleInventoryHash = "25d5a4953cdd4b22a133883458ff68b9e69cae3650faf8c85509478340561e56"
)

func TestNewBetterLeaksScanner_RejectsUnknownRuleWithoutEchoingSelector(t *testing.T) {
	t.Parallel()

	selector := "synthetic-selector-must-not-appear-in-errors"
	_, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		DisableRules:      []string{selector},
	})
	if err == nil {
		t.Fatal("unknown rule selector must fail scanner construction")
	}
	if strings.Contains(err.Error(), selector) {
		t.Fatalf("unknown selector was echoed in bounded error: %q", err)
	}
	if len(err.Error()) > 256 {
		t.Fatalf("unknown selector rejection is not bounded: %d bytes", len(err.Error()))
	}
}

func TestNewBetterLeaksScanner_RejectsMutuallyExclusiveRuleSelectors(t *testing.T) {
	t.Parallel()

	_, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		DisableRules:      []string{"aws-access-token"},
		IsolateRules:      []string{"aws-secret-access-key"},
	})
	if err == nil {
		t.Fatal("disable_rules and isolate_rules must be mutually exclusive")
	}
}

func TestNewBetterLeaksScanner_IsolationRetainsRequiredMultipartComponents(t *testing.T) {
	t.Parallel()

	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		IsolateRules:      []string{"aws-access-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	facts := detector.policyFacts()
	if !containsString(facts.RuleIDs, "aws-access-token") {
		t.Fatalf("isolated root missing from active rule inventory: %#v", facts.RuleIDs)
	}
	if !containsString(facts.RuleIDs, "aws-secret-access-key") {
		t.Fatalf("required multipart component missing from active rule inventory: %#v", facts.RuleIDs)
	}
	if containsString(facts.RuleIDs, "anthropic-api-key") {
		t.Fatalf("unselected rule leaked into isolated inventory: %#v", facts.RuleIDs)
	}
}

func TestNewBetterLeaksScanner_IsolationPrunesUnselectedOptionalComponents(t *testing.T) {
	t.Parallel()

	for _, root := range []string{"cloudflare-api-key.2", "generic-password"} {
		t.Run(root, func(t *testing.T) {
			detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: DefaultBetterLeaksConfidence,
				MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
				Workers:           1,
				IsolateRules:      []string{root},
			})
			if err != nil {
				t.Fatal(err)
			}
			facts := detector.policyFacts()
			if !containsString(facts.RuleIDs, root) {
				t.Fatalf("isolated root missing from active rule inventory: %#v", facts.RuleIDs)
			}
			for _, optional := range []string{"cloudflare-account-id.1", "generic-username"} {
				if containsString(facts.RuleIDs, optional) {
					t.Fatalf("unselected optional component %q became active for root %q: %#v", optional, root, facts.RuleIDs)
				}
			}
		})
	}
}

func TestNewBetterLeaksScanner_RejectsDisablingRequiredComponent(t *testing.T) {
	t.Parallel()

	_, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		DisableRules:      []string{"aws-secret-access-key"},
	})
	if err == nil || !strings.Contains(err.Error(), "required component") {
		t.Fatalf("disabling a required component must fail with a bounded dependency error: %v", err)
	}
}

func TestNewBetterLeaksScanner_AllowsComponentAndRequiredDependentsDisabledTogether(t *testing.T) {
	t.Parallel()

	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		DisableRules:      []string{"artifactory-api-key", "artifactory-jfrog-url", "artifactory-reference-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	facts := detector.policyFacts()
	for _, removed := range []string{"artifactory-api-key", "artifactory-jfrog-url", "artifactory-reference-token"} {
		if containsString(facts.RuleIDs, removed) {
			t.Fatalf("explicitly removed rule %q remained active", removed)
		}
	}
}

func TestNewBetterLeaksScanner_RejectsSharedRequiredComponentWhileAnyParentRemains(t *testing.T) {
	t.Parallel()

	_, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		DisableRules:      []string{"artifactory-jfrog-url"},
	})
	if err == nil || !strings.Contains(err.Error(), "required component") {
		t.Fatalf("shared required component removal must reject while a parent remains: %v", err)
	}
	if len(err.Error()) > 256 {
		t.Fatalf("required-component rejection is not bounded: %d bytes", len(err.Error()))
	}
}

func TestNewBetterLeaksScanner_OptionalComponentSelectionAndRemoval(t *testing.T) {
	t.Parallel()
	cfg, err := defaultBetterLeaksConfig()
	if err != nil {
		t.Fatal(err)
	}
	defaultParent, ok := cfg.Rule("cloudflare-api-key.2")
	if !ok {
		t.Fatal("missing cloudflare rule")
	}
	defaultOptional, ok := cfg.Rule("cloudflare-account-id.1")
	if !ok {
		t.Fatal("missing Cloudflare optional component rule")
	}

	base := BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
	}
	for _, test := range []struct {
		name       string
		selectors  []string
		isolate    bool
		wantParent bool
		wantPart   bool
	}{
		{name: "disable_optional", selectors: []string{"cloudflare-account-id.1"}, wantParent: true, wantPart: false},
		{name: "isolate_root_prunes_optional", selectors: []string{"cloudflare-api-key.2"}, isolate: true, wantParent: true, wantPart: false},
		{name: "isolate_explicit_optional", selectors: []string{"cloudflare-api-key.2", "cloudflare-account-id.1"}, isolate: true, wantParent: true, wantPart: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := base
			if test.isolate {
				policy.IsolateRules = test.selectors
			} else {
				policy.DisableRules = test.selectors
			}
			selected, _, err := resolveBetterLeaksConfig(cfg, policy)
			if err != nil {
				t.Fatal(err)
			}
			selectedParent, parentOK := selected.Rule("cloudflare-api-key.2")
			if parentOK != test.wantParent {
				t.Fatalf("parent active=%t, want %t", parentOK, test.wantParent)
			}
			_, partOK := selected.Rule("cloudflare-account-id.1")
			if partOK != test.wantPart {
				t.Fatalf("optional component active=%t, want %t", partOK, test.wantPart)
			}
			if parentOK {
				if selectedParent.Regex != defaultParent.Regex || selectedParent.FilterExpr != defaultParent.FilterExpr || selectedParent.Confidence != defaultParent.Confidence || selectedParent.SkipReport != defaultParent.SkipReport {
					t.Fatal("selected Cloudflare parent changed its pinned matching/reporting behavior")
				}
			}
			if test.wantPart {
				if !reflect.DeepEqual(selectedParent.Components, defaultParent.Components) || !reflect.DeepEqual(defaultOptional, betterLeaksRule(selected, "cloudflare-account-id.1")) {
					t.Fatal("explicitly selected optional component did not retain pinned behavior")
				}
			} else if parentOK && len(selectedParent.Components) != 0 {
				t.Fatal("excluded optional component reference was not pruned")
			}
		})
	}
}

func TestNewBetterLeaksScanner_IsolatedMultipartScanMatchesDefault(t *testing.T) {
	t.Parallel()

	defaultDetector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	isolatedDetector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		IsolateRules:      []string{"aws-access-token"},
	})
	if err != nil {
		t.Fatal(err)
	}

	defaultFindings := scanBetterLeaksTestFragment(t, defaultDetector, awsMultipartFragment)
	isolatedFindings := scanBetterLeaksTestFragment(t, isolatedDetector, awsMultipartFragment)
	defaultParent, defaultOK := policyFinding(defaultFindings, "aws-access-token")
	isolatedParent, isolatedOK := policyFinding(isolatedFindings, "aws-access-token")
	if !defaultOK || !isolatedOK {
		t.Fatalf("multipart parent finding retained=%t after isolation=%t", defaultOK, isolatedOK)
	}
	if defaultParent.Match.Value != isolatedParent.Match.Value {
		t.Fatal("isolation changed the retained multipart match")
	}
	if componentFindingIDs(defaultParent) == nil || componentFindingIDs(isolatedParent) == nil {
		t.Fatal("multipart parent must retain component reporting")
	}
	if !equalStrings(componentFindingIDs(defaultParent), componentFindingIDs(isolatedParent)) {
		t.Fatalf("isolation changed multipart component reporting: default=%v isolated=%v", componentFindingIDs(defaultParent), componentFindingIDs(isolatedParent))
	}
}

func TestNewBetterLeaksScanner_PolicyFactsAreDefensive(t *testing.T) {
	t.Parallel()

	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := detector.policyFacts()
	if len(first.RuleIDs) == 0 {
		t.Fatal("default policy facts must include rule IDs")
	}
	first.RuleIDs[0] = "mutated"
	second := detector.policyFacts()
	if second.RuleIDs[0] == "mutated" {
		t.Fatal("policy facts exposed mutable generation state")
	}
}

func TestNewBetterLeaksScanner_FailedCandidateLeavesExistingPolicyUntouched(t *testing.T) {
	t.Parallel()

	cfg, err := defaultBetterLeaksConfig()
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := cfg.Hash()
	existing, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	existingFacts := existing.policyFacts()

	_, err = newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		DisableRules:      []string{"candidate-selector-that-is-not-pinned"},
	})
	if err == nil {
		t.Fatal("invalid candidate policy must be rejected")
	}
	if got := cfg.Hash(); got != beforeHash {
		t.Fatal("failed candidate resolution mutated the previously loaded configuration")
	}
	if got := existing.policyFacts(); got.ConfigHash != existingFacts.ConfigHash || got.ActiveRuleCount != existingFacts.ActiveRuleCount || !equalStrings(got.RuleIDs, existingFacts.RuleIDs) {
		t.Fatal("failed candidate resolution changed the existing policy facts")
	}
}

func TestNewBetterLeaksScanner_CanonicalizesSelectorsIntoOnePolicyIdentity(t *testing.T) {
	t.Parallel()

	first, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		DisableRules:      []string{" aws-secret-access-key ", "aws-access-token", "aws-access-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		DisableRules:      []string{"aws-access-token", "aws-secret-access-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.policyFacts().ConfigHash != second.policyFacts().ConfigHash {
		t.Fatalf("equivalent selector spellings produced different policy hashes: %q vs %q", first.policyFacts().ConfigHash, second.policyFacts().ConfigHash)
	}
	if got, want := first.policyFacts().RuleIDs, second.policyFacts().RuleIDs; !equalStrings(got, want) {
		t.Fatalf("equivalent selector spellings produced different inventories: %#v vs %#v", got, want)
	}
	for _, disabled := range []string{"aws-access-token", "aws-secret-access-key"} {
		if containsString(first.policyFacts().RuleIDs, disabled) {
			t.Fatalf("disabled rule %q remained in active inventory", disabled)
		}
	}
}

func TestNewBetterLeaksScanner_DefaultPolicyRatchet(t *testing.T) {
	t.Parallel()

	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	facts := detector.policyFacts()
	if facts.Version != betterLeaksDefaultVersion {
		t.Fatalf("BetterLeaks version changed: got %q want %q", facts.Version, betterLeaksDefaultVersion)
	}
	if facts.ConfigHash != betterLeaksDefaultConfigHash {
		t.Fatalf("default BetterLeaks policy hash changed: got %q want %q", facts.ConfigHash, betterLeaksDefaultConfigHash)
	}
	if facts.ActiveRuleCount != betterLeaksDefaultRuleCount {
		t.Fatalf("default BetterLeaks rule count changed: got %d want %d", facts.ActiveRuleCount, betterLeaksDefaultRuleCount)
	}
	if facts.MinimumConfidence != DefaultBetterLeaksConfidence || facts.MaxDecodeDepth != DefaultBetterLeaksDecodeDepth || facts.Workers != 1 {
		t.Fatalf("default BetterLeaks tuning facts changed: %#v", facts)
	}
	if facts.RuleInventoryHash != betterLeaksDefaultRuleInventoryHash {
		t.Fatalf("default BetterLeaks rule inventory changed: got %q want %q", facts.RuleInventoryHash, betterLeaksDefaultRuleInventoryHash)
	}
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const awsMultipartFragment = "aws_token := \"AKIALALEMEL33243OLIA\" aws_secret_access_key = \"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\""
