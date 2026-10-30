package config_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestValidateAccountingConcurrencyDisabledByDefault(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Accounting.Concurrency.Enabled {
		t.Fatal("concurrency must be disabled by default")
	}
}

func TestValidateAccountingConcurrencyDefaults(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Accounting: config.AccountingConfig{
			Concurrency: config.ConcurrencyAuthorityConfig{
				Enabled: true,
				Rules: []config.ConcurrencyAuthorityRuleConfig{{
					ID:                "max-active",
					Mode:              "strict",
					MaxActiveRequests: 5,
					Match: config.AccountingAuthorityDimensionsConfig{
						Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("p1")},
					},
				}},
			},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Accounting.Concurrency.Store != "memory" {
		t.Fatalf("store=%q want memory", cfg.Accounting.Concurrency.Store)
	}
	if cfg.Accounting.Concurrency.StoreID != "default" {
		t.Fatalf("store_id=%q want default", cfg.Accounting.Concurrency.StoreID)
	}
	if got, err := cfg.Accounting.Concurrency.LeaseTTLDuration(); err != nil || got != config.DefaultConcurrencyLeaseTTL {
		t.Fatalf("lease_ttl=%v err=%v", got, err)
	}
	if got, err := cfg.Accounting.Concurrency.RenewBeforeDuration(); err != nil || got != config.DefaultConcurrencyRenewBefore {
		t.Fatalf("renew_before=%v err=%v", got, err)
	}
}

func TestValidateAccountingConcurrencyRejectsInvalidStore(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Accounting: config.AccountingConfig{
			Concurrency: config.ConcurrencyAuthorityConfig{
				Enabled: true,
				Store:   "redis",
			},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected invalid store error")
	}
}

func TestConcurrencyDomainRulesApplyDefaults(t *testing.T) {
	t.Parallel()
	cfg := config.ConcurrencyAuthorityConfig{
		Enabled: true,
		Rules: []config.ConcurrencyAuthorityRuleConfig{{
			ID:                "r1",
			MaxActiveRequests: 3,
			Match: config.AccountingAuthorityDimensionsConfig{
				Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("alice")},
			},
		}},
	}
	rules, err := cfg.DomainRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules=%d", len(rules))
	}
	if rules[0].Mode != "strict" || rules[0].Limit != 3 {
		t.Fatalf("rule=%+v", rules[0])
	}
	if rules[0].LeaseTTL != config.DefaultConcurrencyLeaseTTL {
		t.Fatalf("ttl=%v", rules[0].LeaseTTL)
	}
	if rules[0].RenewBefore != config.DefaultConcurrencyRenewBefore {
		t.Fatalf("renew_before=%v", rules[0].RenewBefore)
	}
}
