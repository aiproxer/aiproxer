package runtimebundle_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/prometheus/client_golang/prometheus"
)

const pricingBlock = "accounting:\n  pricing:\n    currency: USD\n"

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

const metricsEnabledBlock = "observability:\n  metrics:\n    enabled: true\n"

func billingCapabilities() config.CapabilitySet {
	return config.CapabilitySet{config.CapabilityBilling: true}
}

func usageLimitsCapabilities() config.CapabilitySet {
	return config.CapabilitySet{config.CapabilityUsageLimits: true}
}

func dogfoodBytes(tb testing.TB) []byte {
	tb.Helper()
	raw, err := os.ReadFile(runtimebundle.MaterializeExampleConfigForTest(
		tb, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
	))
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

// writeDogfood atomically replaces path with the dogfood fixture plus block;
// edit may pre-process the base bytes (for example to force a publication).
func writeDogfood(tb testing.TB, path, block string, edit func([]byte) []byte) {
	tb.Helper()
	body := append([]byte{}, dogfoodBytes(tb)...)
	if edit != nil {
		body = edit(body)
	}
	candidate := filepath.Join(tb.TempDir(), "candidate.yaml")
	if err := os.WriteFile(candidate, append(body, []byte("\n"+block)...), 0o600); err != nil {
		tb.Fatal(err)
	}
	if err := replaceTestFile(candidate, path); err != nil {
		tb.Fatal(err)
	}
}

func newDogfoodConfig(tb testing.TB, block string) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "host.yaml")
	writeDogfood(tb, path, block, nil)
	return path
}

func reloadHost(t *testing.T, host *runtimebundle.Host, actor string) sdkreload.Result {
	t.Helper()
	return host.Reload(t.Context(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI, AcceptedAt: time.Now().UTC(), SafeActor: actor})
}

// TestEnterpriseCapabilityThreading proves the declared capability set reaches
// each distinct startup entry point, so pricing configuration is accepted.
func TestEnterpriseCapabilityThreading(t *testing.T) {
	t.Parallel()
	path := newDogfoodConfig(t, pricingBlock)
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:             path,
		Mandatory:              aipsdk.StandardDistributionRequirements(),
		LogWriter:              io.Discard,
		HandlerComposer:        stdhttp.ComposeStandardHTTP,
		EnterpriseCapabilities: billingCapabilities(),
	})
	if err != nil {
		t.Fatalf("BuildHost with the billing capability: %v", err)
	}
	hostServeCleanup(t, host)

	if _, err := runtimebundle.ValidateStructural(t.Context(), runtimebundle.ValidateStructuralInput{
		ConfigPath:             path,
		Mandatory:              aipsdk.StandardDistributionRequirements(),
		EnterpriseCapabilities: billingCapabilities(),
	}); err != nil {
		t.Fatalf("ValidateStructural with the billing capability: %v", err)
	}
	if _, err := runtimebundle.InspectRoutes(t.Context(), runtimebundle.InspectInput{
		ConfigPath:             path,
		Mandatory:              aipsdk.StandardDistributionRequirements(),
		EnterpriseCapabilities: billingCapabilities(),
	}); err != nil {
		t.Fatalf("InspectRoutes with the billing capability: %v", err)
	}
	if err := runtimebundle.ValidateDistribution(t.Context(), runtimebundle.ValidateDistributionInput{
		ConfigPath:             path,
		Mandatory:              aipsdk.StandardDistributionRequirements(),
		HandlerComposer:        stdhttp.ComposeStandardHTTP,
		EnterpriseCapabilities: billingCapabilities(),
	}); err != nil {
		t.Fatalf("ValidateDistribution with the billing capability: %v", err)
	}
}

// TestEnterpriseCapabilitySnapshot_ImmutableAcrossReload proves BuildHost owns
// one immutable capability snapshot: caller map mutations in both directions
// cannot change what a real published reload admits, and concurrent reads never
// race the caller's map.
func TestEnterpriseCapabilitySnapshot_ImmutableAcrossReload(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "host.yaml")
	writeDogfood(t, path, pricingBlock, nil)

	callerCaps := map[string]bool{"billing": true}
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:             path,
		Mandatory:              aipsdk.StandardDistributionRequirements(),
		LogWriter:              io.Discard,
		HandlerComposer:        stdhttp.ComposeStandardHTTP,
		EnterpriseCapabilities: callerCaps,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	// Mutate both directions on the caller's map after BuildHost returned.
	delete(callerCaps, "billing")
	callerCaps["usage-limits"] = true

	bumpMaxAttempts := func(body []byte) []byte {
		return bytes.Replace(body, []byte("max_attempts: 3"), []byte("max_attempts: 4"), 1)
	}
	writeDogfood(t, path, pricingBlock, bumpMaxAttempts)
	if res := reloadHost(t, host, "capability-snapshot-test"); res.Category != sdkreload.ResultPublished {
		t.Fatalf("pricing publication on the snapshot = %+v, want ResultPublished", res)
	}

	writeDogfood(t, path, usageLimitRuleBlock, nil)
	if res := reloadHost(t, host, "capability-snapshot-test"); res.Category != sdkreload.ResultInvalid {
		t.Fatalf("rule candidate after caller map mutation = %+v, want ResultInvalid (usage-limits was never declared)", res)
	}

	writeDogfood(t, path, pricingBlock, nil)
	if res := reloadHost(t, host, "capability-snapshot-test"); res.Category != sdkreload.ResultPublished {
		t.Fatalf("second pricing publication = %+v, want ResultPublished", res)
	}
}

