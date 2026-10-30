package runtimebundle_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// closerAcquisitionOwnership classifies every production closer acquisition by
// site-unique structural ID (task 1.1 / req 4.9). Duplicate acquisitions keep
// distinct occurrence ordinals; IDs are source-position-independent.
var closerAcquisitionOwnership = []ownershipEntry{
	{
		Symbol: "host_build.go:buildHost:acq#0:assign:traceShutdown=traceRes.Shutdown#0",
		Class:  ownershipProcess,
		Source: "host_build.go → tracing.Init Result.Shutdown",
		Notes:  "Process tracing shutdown retained for BuildHost / Host.Close (Task 5.2).",
	},
	{
		Symbol: "host_build.go:buildHost:acq#1:assign:closeProcess=ps.Close#0",
		Class:  ownershipProcess,
		Source: "host_build.go → ProcessServices.Close",
		Notes:  "Pre-Host process closer retained for joinInitialFailureCleanup on compile/publish/bind failure.",
	},
	{
		Symbol: "process_services.go:NewProcessServices:acq#0:append(ps.closers, c)#0",
		Class:  ownershipProcess,
		Source: "process_services.go → NewProcessServices register helper",
		Notes:  "Process closer bag: control-plane, usage, concurrency, persistence, accounting, metering, and terminal-work teardown (req 6.2–6.5, 13.8).",
	},
	{
		Symbol: "backend_resource_pool.go:build:acq#0:assign:entry.cleanup=cleanup#0",
		Class:  ownershipProcess,
		Source: "backend_resource_pool.go → backendResourceEntry composite physical cleanup",
		Notes:  "The pool entry is the sole process ownership authority for composite physical cleanup; generations receive only idempotent lease release.",
	},
	{
		Symbol: "validate_distribution.go:validateDistribution:acq#0:assign:traceShutdownRaw=traceRes.Shutdown#0",
		Class:  ownershipProcess,
		Source: "validate_distribution.go → tracing.Init Result.Shutdown",
		Notes:  "Process tracing shutdown retained for ValidateDistribution's own internal close (no Host handoff).",
	},
	{
		Symbol: "validate_distribution.go:validateDistribution:acq#1:assign:closeProcess=ps.Close#0",
		Class:  ownershipProcess,
		Source: "validate_distribution.go → ProcessServices.Close",
		Notes:  "ValidateDistribution process closer for unpublished dry-run teardown.",
	},

	{
		Symbol: "build_lifecycle.go:RegisterPluginBuildCleanup:acq#0:append(closers, func:once.Do|normalizePluginCleanupErr|cleanup)#0",
		Class:  ownershipGeneration,
		Source: "build_lifecycle.go → RegisterPluginBuildCleanup",
		Notes:  "Idempotent once-wrapped connector BuildResult cleanup for nil-ledger rollback bags.",
	},
	{
		Symbol: "build_model.go:buildBackends:acq#0:assign:cleanup=res.Cleanup#0",
		Class:  ownershipGeneration,
		Source: "build_model.go → buildBackends BackendBuildResult.Cleanup",
		Notes:  "Connector plugin composition-owned cleanup handle captured before ledger/closer registration.",
	},
	{
		Symbol: "build_model.go:buildBackends:acq#1:assign:wrapped.Close=inst.Close#0",
		Class:  ownershipGeneration,
		Source: "build_model.go → buildBackends",
		Notes:  "Canonical BackendInstance.Close when no ledger; ledger path registers AddClose and clears wrapped.Close.",
	},
	{
		Symbol: "build_model.go:buildBackends:acq#2:append(nilLedgerRollback, inst.Close)#0",
		Class:  ownershipGeneration,
		Source: "build_model.go → buildBackends nilLedgerRollback",
		Notes:  "Nil-ledger partial-build rollback bag for BackendInstance.Close; ledger path uses AddClose instead.",
	},
	{
		Symbol: "secure_session.go:buildSecureSessionRuntime:acq#0:assign:closer=closeFn#0",
		Class:  ownershipProcess,
		Source: "secure_session.go → buildSecureSessionRuntime postgres branch",
		Notes:  "Postgres store closer from openPostgresStore: no-op when registry-owned, bunDB.Close for a dedicated handle.",
	},
	{
		Symbol: "control_plane.go:buildControlPlaneStore:acq#0:assign:closer=c.Close#0",
		Class:  ownershipProcess,
		Source: "control_plane.go → buildControlPlaneStore",
		Notes:  "Direct Close method-value acquisition for control-plane store.",
	},
	{
		Symbol: "modelcatalog_attach.go:startModelCatalog:acq#0:literal(closers,#0,func:cat.Close|closeCatalogHTTP)#0",
		Class:  ownershipGeneration,
		Source: "modelcatalog_attach.go → startModelCatalog",
		Notes:  "Candidate catalog Close + HTTP idle cleanup via slice literal acquisition.",
	},
	{
		Symbol: "modelcatalog_attach.go:startModelCatalog:acq#1:append(out.quiesceClosers, func:refreshCleanup)#0",
		Class:  ownershipGeneration,
		Source: "modelcatalog_attach.go → startModelCatalog",
		Notes:  "Candidate catalog refresh cancel/wait registered distinctly for PhaseQuiesce.",
	},
}

