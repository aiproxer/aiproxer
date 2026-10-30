package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeBillingBoundaryHasNoStreamMonetarySettlement(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal/core/runtime/executor_settlement.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, forbidden := range []string{
		"EconomicsRater",
		"rateMonetaryExposure",
		"accountingledger",
		"recordTokenAccountingLedger",
		"recordPartialTokenAccountingLedger",
		"recordCancellationBillingMarker",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("executor settlement contains deleted stream monetary path %q", forbidden)
		}
	}
}

func TestPhase8KeepsTerminalFinalizeBillingCostMerge(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	// Req 3.4: stream CostPresent (including authoritative zero) is selected
	// onto the current call-leg record through the explicit one-way V1
	// compatibility projection. Behavior is locked by runtime tests
	// TestBillingLegPreservesStreamAuthoritativeZeroCostAcrossFinalize and
	// TestParallelBillingLegPreservesStreamAuthoritativeZeroCostAcrossFinalize.
	// This gate forbids splicing money onto aipapi.Event in the observe path.
	// Task 18.1 retired the destructive mergeStreamCostOntoLeg helper; only
	// projectV1BillingEvidence may perform the V1 selection.
	for _, rel := range []string{
		"internal/core/runtime/billing_leg.go",
	} {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		if strings.Contains(text, "finalize.CostPresent") && strings.Contains(text, "stream.CostNanoUnits") {
			t.Fatalf("%s still splices CostPresent onto aipapi.Event; merge belongs on current call-leg money evidence", rel)
		}
		if strings.Contains(text, "mergeStreamCostOntoLeg") {
			t.Fatalf("%s still defines mergeStreamCostOntoLeg; V1 cost selection belongs to projectV1BillingEvidence only", rel)
		}
		if !strings.Contains(text, "projectV1BillingEvidence") {
			t.Fatalf("%s must select stream CostPresent onto current call-leg evidence through projectV1BillingEvidence when FinalizeBilling has no money", rel)
		}
	}
}

func TestPhase8ConfigRejectsMonetaryAuthorityKinds(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal/core/config/accounting_authority.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "func retiredMonetaryAuthorityRule") {
		t.Fatal("production accounting.authority YAML must keep a monetary-rule rejection gate")
	}
	if !strings.Contains(text, "budget") || !strings.Contains(text, "spend_cap") || !strings.Contains(text, "money_nano") {
		t.Fatal("monetary authority rejection must name budget, spend_cap, and money_nano")
	}
}
