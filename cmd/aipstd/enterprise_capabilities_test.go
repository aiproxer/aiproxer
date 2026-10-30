package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
)

// TestCheckConfig_RejectsPricingUnderStockCapabilities proves the stock
// command no longer composes the built-in billing path: an Enterprise pricing
// block fails validation naming the Enterprise capability instead of being
// silently ignored or served by a stock composition.
func TestCheckConfig_RejectsPricingUnderStockCapabilities(t *testing.T) {
	t.Parallel()
	body := readDogfoodConfig(t)
	path := filepath.Join(t.TempDir(), "enterprise-pricing.yaml")
	const block = `
accounting:
  pricing:
    currency: USD
`
	if err := os.WriteFile(path, []byte(body+block), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: path,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code == 0 {
		t.Fatalf("check-config exit 0 for pricing under stock capabilities; stdout=%s", out.String())
	}
	if want := `accounting.pricing requires AIProxer Enterprise (capability "billing")`; !strings.Contains(errb.String(), want) {
		t.Fatalf("stderr = %q, want %q", errb.String(), want)
	}
}

// TestCheckConfig_RejectsUsageLimitRulesUnderStockCapabilities proves the stock
// command no longer composes the built-in usage-limit rule path: a rules block
// fails validation naming the Enterprise capability instead of being silently
// ignored or served by a built-in authority (task 5.3).
func TestCheckConfig_RejectsUsageLimitRulesUnderStockCapabilities(t *testing.T) {
	t.Parallel()
	body := readDogfoodConfig(t)
	path := filepath.Join(t.TempDir(), "enterprise-usage-limits.yaml")
	const block = `
accounting:
  authority:
    enabled: true
    mode: strict
    store: memory
    startup_posture: fail_closed
    rules:
      - id: customer.requests
        kind: quota
        unit: requests
        limit: 10
        perspective: customer
        lifecycle_scope: logical_request
        basis: frontend_ingress
        namespace: usage-authority/v2
        version: "1"
`
	if err := os.WriteFile(path, []byte(body+block), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: path,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code == 0 {
		t.Fatalf("check-config exit 0 for usage-limit rules under stock capabilities; stdout=%s", out.String())
	}
	if want := `accounting.authority.rules requires AIProxer Enterprise (capability "usage-limits")`; !strings.Contains(errb.String(), want) {
		t.Fatalf("stderr = %q, want %q", errb.String(), want)
	}
}

func readDogfoodConfig(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(bpkit.WriteDogfoodLocalStubConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