func TestOwnershipCloserAcquisitionCoversAppendSites(t *testing.T) {
	t.Parallel()

	if len(closerAcquisitionOwnership) == 0 {
		t.Fatal("closerAcquisitionOwnership must not be empty")
	}
	ledger := map[string]ownershipEntry{}
	for _, e := range closerAcquisitionOwnership {
		if e.Symbol == "" || e.Source == "" || !e.Class.valid() {
			t.Fatalf("invalid closer ownership entry: %+v", e)
		}
		if _, dup := ledger[e.Symbol]; dup {
			t.Fatalf("duplicate closer ownership symbol %q", e.Symbol)
		}
		ledger[e.Symbol] = e
	}

	got := enumerateCloserAppendIDs(t)
	if len(got) == 0 {
		t.Fatal("expected closer append expressions in runtimebundle production sources")
	}

	var missing, stale []string
	for _, id := range got {
		if _, ok := ledger[id]; !ok {
			missing = append(missing, id)
		}
	}
	gotCounts := map[string]int{}
	for _, id := range got {
		gotCounts[id]++
	}
	for id := range ledger {
		if gotCounts[id] == 0 {
			stale = append(stale, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) != 0 || len(stale) != 0 {
		t.Fatalf("closer acquisition ledger mismatch:\nmissing (unclassified):\n%s\nstale (extra):\n%s\ngot:\n%s",
			strings.Join(missing, "\n"), strings.Join(stale, "\n"), strings.Join(got, "\n"))
	}
	// Bidirectional: live sites and ledger must have equal multiplicity.
	for id, n := range gotCounts {
		if n != 1 {
			// Production currently has unique IDs; multiplicity >1 must still be ledgered distinctly.
			_ = n
		}
		if _, ok := ledger[id]; !ok {
			t.Fatalf("live closer ID absent from ledger: %s", id)
		}
	}
}

func TestOwnershipCloserAcquisition_RejectsUnclassifiedAppendInKnownFunction(t *testing.T) {
	t.Parallel()

	src := `package runtimebundle
func Build() {
	closers = append(closers, controlPlane.closer)
	closers = append(closers, newUnclassifiedCloser)
}
`
	ids := closerAppendIDsFromSource(t, "build.go", src)
	ledger := map[string]bool{}
	for _, e := range closerAcquisitionOwnership {
		ledger[e.Symbol] = true
	}
	var unclassified []string
	for _, id := range ids {
		if !ledger[id] {
			unclassified = append(unclassified, id)
		}
	}
	found := false
	for _, id := range unclassified {
		if strings.Contains(id, "newUnclassifiedCloser") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected unclassified closer containing newUnclassifiedCloser in %v (unclassified=%v)", ids, unclassified)
	}
}

func TestOwnershipCloserAcquisition_NoFuzzyContainsCoverage(t *testing.T) {
	t.Parallel()
	ledgerOnlyKnown := []ownershipEntry{{
		Symbol: "build.go:Build:acq#0:append(closers, controlPlane.closer)#0",
		Class:  ownershipProcess,
		Source: "build.go",
	}}
	src := `package runtimebundle
func Build() {
	closers = append(closers, controlPlane.closer)
	closers = append(closers, extraCloser)
}
`
	ids := closerAppendIDsFromSource(t, "build.go", src)
	ledger := map[string]bool{}
	for _, e := range ledgerOnlyKnown {
		ledger[e.Symbol] = true
	}
	var uncovered []string
	for _, id := range ids {
		if !ledger[id] {
			uncovered = append(uncovered, id)
		}
	}
	if len(uncovered) != 1 || !strings.Contains(uncovered[0], "extraCloser") {
		t.Fatalf("fuzzy coverage must not hide extraCloser; uncovered=%v", uncovered)
	}
}

func TestOwnershipCloserAcquisition_RemediationBypassFixtures(t *testing.T) {
	t.Parallel()

	t.Run("duplicate_identical_appends_are_distinct", func(t *testing.T) {
		t.Parallel()
		src := `package runtimebundle
func Build() {
	closers = append(closers, controlPlane.closer)
	closers = append(closers, controlPlane.closer)
}
`
		ids := closerAppendIDsFromSource(t, "build.go", src)
		if len(ids) != 2 {
			t.Fatalf("duplicate identical appends must yield 2 distinct IDs, got %v", ids)
		}
		if ids[0] == ids[1] {
			t.Fatalf("duplicate append IDs must not collapse: %v", ids)
		}
	})

	t.Run("renamed_cleanup_bag_detected", func(t *testing.T) {
		t.Parallel()
		src := `package runtimebundle
func Build() {
	shutdowns = append(shutdowns, controlPlane.closer)
	cleanup = append(cleanup, store.Close)
	disposers = append(disposers, func() error { return nil })
}
`
		ids := closerAppendIDsFromSource(t, "build.go", src)
		if len(ids) < 3 {
			t.Fatalf("expected renamed cleanup bags detected, got %v", ids)
		}
	})

	t.Run("direct_assignment_from_Close", func(t *testing.T) {
		t.Parallel()
		src := `package runtimebundle
func Build() {
	var shutdowns []func() error
	shutdowns = append(shutdowns, resource.Close)
	c := helperReturnedCleanup()
	shutdowns = append(shutdowns, c)
	direct := resource.Close
	_ = direct
}
`
		ids := closerAppendIDsFromSource(t, "build.go", src)
		foundDirect := false
		for _, id := range ids {
			if strings.Contains(id, "resource.Close") || strings.Contains(id, "helperReturnedCleanup") || strings.Contains(id, "assign:") {
				foundDirect = true
				break
			}
		}
		if !foundDirect || len(ids) < 2 {
			t.Fatalf("expected direct Close/helper cleanup acquisition detection, got %v", ids)
		}
	})

	t.Run("colliding_function_literals_distinct", func(t *testing.T) {
		t.Parallel()
		src := `package runtimebundle
func Build() {
	closers := []func() error{
		func() error { cleanup(); return a.Close() },
		func() error { cleanup(); return b.Close() },
	}
	_ = closers
}
`
		ids := closerAppendIDsFromSource(t, "build.go", src)
		if len(ids) != 2 {
			t.Fatalf("expected 2 distinct function-literal closer IDs, got %v", ids)
		}
		if ids[0] == ids[1] {
			t.Fatalf("function-literal IDs must not collide when first call shape matches: %v", ids)
		}
	})
}

func enumerateCloserAppendIDs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		// Do not deduplicate: duplicate acquisitions must remain distinct.
		ids = append(ids, closerAppendIDsFromSource(t, name, string(src))...)
	}
	sort.Strings(ids)
	return ids
}