func buildMetricsHost(t *testing.T, cfgPath string, register func(prometheus.Registerer) error) (*runtimebundle.Host, error) {
	t.Helper()
	return runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		Production:      runtimebundle.ProductionOptions{RegisterStartupMetrics: register},
	})
}

func counter(name string) prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: "startup registration test"})
}

// TestStartupMetricsRegistration_NilAndDisabledSkip: nil registration with
// metrics enabled builds; a non-nil function with metrics disabled never runs.
func TestStartupMetricsRegistration_NilAndDisabledSkip(t *testing.T) {
	t.Parallel()
	host, err := buildMetricsHost(t, newDogfoodConfig(t, metricsEnabledBlock), nil)
	if err != nil {
		t.Fatalf("nil registration with metrics enabled: %v", err)
	}
	hostServeCleanup(t, host)

	var calls atomic.Int64
	host, err = buildMetricsHost(t, newDogfoodConfig(t, "# metrics stay disabled (default)"), func(prometheus.Registerer) error {
		calls.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("registration with metrics disabled: %v", err)
	}
	hostServeCleanup(t, host)
	if got := calls.Load(); got != 0 {
		t.Fatalf("registration calls with metrics disabled = %d, want 0", got)
	}
}

// TestStartupMetricsRegistration_RealPublicationAndReload proves the callback
// receives the actual process registry exactly once, across a published reload.
func TestStartupMetricsRegistration_RealPublicationAndReload(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "host.yaml")
	writeDogfood(t, path, metricsEnabledBlock, nil)
	var got prometheus.Registerer
	var calls atomic.Int64
	metric := counter("aip_test_startup_metrics_registration_total")

	host, err := buildMetricsHost(t, path, func(reg prometheus.Registerer) error {
		calls.Add(1)
		got = reg
		return reg.Register(metric)
	})
	if err != nil {
		t.Fatalf("BuildHost with real registration: %v", err)
	}
	hostServeCleanup(t, host)
	if calls.Load() != 1 {
		t.Fatalf("registration calls = %d, want exactly 1", calls.Load())
	}
	gatherer, ok := got.(prometheus.Gatherer)
	if !ok {
		t.Fatalf("registration received %T, want the actual process registry", got)
	}
	assertMetricFamilyCount(t, gatherer, "aip_test_startup_metrics_registration_total", 1)

	writeDogfood(t, path, metricsEnabledBlock, func(body []byte) []byte {
		return bytes.Replace(body, []byte("max_attempts: 3"), []byte("max_attempts: 4"), 1)
	})
	if res := reloadHost(t, host, "metrics-registration-test"); res.Category != sdkreload.ResultPublished {
		t.Fatalf("published reload = %+v, want ResultPublished", res)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("registration calls after published reload = %d, want exactly 1", got)
	}
	assertMetricFamilyCount(t, gatherer, "aip_test_startup_metrics_registration_total", 1)
}

func assertMetricFamilyCount(t *testing.T, gatherer prometheus.Gatherer, name string, want int) {
	t.Helper()
	families, err := gatherer.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	got := 0
	for _, family := range families {
		if family.GetName() == name {
			got++
		}
	}
	if got != want {
		t.Fatalf("metric family %q count = %d, want %d", name, got, want)
	}
}

// TestStartupMetricsRegistration_DuplicateFailsStartupCleanly: a real
// prometheus registration error aborts the build and leaves no global residue.
func TestStartupMetricsRegistration_DuplicateFailsStartupCleanly(t *testing.T) {
	t.Parallel()
	cfgPath := newDogfoodConfig(t, metricsEnabledBlock)
	duplicate := counter("aip_test_startup_metrics_duplicate_total")

	host, err := buildMetricsHost(t, cfgPath, func(reg prometheus.Registerer) error {
		if err := reg.Register(duplicate); err != nil {
			return err
		}
		return reg.Register(duplicate)
	})
	if err == nil {
		_ = host.Close(context.Background())
		t.Fatal("duplicate collector registration must fail startup")
	}
	if host != nil {
		t.Fatal("failed startup must not return a host")
	}
	if !strings.Contains(err.Error(), "startup metrics registration") {
		t.Fatalf("error = %v, want the startup registration wrapper", err)
	}

	host, err = buildMetricsHost(t, cfgPath, func(reg prometheus.Registerer) error {
		return reg.Register(counter("aip_test_startup_metrics_fresh_total"))
	})
	if err != nil {
		t.Fatalf("fresh build after failed registration must succeed: %v", err)
	}
	hostServeCleanup(t, host)
}
