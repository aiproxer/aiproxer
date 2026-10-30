package runtimebundle_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestBuildConcurrencyAuthorityDisabledIsNoop(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	_, built := mustProcessAndCandidate(t, cfg, baseAuthorityOptions(t))
	if built.Executor().ConcurrencyProvider != nil {
		t.Fatal("concurrency provider must be nil when disabled")
	}
	if built.Executor().RequestCoordinator != nil && built.Executor().RequestCoordinator.Concurrency != nil {
		t.Fatal("request coordinator must not wire concurrency when disabled")
	}
}

func TestBuildConcurrencyAuthorityWiresProvider(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	cfg.Accounting.Concurrency = config.ConcurrencyAuthorityConfig{
		Enabled: true,
		Store:   "memory",
		Rules: []config.ConcurrencyAuthorityRuleConfig{{
			ID:                "max-active",
			Mode:              "strict",
			MaxActiveRequests: 2,
			Match: config.AccountingAuthorityDimensionsConfig{
				Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("alice")},
			},
		}},
	}
	if err := config.ValidateWithCapabilities(cfg, usageLimitsCapabilities()); err != nil {
		t.Fatal(err)
	}
	_, built := mustProcessAndCandidate(t, cfg, baseAuthorityOptions(t))
	if built.Executor().ConcurrencyProvider == nil {
		t.Fatal("expected concurrency provider")
	}
	if built.Executor().RequestCoordinator == nil || built.Executor().RequestCoordinator.Concurrency == nil {
		t.Fatal("expected concurrency wired into request coordinator")
	}
	dec, err := built.Executor().ConcurrencyProvider.AdmitLease(context.Background(), authority.LeaseAdmission{
		RequestID: "r1",
		Scope:     scope.PrincipalScopeView{PrincipalID: scope.Known("alice")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != authority.LeaseAllow || dec.LeaseID == "" {
		t.Fatalf("dec=%+v", dec)
	}
	if err := built.Executor().ConcurrencyProvider.ReleaseLease(context.Background(), authority.LeaseRelease{
		LeaseID:   dec.LeaseID,
		RequestID: "r1",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildConcurrencyAuthoritySQLite(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	cfg.Accounting.Concurrency = config.ConcurrencyAuthorityConfig{
		Enabled:    true,
		Store:      "sqlite",
		SQLitePath: filepath.Join(t.TempDir(), "leases.db"),
		Rules: []config.ConcurrencyAuthorityRuleConfig{{
			ID:                "max-active",
			MaxActiveRequests: 1,
			Match: config.AccountingAuthorityDimensionsConfig{
				Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("bob")},
			},
		}},
	}
	if err := config.ValidateWithCapabilities(cfg, usageLimitsCapabilities()); err != nil {
		t.Fatal(err)
	}
	_, built := mustProcessAndCandidate(t, cfg, baseAuthorityOptions(t))
	if built.Executor().ConcurrencyProvider == nil {
		t.Fatal("expected sqlite concurrency provider")
	}
}