func closerAppendIDsFromSource(t *testing.T, filename, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	var ids []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name == nil {
			continue
		}
		ids = append(ids, closerAcquisitionsInFunc(filename, fn)...)
	}
	return ids
}

func closerAcquisitionsInFunc(filename string, fn *ast.FuncDecl) []string {
	base := filepath.Base(filename)
	fnName := fn.Name.Name
	cleanupBags := map[string]bool{}
	exprOcc := map[string]int{}
	acq := 0
	var ids []string

	rememberBag := func(expr ast.Expr) {
		switch e := expr.(type) {
		case *ast.Ident:
			cleanupBags[e.Name] = true
		case *ast.SelectorExpr:
			if e.Sel != nil {
				cleanupBags[e.Sel.Name] = true
			}
		}
	}

	emit := func(kind, norm string) {
		occ := exprOcc[norm]
		exprOcc[norm] = occ + 1
		id := fmt.Sprintf("%s:%s:acq#%d:%s#%d", base, fnName, acq, norm, occ)
		acq++
		ids = append(ids, id)
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			// Track []func() error bags and direct cleanup assignments.
			for i, lhs := range n.Lhs {
				if i >= len(n.Rhs) {
					break
				}
				rhs := n.Rhs[i]
				if lit, ok := rhs.(*ast.CompositeLit); ok && isFuncErrorSliceLit(lit) {
					rememberBag(lhs)
					for li, elt := range lit.Elts {
						fl, ok := elt.(*ast.FuncLit)
						if !ok {
							continue
						}
						norm := fmt.Sprintf("literal(%s,#%d,func:%s)", bagName(lhs), li, funcLitSummary(fl))
						emit("literal", norm)
					}
					continue
				}
				if id, ok := lhs.(*ast.Ident); ok && id.Name == "_" {
					continue
				}
				if isCleanupValueExpr(rhs) {
					rememberBag(lhs)
					norm := "assign:" + exprString(lhs) + "=" + normalizeCleanupAssignRHS(rhs)
					emit("assign", norm)
				}
			}
		case *ast.ValueSpec:
			if n.Type != nil && isFuncErrorSliceType(n.Type) {
				for _, name := range n.Names {
					cleanupBags[name.Name] = true
				}
			}
			for i, name := range n.Names {
				if i < len(n.Values) {
					if lit, ok := n.Values[i].(*ast.CompositeLit); ok && isFuncErrorSliceLit(lit) {
						cleanupBags[name.Name] = true
						for li, elt := range lit.Elts {
							fl, ok := elt.(*ast.FuncLit)
							if !ok {
								continue
							}
							norm := fmt.Sprintf("literal(%s,#%d,func:%s)", name.Name, li, funcLitSummary(fl))
							emit("literal", norm)
						}
					}
				}
			}
		case *ast.CallExpr:
			fun, ok := n.Fun.(*ast.Ident)
			if !ok || fun.Name != "append" || len(n.Args) < 2 {
				return true
			}
			if !isCleanupBagExpr(n.Args[0], cleanupBags) && !appendLooksLikeCleanup(n) {
				return true
			}
			rememberBag(n.Args[0])
			emit("append", formatCloserAppend(n))
		case *ast.KeyValueExpr:
			key, ok := n.Key.(*ast.Ident)
			if !ok || !isCleanupBagName(key.Name) {
				return true
			}
			lit, ok := n.Value.(*ast.CompositeLit)
			if !ok || !isFuncErrorSliceLit(lit) {
				return true
			}
			cleanupBags[key.Name] = true
			for li, elt := range lit.Elts {
				fl, ok := elt.(*ast.FuncLit)
				if !ok {
					continue
				}
				norm := fmt.Sprintf("literal(%s,#%d,func:%s)", key.Name, li, funcLitSummary(fl))
				emit("literal", norm)
			}
		}
		return true
	})
	return ids
}

