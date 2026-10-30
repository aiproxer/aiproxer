package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 14.3 certification: strict offers are rejected through the one monetary
// admission authority, supplier work never takes the customer balance lock,
// and stream handlers stay off all quote/settle/balance math.

// TestPhase14StreamFilesStayOffQuoteSettleAndBalance extends the standing
// stream-handler guard to the Phase 14 quote/settle symbols: no stream-time
// financial rating, admission math, or balance mutation path may exist.
func TestPhase14StreamFilesStayOffQuoteSettleAndBalance(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	streamFiles := []string{
		"executor_recv_loop.go",
		"executor_settlement.go",
		"executor_retry_stream.go",
		"response_pipeline_observations.go",
		"stream_terminal.go",
		"turn_terminal.go",
		"parallel_race.go",
	}
	forbiddenIdents := []string{
		"EstimateMaxCustomerCharge",
		"EstimateRichCustomerCharge",
		"EvaluateSettle",
		"EvaluateAdmit",
		"AdmitExposure",
		"ApplyCallBillingResult",
		"ApplyBalanceDelta",
		"AdmitExposureInput",
		"SettleExposureInput",
	}
	dir := filepath.Join(root, "internal", "core", "runtime")
	fset := token.NewFileSet()
	for _, name := range streamFiles {
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, forbid := range forbiddenIdents {
				if id.Name == forbid {
					t.Fatalf("%s must not reference %s (no stream-time quote/settle/balance path)", name, forbid)
				}
			}
			return true
		})
	}
}

// TestPhase14SingleMonetaryAdmissionAuthority locks Req 14.1/14.4: exactly one
// production implementation of the monetary exposure admission interface and
// exactly one durable exposure store may exist. Test fakes are excluded;
// non-money authority registrations live on different interfaces.
// Task 15.2 named exception: internal/infra/billingbinding/adapter.go is the
// single permitted delegating adapter. It implements no admission math of its
// own; it quotes and admits exclusively through the validated public binding
// ports (see TestBillingBindingAdapterHoldsNoAdmissionMath).
// Task 17.4's recovery verifier and its quiescence decorator now live in the
// Enterprise composition (enterprise/internal/compose/accounting_recovery.go),
// outside this open-source scan root. The two remaining authority files are the
// adapter and the public binding adapter; the Enterprise verifier is covered
// by TestRecovery174QuiescedAdmissionDeniesStrictWithZeroEffects.
func TestPhase14SingleMonetaryAdmissionAuthority(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var authorityImpls []string
	var storeImpls []string
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		body := string(src)
		// Receiver definitions carry ") Admit(" / ") AdmitExposure("; the
		// port interface declarations do not, so they never match here.
		for line := range strings.SplitSeq(body, "\n") {
			if strings.Contains(line, ") Admit(") && strings.Contains(line, "BillingExposureAdmissionInput") {
				rel, _ := filepath.Rel(root, path)
				authorityImpls = append(authorityImpls, filepath.ToSlash(rel))
				break
			}
		}
		for line := range strings.SplitSeq(body, "\n") {
			if strings.Contains(line, ") AdmitExposure(") {
				rel, _ := filepath.Rel(root, path)
				storeImpls = append(storeImpls, filepath.ToSlash(rel))
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The billing admission adapter moved to the Enterprise tree with task
	// 6.5; enterprise/internal/archtest keeps it the single Enterprise
	// admission authority.
	wantAuthority := []string{
		"internal/infra/billingbinding/adapter.go",
	}
	wantAll := wantAuthority
	if len(authorityImpls) != len(wantAll) {
		t.Fatalf("monetary admission authority impls = %v, want %v", authorityImpls, wantAll)
	}
	seen := make(map[string]struct{}, len(authorityImpls))
	for _, impl := range authorityImpls {
		seen[impl] = struct{}{}
	}
	for _, want := range wantAll {
		if _, ok := seen[want]; !ok {
			t.Fatalf("monetary admission authority impls = %v, want %v", authorityImpls, wantAll)
		}
	}
	// The durable exposure store moved to the Enterprise tree with
	// open-core-billing-split task 6.3; enterprise/internal/archtest asserts it
	// stays the single store implementation.
	if len(storeImpls) != 0 {
		t.Fatalf("durable exposure store impls in the open-source tree = %v, want none", storeImpls)
	}
}

// TestPhase14RuntimeStaysOffQuoteAndSettleMath locks the authority boundary:
// quote mathematics lives in the admission adapter and settlement math in the
// billing domain/store. Runtime only carries the admission interface and
// terminal handoff, never the math.
func TestPhase14RuntimeStaysOffQuoteAndSettleMath(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "runtime")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		for _, term := range []string{
			"EstimateMaxCustomerCharge",
			"EstimateRichCustomerCharge",
			"EvaluateSettle",
			"EvaluateAdmit",
		} {
			if strings.Contains(body, term) {
				t.Fatalf("runtime %s must not contain quote/settle math (%q)", name, term)
			}
		}
	}
}
