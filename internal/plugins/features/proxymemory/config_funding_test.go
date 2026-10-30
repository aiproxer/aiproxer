package proxymemory

import (
	"errors"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestConfigFundingDecisionVocabulary pins the operator-declared funding
// vocabulary of the feature configuration.
//
// The vocabulary is closed and carries no money shape: the host owns the
// funding machinery, and the feature never resolves an account. An operator
// who declared no funding mode leaves the background work undeclared, and an
// undeclared mode is refused by the host before any provider dispatch.
func TestConfigFundingDecisionVocabulary(t *testing.T) {
	t.Run("the default declares no funding mode", func(t *testing.T) {
		cfg := DefaultConfig()
		if cfg.FundingMode != FundingModeUndeclared {
			t.Fatalf("the default funding mode = %q, want the undeclared mode", cfg.FundingMode)
		}
		if cfg.SponsorKey != "" || len(cfg.SponsorKeys) != 0 {
			t.Fatalf("the default declares a sponsor: %q %v", cfg.SponsorKey, cfg.SponsorKeys)
		}
	})

	t.Run("the closed vocabulary decodes", func(t *testing.T) {
		for _, mode := range []FundingMode{FundingModeCustomerFunded, FundingModeOperatorSponsored} {
			cfg, err := DecodeConfig(mapToNode(t, configRequired+"funding_mode: "+string(mode)+"\n"))
			if err != nil {
				t.Fatalf("DecodeConfig(funding_mode=%q) failed: %v", mode, err)
			}
			if cfg.FundingMode != mode {
				t.Fatalf("the decoded funding mode = %q, want %q", cfg.FundingMode, mode)
			}
		}
	})

	t.Run("an unknown funding mode is rejected", func(t *testing.T) {
		for _, mode := range []string{"customer-funded", "free", "CUSTOMER_FUNDED", "usage_only"} {
			_, err := DecodeConfig(mapToNode(t, configRequired+"funding_mode: "+mode+"\n"))
			if err == nil {
				t.Fatalf("DecodeConfig(funding_mode=%q) was accepted", mode)
			}
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Field != "funding_mode" {
				t.Fatalf("DecodeConfig(funding_mode=%q) error = %v, want a funding_mode rejection", mode, err)
			}
		}
	})

	t.Run("a declared sponsor key decodes", func(t *testing.T) {
		cfg, err := DecodeConfig(mapToNode(t, configRequired+
			"funding_mode: operator_sponsored\nsponsor_keys: [memory-team, ops-team]\nsponsor_key: ops-team\n"))
		if err != nil {
			t.Fatalf("DecodeConfig failed: %v", err)
		}
		if cfg.SponsorKey != "ops-team" {
			t.Fatalf("the decoded sponsor key = %q, want %q", cfg.SponsorKey, "ops-team")
		}
		if len(cfg.SponsorKeys) != 2 {
			t.Fatalf("the decoded sponsor keys = %v, want two keys", cfg.SponsorKeys)
		}
	})

	t.Run("a sponsor key the operator did not declare is rejected", func(t *testing.T) {
		_, err := DecodeConfig(mapToNode(t, configRequired+
			"funding_mode: operator_sponsored\nsponsor_keys: [memory-team]\nsponsor_key: other-team\n"))
		if err == nil {
			t.Fatal("DecodeConfig accepted a sponsor key outside the declared set")
		}
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.Field != "sponsor_key" {
			t.Fatalf("DecodeConfig error = %v, want a sponsor_key rejection", err)
		}
	})

	t.Run("a sponsor key that is not a bounded token is rejected", func(t *testing.T) {
		for _, key := range []string{"memory team", "memory/team", "memory:team", "memory team with spaces"} {
			_, err := DecodeConfig(mapToNode(t, configRequired+
				"funding_mode: operator_sponsored\nsponsor_keys: [\"memory-team\"]\nsponsor_key: \""+key+"\"\n"))
			if err == nil {
				t.Fatalf("DecodeConfig accepted the sponsor key %q", key)
			}
		}
	})
}

// TestConfigRejectsUnknownFundingKeys keeps the funding vocabulary out of
// every other spelling: the operator's decision has exactly one key set.
func TestConfigRejectsUnknownFundingKeys(t *testing.T) {
	for _, raw := range []string{
		configRequired + "funding: customer_funded\n",
		configRequired + "sponsorship_mode: customer_funded\n",
		configRequired + "funding_mode: customer_funded\nfunding_mode: operator_sponsored\n",
	} {
		if _, err := DecodeConfig(mapToNode(t, raw)); err == nil {
			t.Fatalf("DecodeConfig accepted %q", raw)
		}
	}
}

// TestConfigFundingModeNeverNamesAnAccount proves that the funding
// vocabulary has no place for an account: the account comes from the host's
// identity seam, never from the feature configuration.
func TestConfigFundingModeNeverNamesAnAccount(t *testing.T) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(configRequired+"funding_mode: customer_funded\naccount_id: acct-chosen\n"), &node); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeConfig(node); err == nil {
		t.Fatal("DecodeConfig accepted an account identifier in the feature configuration")
	}
}