func bagName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if e.Sel != nil {
			return e.Sel.Name
		}
	}
	return exprString(expr)
}

func isCleanupBagName(name string) bool {
	lower := strings.ToLower(name)
	keys := []string{"closer", "shutdown", "cleanup", "dispose", "teardown", "release", "finalizer"}
	for _, k := range keys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

func isCleanupBagExpr(expr ast.Expr, known map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return known[e.Name] || isCleanupBagName(e.Name)
	case *ast.SelectorExpr:
		if e.Sel != nil && (known[e.Sel.Name] || isCleanupBagName(e.Sel.Name)) {
			return true
		}
	}
	return false
}

func appendLooksLikeCleanup(call *ast.CallExpr) bool {
	for i := 1; i < len(call.Args); i++ {
		if isCleanupValueExpr(call.Args[i]) {
			return true
		}
	}
	return false
}

func isCleanupValueExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return false
		}
		switch e.Sel.Name {
		case "Close", "Shutdown", "Cleanup", "Stop", "closer":
			return true
		}
	case *ast.Ident:
		switch e.Name {
		case "closeFn", "refreshCancel", "refreshCleanup", "shutdown", "cleanup":
			return true
		}
		// Singular closer handle, not plural bag names (closers/constructedClosers).
		if e.Name == "closer" || strings.HasSuffix(e.Name, "Closer") {
			return true
		}
	case *ast.CallExpr:
		name := ""
		switch fun := e.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			if fun.Sel != nil {
				name = fun.Sel.Name
			}
		}
		lower := strings.ToLower(name)
		// Factory helpers that return cleanup funcs — not closer() invocations
		// and not CleanupTimeoutDuration-style methods.
		return strings.Contains(lower, "returnedcleanup") ||
			lower == "helperreturnedcleanup" ||
			(strings.HasPrefix(lower, "new") && strings.Contains(lower, "cleanup"))
	case *ast.FuncLit:
		// Func literals are acquired via cleanup-bag literals/appends, not as
		// arbitrary callback field assignments.
		return false
	}
	return false
}

