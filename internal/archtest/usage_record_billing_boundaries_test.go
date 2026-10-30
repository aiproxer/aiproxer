package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestRuntimeStreamHandlersStayOffJournalRatingSettlement locks 7.1 / 17.8
// (already active) and design Fail-if "runtime stream handlers call rating/journal/settlement".
func TestRuntimeStreamHandlersStayOffJournalRatingSettlement(t *testing.T) {
	t.Parallel()

	assertDirectImportsExclude(t, "./internal/core/runtime", "/enterprise/internal/infra/billingstore",
		"runtime must not import billingstore journal settlement")
	assertDirectImportsExclude(t, "./internal/core/runtime", "/internal/infra/billingadmission",
		"runtime must not import billing admission store adapters")
	assertDirectImportsExclude(t, "./internal/core/runtime", "/internal/core/tokenaccounting/ledger",
		"runtime must not import legacy token ledger")

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
		"rateMonetaryExposure",
		"rateMonetaryExposureWith",
		"EconomicsRater",
		"ApplyBillingResult",
		"PostJournalTransaction",
		"CustomerSettlementSourceKey",
		"ProviderCostSourceKey",
		"enrichUsageCost",
		"recordTokenAccountingLedger",
		"recordPartialTokenAccountingLedger",
		"recordCancellationBillingMarker",
		"openDurableAccountingLedger",
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
					t.Fatalf("%s must not reference %s (stream handlers stay off rating/journal/legacy money)", name, forbid)
				}
			}
			return true
		})
	}

	// Authority settle must not rebuild Rated money from stream CostPresent.
	lifecycleDir := dir
	lifecyclePaths, err := filepath.Glob(filepath.Join(lifecycleDir, "authority_lifecycle*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var lifecycleSrc []byte
	scanned := map[string]struct{}{}
	for _, lifecyclePath := range lifecyclePaths {
		if strings.HasSuffix(lifecyclePath, "_test.go") {
			continue
		}
		scanned[filepath.Base(lifecyclePath)] = struct{}{}
		src, err := os.ReadFile(lifecyclePath)
		if err != nil {
			t.Fatal(err)
		}
		lifecycleSrc = append(lifecycleSrc, src...)
		lifecycleSrc = append(lifecycleSrc, '\n')
	}
	if len(lifecycleSrc) == 0 {
		t.Fatal("authority_lifecycle sources not found")
	}
	for _, name := range []string{
		"authority_lifecycle.go",
		"authority_lifecycle_settle.go",
		"authority_lifecycle_release.go",
	} {
		if _, ok := scanned[name]; !ok {
			t.Fatalf("authority_lifecycle glob missed %s", name)
		}
	}
	body := string(lifecycleSrc)
	// Match the assignment shape regardless of gofmt's field alignment: an
	// exact-spacing needle goes blind after a harmless reflow, leaving the
	// money path unguarded.
	for _, term := range []*regexp.Regexp{
		regexp.MustCompile(`Rated:\s+rated`),
		regexp.MustCompile(`Money:\s+moneyFromUsageEvent`),
		regexp.MustCompile(`NanoUnits:\s+usageEv\.CostNanoUnits`),
	} {
		if term.MatchString(body) {
			t.Fatalf("authority_lifecycle must not feed stream cost into monetary Rated/Money settle (%q)", term)
		}
	}
}

// TestNoSecondAuthoritativeCustomerBalanceReducerOutsideBillingStore locks
// design Fail-if "another authoritative customer-balance reducer appears".
func TestNoSecondAuthoritativeCustomerBalanceReducerOutsideBillingStore(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var offenders []string
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// Domain billing may compute provisional Account values; durable
		// authoritative balance mutation stays in billingstore SQL/transactions.
		if strings.HasPrefix(rel, "enterprise/internal/infra/billingstore/") || strings.HasPrefix(rel, "internal/core/billing/") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		body := string(src)
		if strings.Contains(body, "SET balance_nano") || strings.Contains(body, "balance_nano =") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("authoritative customer-balance mutation must stay in billingstore:\n%s", strings.Join(offenders, "\n"))
	}
}

// TestUsageRecordBillingFinalFlowPackagesExist locks Req 17.10 ownership surface:
// route/authorize/execute/TUR/rate/settle/journal/report packages remain present
// and directionally separated.
func TestUsageRecordBillingFinalFlowPackagesExist(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	required := []string{
		"internal/core/runtime",
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("final billing flow package missing: %s (%v)", rel, err)
		}
	}
	// The billing domain moved to the Enterprise tree with task 6.5;
	// enterprise/internal/archtest keeps it off the runtime core.
	assertDirectImportsExclude(t, "./internal/core/runtime", "/enterprise/internal/infra/billingstore",
		"runtime must not import billingstore (authorize+handoff only; journal stays post-turn)")
}

// TestEventCloneIsSingleSource keeps aipapi.Event.Clone as the only full
// event copy: any other func(aipapi.Event) aipapi.Event outside pkg/aipapi
// whose name contains "clone" is a competing shallow copy that drifts.
// Intentional billing projections must not use clone in their name.
func TestEventCloneIsSingleSource(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var bad []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if pkg := PackageDirFromRel(rel); pkg == "pkg/aipapi" || strings.HasPrefix(pkg, "pkg/aipapi/") {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		importsAipapi := false
		for _, imp := range f.Imports {
			if imp.Path != nil && strings.Contains(strings.Trim(imp.Path.Value, `"`), "pkg/aipapi") {
				importsAipapi = true
				break
			}
		}
		if !importsAipapi {
			return nil
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Recv != nil {
				continue
			}
			if !strings.Contains(strings.ToLower(fd.Name.Name), "clone") {
				continue
			}
			if isAipapiEventToEvent(fd) {
				bad = append(bad, rel+": "+fd.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("event clones must use aipapi.Event.Clone, found competing copies:\n%s", strings.Join(bad, "\n"))
	}
}

// isAipapiEventToEvent reports a plain func(aipapi.Event) aipapi.Event
// signature: exactly one value Event parameter and one value Event result.
func isAipapiEventToEvent(fd *ast.FuncDecl) bool {
	if fd.Type == nil || fd.Type.Params == nil || fd.Type.Results == nil {
		return false
	}
	params := 0
	paramsEvent := false
	for _, field := range fd.Type.Params.List {
		params += len(field.Names)
		if len(field.Names) == 0 {
			params++
		}
		if isAipapiEventExpr(field.Type) {
			paramsEvent = true
		} else {
			return false
		}
	}
	if params != 1 || !paramsEvent {
		return false
	}
	results := 0
	resultsEvent := false
	for _, field := range fd.Type.Results.List {
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		results += n
		if isAipapiEventExpr(field.Type) {
			resultsEvent = true
		} else {
			return false
		}
	}
	return results == 1 && resultsEvent
}

func isAipapiEventExpr(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Event" {
		return false
	}
	_, ok = sel.X.(*ast.Ident)
	return ok
}
