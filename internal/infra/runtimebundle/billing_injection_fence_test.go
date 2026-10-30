package runtimebundle_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestBuildHost_LeftoverLedgerPricingDoesNotOpenBillingJournal proves leftover
// accounting.ledger.* and accounting.pricing YAML with authoritative unset is
// not a billing factory (req 1.3, 3.5). The billing capability is declared
// because the fixture requests pricing configuration; declaring a capability
// admits configuration, it never injects a binding.
func TestBuildHost_LeftoverLedgerPricingDoesNotOpenBillingJournal(t *testing.T) {
	t.Parallel()
	ledgerPath := filepath.Join(t.TempDir(), "leftover-token-ledger.db")
	cfgPath := writeLeftoverLedgerPricingConfig(t, ledgerPath)

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:             cfgPath,
		Mandatory:              aipsdk.StandardDistributionRequirements(),
		LogWriter:              io.Discard,
		HandlerComposer:        stdhttp.ComposeStandardHTTP,
		EnterpriseCapabilities: billingCapabilities(),
	})
	if err != nil {
		t.Fatalf("BuildHost with leftover ledger+pricing YAML: %v", err)
	}
	hostServeCleanup(t, host)

	if host.Config() == nil {
		t.Fatal("expected loaded config")
	}
	acc := host.Config().Accounting
	if acc.Ledger.Store == "" || len(acc.Pricing.Models) == 0 {
		t.Fatal("test fixture must include leftover ledger and pricing YAML")
	}
	ex := hostActiveExecutor(t, host)
	if ex.BillingCreditGate != nil || ex.BillingExposureAdmission != nil || ex.TerminalUsageSink != nil || ex.BillingIdentity.AccountID != nil {
		t.Fatal("leftover ledger+pricing YAML must not wire partial billing ports")
	}
	if _, err := os.Stat(ledgerPath); !os.IsNotExist(err) {
		t.Fatalf("leftover accounting.ledger YAML opened sqlite at %s: %v", ledgerPath, err)
	}
}

func writeLeftoverLedgerPricingConfig(t *testing.T, ledgerPath string) string {
	t.Helper()
	basePath := runtimebundle.MaterializeExampleConfigForTest(
		t,
		filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
	)
	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	block := fmt.Sprintf(`
accounting:
  enabled: true
  mode: provider_first
  tokenizer:
    default_encoding: cl100k_base
  ledger:
    store: sqlite
    sqlite_path: %q
    write_policy: required
  pricing:
    currency: USD
    catalog_version: leftover-yaml-not-tur-catalog
    models:
      - backend: dogfood-local
        model: stub-default
        input_per_1m: "1.00"
        output_per_1m: "2.00"
`, filepath.ToSlash(ledgerPath))
	path := filepath.Join(t.TempDir(), "leftover-ledger-pricing.yaml")
	if err := os.WriteFile(path, append(append([]byte{}, raw...), []byte(block)...), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
