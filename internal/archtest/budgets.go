package archtest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Explicit maintainer-approved prospective capacity for
// billing-uncertain-component-overlap task 1.1. These are planned growth limits,
// not audited production lines; historical audit pins remain unchanged.
const (
	billingUncertainOverlapCoreReserve    = 1500
	billingUncertainOverlapOverlayReserve = billingUncertainOverlapCoreReserve + 250
)

// Only these paths may consume the reserve. New assessor files retain baseline
// zero/new provenance in the fork-anchored manifest; their absence credits zero.
var billingUncertainOverlapFileReserve = map[string]int{
	"internal/core/billing/component_rater_advisory.go":        450,
	"internal/core/billing/component_rater_advisory_graph.go":  450,
	"internal/core/billing/component_rater.go":                 100,
	"internal/core/billing/component_rater_support.go":         100,
	"internal/core/billing/component_rater_overlap.go":         100,
	"internal/core/billing/component_rater_quantity_solver.go": 100,
	"internal/core/billing/retail_rating.go":                   200,
	"internal/infra/billingcompose/catalog.go":                 250,
}

// CriticalFileBudget caps the non-test line count for hotspot files.
type CriticalFileBudget struct {
	Path string
	Max  int
}

// CriticalFileBudgets is the fixed ceiling table for hotspot files. Each value
// uses the measured line count plus max(100, ceil(50% of measured)), rounded up
// to the next 50 lines. The exactness test pins selected audited snapshots;
// budget checks enforce every fixed cap against the live source.
var CriticalFileBudgets = []CriticalFileBudget{
	{Path: "internal/core/runtime/executor.go", Max: 300},
	{Path: "internal/infra/runtimebundle/options.go", Max: 300},
	{Path: "internal/standardplugins/standard_table.go", Max: 350},
	{Path: "internal/pluginreg/reg.go", Max: 650},
	{Path: "internal/stdhttp/server.go", Max: 150},
	{Path: "internal/infra/runtimehost/coordinator.go", Max: 450},
	{Path: "internal/infra/runtimehost/generation.go", Max: 450},
	{Path: "internal/infra/runtimebundle/candidate_compile.go", Max: 450},
	{Path: "internal/infra/runtimebundle/handler_composer.go", Max: 150},
	{Path: "internal/infra/runtimebundle/compile_generation.go", Max: 600},
	{Path: "internal/stdhttp/request_plane.go", Max: 200},
	{Path: "internal/infra/runtimebundle/process_services.go", Max: 550},
	{Path: "pkg/aipruntime/build.go", Max: 150},
	{Path: "pkg/aipruntime/host.go", Max: 200},
	{Path: "pkg/aipruntime/facade.go", Max: 200},
	{Path: "cmd/aipstd/command.go", Max: 650},
	{Path: "pkg/aipruntime/reload.go", Max: 200},
	{Path: "pkg/aipruntime/reload_aliases.go", Max: 150},
	{Path: "pkg/aipsdk/backendplugin/convert.go", Max: 600},
	{Path: "pkg/aipsdk/backendplugin/convert_frames.go", Max: 650},
	{Path: "internal/core/securesession/adapters/bunstore/store.go", Max: 500},
	{Path: "internal/core/securesession/adapters/bunstore/store_evidence.go", Max: 450},
	{Path: "internal/core/runtime/authority_lifecycle.go", Max: 450},
	{Path: "internal/core/runtime/authority_lifecycle_settle.go", Max: 600},
	{Path: "internal/core/runtime/authority_lifecycle_release.go", Max: 500},
	{Path: "internal/plugins/protocols/openresponses/state_machine.go", Max: 1050},
	{Path: "internal/plugins/protocols/openresponses/state_machine_event_handlers.go", Max: 750},
	{Path: "internal/plugins/frontends/frontendpipe/pipe.go", Max: 750},
	{Path: "internal/plugins/features/keepwarm/manager.go", Max: 700},
	{Path: "internal/plugins/features/keepwarm/scheduler.go", Max: 700},
	{Path: "internal/standardplugins/featurehost/runtime.go", Max: 300},
	{Path: "internal/standardplugins/featurehost/process.go", Max: 300},
	{Path: "internal/standardplugins/featurehost/generation.go", Max: 400},
	{Path: "internal/standardplugins/featurehost/inputs.go", Max: 250},
	{Path: "internal/standardplugins/featurehost/bindings.go", Max: 350},
}

// PackageTreeBudget caps recursive non-test .go lines for a package tree.
type PackageTreeBudget struct {
	Tree string
	Max  int
}

// PackageTreeBudgets uses measured lines plus max(5000, ceil(50% of measured)),
// rounded up to the next 1000. Values are fixed snapshots and match overlapping
// LineBudgets entries exactly.
var PackageTreeBudgets = []PackageTreeBudget{
	{Tree: "internal/infra/runtimebundle", Max: 22000},
	{Tree: "internal/standardplugins/featurehost", Max: 9000},
	{Tree: "internal/stdhttp", Max: 14000},
	{Tree: "cmd/aipstd", Max: 6000},
	{Tree: "pkg/aipruntime", Max: 6000},
}

// LineBudget caps recursive non-test lines for a broader architectural layer.
type LineBudget struct {
	Dir string
	Max int
}

// LineBudgets uses the same fixed recursive-tree policy as PackageTreeBudgets.
// Overlapping entries must stay synchronized with the package-tree table.
var LineBudgets = []LineBudget{
	{Dir: "internal/core", Max: 216000},
	{Dir: "internal/pluginreg", Max: 7000},
	{Dir: "internal/stdhttp", Max: 14000},
	{Dir: "internal/infra/runtimebundle", Max: 22000},
	{Dir: "internal/standardplugins/featurehost", Max: 9000},
	{Dir: "internal/compactionfacts", Max: 6000},
	{Dir: "internal/capabilityfacts", Max: 6000},
	{Dir: "cmd/aipstd", Max: 6000},
	{Dir: "pkg/aipruntime", Max: 6000},
}

// budgetBoundaryError reports excess over a ceiling; empty means within budget.
// Deletions always pass (measured may sit arbitrarily far below the ceiling);
// only excess fails. Shared by every budget-pin assertion.
func budgetBoundaryError(label string, measured, ceiling int) string {
	if measured > ceiling {
		return fmt.Sprintf("%s: measured %d exceeds ceiling %d", label, measured, ceiling)
	}
	return ""
}

// CountNonTestGoLines recursively counts physical lines in non-test .go files.
func CountNonTestGoLines(dir string) (int, error) {
	var total int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		n, err := countTreeFileLines(path)
		if err != nil {
			return err
		}
		total += n
		return nil
	})
	return total, err
}

func countTreeFileLines(path string) (n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// CountFileLines counts physical lines in one file.
func CountFileLines(path string) (int, error) {
	return countTreeFileLines(path)
}

// FormatRuntimeConvergencePackageBudgets renders the advisory Markdown section.
func FormatRuntimeConvergencePackageBudgets(root string) (string, error) {
	var b strings.Builder
	fmt.Fprintln(&b, "## Runtime-convergence package budgets")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Tree | Non-test lines | Budget |")
	fmt.Fprintln(&b, "| --- | --- | --- |")
	for _, budget := range PackageTreeBudgets {
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(budget.Tree)))
		if err != nil {
			fmt.Fprintf(&b, "| `%s` | (missing: %v) | %d |\n", budget.Tree, err, budget.Max)
			continue
		}
		fmt.Fprintf(&b, "| `%s` | %d | %d |\n", budget.Tree, n, budget.Max)
	}
	fmt.Fprintln(&b)
	return b.String(), nil
}
