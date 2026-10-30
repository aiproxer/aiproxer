package aipruntime_test

import (
	"context"
	"strings"
	"testing"

	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/prometheus/client_golang/prometheus"
)

const pricingOnlyBlock = "accounting:\n  pricing:\n    currency: USD\n"

const usageLimitRuleBlock = `accounting:
  authority:
    enabled: true
    mode: strict
    store: memory
    startup_posture: fail_closed
    rules:
      - id: customer.requests
        kind: quota
        mode: strict
        unit: requests
        limit: 10
        perspective: customer
        lifecycle_scope: logical_request
        basis: frontend_ingress
        namespace: usage-authority/v2
        version: "1"
`

// enterpriseConfigPath appends one block to a materialized dogfood config.
func enterpriseConfigPath(t *testing.T, block string) string {
	t.Helper()
	body := mustReadFile(t, bpkit.WriteDogfoodLocalStubConfig(t))
	return writeAtomicConfig(t, t.TempDir(), "cfg.yaml", body+"\n"+block)
}

// seamUsageLimits is a minimal public-only port implementation for the seam
// tests: it admits every request and ignores completion.
type seamUsageLimits struct{}

var _ aipruntime.UsageLimits = seamUsageLimits{}

func (seamUsageLimits) Admit(context.Context, aipruntime.UsageAdmissionInput) (aipruntime.UsageAdmissionResult, error) {
	return aipruntime.UsageAdmissionResult{Allowed: true, Outcome: "allow"}, nil
}

func (seamUsageLimits) Settle(context.Context, aipruntime.UsageSettleInput) (aipruntime.UsageSettleResult, error) {
	return aipruntime.UsageSettleResult{}, nil
}

func (seamUsageLimits) Release(context.Context, aipruntime.UsageReleaseInput) (aipruntime.UsageReleaseResult, error) {
	return aipruntime.UsageReleaseResult{}, nil
}

func (seamUsageLimits) ApplyUsage(context.Context, aipruntime.UsageApplyUsageCommand) (aipruntime.UsageApplyUsageResult, error) {
	return aipruntime.UsageApplyUsageResult{}, nil
}

// TestBuild_EnterpriseCapabilityThreading is the public entry-point sentinel:
// declared capabilities must reach config validation; absent ones fail closed
// naming the key.
func TestBuild_EnterpriseCapabilityThreading(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pricingPath := enterpriseConfigPath(t, pricingOnlyBlock)

	if _, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: pricingPath}); err == nil {
		t.Fatal("Build without capabilities must fail closed for pricing configuration")
	} else if !strings.Contains(err.Error(), `capability "billing"`) {
		t.Fatalf("error = %v, want the named billing capability", err)
	}
	priced, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath:             pricingPath,
		EnterpriseCapabilities: []aipruntime.EnterpriseCapability{aipruntime.CapabilityBilling},
	})
	if err != nil {
		t.Fatalf("Build with the billing capability: %v", err)
	}
	t.Cleanup(func() { _ = priced.Close(ctx) })

	rulesPath := enterpriseConfigPath(t, usageLimitRuleBlock)
	limited, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath:             rulesPath,
		EnterpriseCapabilities: []aipruntime.EnterpriseCapability{aipruntime.CapabilityUsageLimits},
		UsageLimits:            seamUsageLimits{},
	})
	if err != nil {
		t.Fatalf("Build with the usage-limits capability and an injected port: %v", err)
	}
	t.Cleanup(func() { _ = limited.Close(ctx) })
}

// TestBuild_UsageLimitsCapabilityRequiresPort is the D0 negative control: the
// declared usage-limits capability is refused without a usable port, and a
// typed-nil port is named as typed-nil before any I/O.
func TestBuild_UsageLimitsCapabilityRequiresPort(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rulesPath := enterpriseConfigPath(t, usageLimitRuleBlock)

	_, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath:             rulesPath,
		EnterpriseCapabilities: []aipruntime.EnterpriseCapability{aipruntime.CapabilityUsageLimits},
	})
	if err == nil || !strings.Contains(err.Error(), `capability "usage-limits"`) {
		t.Fatalf("declared capability without a port must be refused, got %v", err)
	}

	var typedNil *seamUsageLimitsPointer
	_, err = aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath:             rulesPath,
		EnterpriseCapabilities: []aipruntime.EnterpriseCapability{aipruntime.CapabilityUsageLimits},
		UsageLimits:            typedNil,
	})
	if err == nil || !strings.Contains(err.Error(), "Options.UsageLimits is typed-nil") {
		t.Fatalf("typed-nil port must be refused by name, got %v", err)
	}
}

// seamUsageLimitsPointer exercises the typed-nil rejection: the pointer type
// implements the port, and a nil pointer must never be captured.
type seamUsageLimitsPointer struct{ seamUsageLimits }

func (*seamUsageLimitsPointer) Admit(context.Context, aipruntime.UsageAdmissionInput) (aipruntime.UsageAdmissionResult, error) {
	return aipruntime.UsageAdmissionResult{Allowed: true}, nil
}

// TestBuildWithBilling_ImpliesBillingOnly proves the monetary entry point
// declares billing implicitly and never the usage-limits capability.
func TestBuildWithBilling_ImpliesBillingOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	counters := &lifecycleCounters{}
	rt, err := aipruntime.BuildWithBilling(ctx, aipruntime.Options{ConfigPath: enterpriseConfigPath(t, pricingOnlyBlock)}, validFacadeBinding(counters))
	if err != nil {
		t.Fatalf("BuildWithBilling must imply the billing capability: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	rulesPath := enterpriseConfigPath(t, usageLimitRuleBlock)
	if _, err := aipruntime.BuildWithBilling(ctx, aipruntime.Options{ConfigPath: rulesPath}, validFacadeBinding(&lifecycleCounters{})); err == nil {
		t.Fatal("BuildWithBilling must not imply the usage-limits capability")
	} else if !strings.Contains(err.Error(), `capability "usage-limits"`) {
		t.Fatalf("error = %v, want the named usage-limits capability", err)
	}
}

// TestBuild_RegisterStartupMetricsErrorAborts proves the optional registration
// function is threaded from the public options into process construction.
func TestBuild_RegisterStartupMetricsErrorAborts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	metricsPath := enterpriseConfigPath(t, "observability:\n  metrics:\n    enabled: true\n")
	_, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath: metricsPath,
		RegisterStartupMetrics: func(prometheus.Registerer) error {
			return context.DeadlineExceeded
		},
	})
	if err == nil || !strings.Contains(err.Error(), "startup metrics registration") {
		t.Fatalf("registration error must abort the build: %v", err)
	}
}
