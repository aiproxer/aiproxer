package configreload_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

func TestReloadabilityClassify_QuotaPolicyIsGenerationReloadable(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	active.Accounting.Authority.Quota = testQuotaConfig("quota-v1")
	candidate.Accounting.Authority.Quota = testQuotaConfig("quota-v2")

	changes, err := configreload.Classify(active, candidate)
	if restartRequired, ok := errors.AsType[*configreload.RestartRequiredError](err); ok {
		t.Fatalf("quota policy replacement must be reloadable, got restart_required=%v", restartRequired)
	}
	if err != nil {
		t.Fatalf("classify quota policy replacement: %v", err)
	}
	if !containsChange(changes, "accounting.authority.quota", configreload.ChangeReloadable) {
		t.Fatalf("quota policy replacement must be a reloadable change, got %#v", changes)
	}
}

func testQuotaConfig(version string) *config.AccountingQuotaConfig {
	return &config.AccountingQuotaConfig{
		ID:                 "provider-requests",
		Version:            version,
		Method:             authority.QuotaPolicyMethodV1,
		StoreID:            "metering-store",
		TenantID:           "tenant-1",
		ProviderAccountKey: "provider-account",
		PoolID:             "pool-1",
		WindowID:           "monthly",
		ResetAt:            "2026-01-01T00:00:00Z",
		Freshness:          "1h",
	}
}