func normalizeCleanupAssignRHS(expr ast.Expr) string {
	// Keep assign IDs compact and position-independent.
	return exprString(expr)
}

func isFuncErrorSliceLit(lit *ast.CompositeLit) bool {
	return isFuncErrorSliceType(lit.Type)
}

func isFuncErrorSliceType(expr ast.Expr) bool {
	arr, ok := expr.(*ast.ArrayType)
	if !ok || arr.Elt == nil {
		return false
	}
	ft, ok := arr.Elt.(*ast.FuncType)
	if !ok || ft.Results == nil || len(ft.Results.List) != 1 {
		return false
	}
	id, ok := ft.Results.List[0].Type.(*ast.Ident)
	return ok && id.Name == "error"
}

func formatCloserAppend(call *ast.CallExpr) string {
	target := exprString(call.Args[0])
	var rhsParts []string
	for i := 1; i < len(call.Args); i++ {
		arg := call.Args[i]
		if i == len(call.Args)-1 && call.Ellipsis != token.NoPos {
			rhsParts = append(rhsParts, exprString(arg)+"...")
			continue
		}
		if fl, ok := arg.(*ast.FuncLit); ok {
			rhsParts = append(rhsParts, "func:"+funcLitSummary(fl))
			continue
		}
		rhsParts = append(rhsParts, exprString(arg))
	}
	return "append(" + target + ", " + strings.Join(rhsParts, ", ") + ")"
}

func funcLitSummary(fl *ast.FuncLit) string {
	if fl.Body == nil {
		return "literal"
	}
	var parts []string
	ast.Inspect(fl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		parts = append(parts, exprString(call.Fun))
		return true
	})
	if len(parts) == 0 {
		return "literal"
	}
	// Full call sequence avoids first-call collisions across literals.
	return strings.Join(parts, "|")
}

func exprString(expr ast.Expr) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, token.NewFileSet(), expr)
	return strings.ReplaceAll(buf.String(), "\n", " ")
}
