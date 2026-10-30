package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/archtest"
)

const hexagonalBaselineRelPath = "testdata/architecture/hexagonal_migration_baseline.json"

type pkgMeta struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Imports    []string
}

type baselineFile struct {
	Packages []baselineEntry `json:"packages"`
}

type baselineEntry struct {
	GoListPattern     string           `json:"go_list_pattern"`
	Classification    string           `json:"classification"`
	Role              string           `json:"role,omitempty"`
	RetirementTrigger string           `json:"retirement_trigger"`
	Backlog           *baselineBacklog `json:"backlog,omitempty"`
}

type baselineBacklog struct {
	Owner            string `json:"owner"`
	NextExtraction   string `json:"next_extraction"`
	RetirementTarget string `json:"retirement_target"`
	Status           string `json:"status"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}
	modPath, err := modulePath(ctx, root)
	if err != nil {
		fatal(err)
	}
	pkgs, err := packageImportGraph(ctx, root)
	if err != nil {
		fatal(err)
	}

	var b strings.Builder
	fmt.Fprintln(&b, "# Architecture report")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Produced by `make arch-report`. Package/fan tables are advisory; Requirement 11.5 net shrinkage is enforced (non-zero exit on FAIL).")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Module: `%s`\n\n", modPath)

	writePackageLineReport(&b, pkgs)
	writeHotspotReport(&b, root)
	writeRuntimeConvergencePackageBudgets(&b, root)
	shrinkagePass := writeRuntimeConvergenceShrinkage(&b, root)
	writeRuntimeConvergenceExceptions(&b, root)
	writeRuntimeConvergenceAffectedFanIO(&b, pkgs, modPath)
	writeFanOutReport(&b, pkgs, modPath)
	writeFanInReport(&b, pkgs, modPath)
	writeExportedSymbolsReport(&b, root)
	writeBaselineClassifications(&b, root)
	writeExtensionPlanesReport(&b, root)
	writeRuntimeConvergenceDeletedInventory(&b)

	if err := archtest.WriteExtensionPlanesBaseline(root); err != nil {
		fmt.Fprintf(os.Stderr, "arch-report: write extension planes baseline warning: %v\n", err)
	}

	fmt.Print(b.String())
	if !shrinkagePass {
		fmt.Fprintln(os.Stderr, "arch-report: Requirement 11.5 net shrinkage gate FAILED (see Runtime-convergence net shrinkage section)")
		os.Exit(1)
	}
}

func writeExtensionPlanesReport(b *strings.Builder, root string) {
	section, err := archtest.FormatExtensionPlanesReport(root)
	if err != nil {
		fmt.Fprintf(b, "## Extension plane declaration and generation status\n\n(could not measure: %v)\n\n", err)
		return
	}
	fmt.Fprint(b, section)
}

func writePackageLineReport(b *strings.Builder, pkgs []pkgMeta) {
	fmt.Fprintln(b, "## Non-test lines by package (top 25)")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Package | Lines |")
	fmt.Fprintln(b, "| --- | --- |")

	type row struct {
		path  string
		lines int
	}
	rows := make([]row, 0, len(pkgs))
	for _, p := range pkgs {
		n := 0
		for _, f := range p.GoFiles {
			lines, err := countFileLines(filepath.Join(p.Dir, f))
			if err != nil {
				continue
			}
			n += lines
		}
		rows = append(rows, row{path: p.ImportPath, lines: n})
	}
	slices.SortFunc(rows, func(a, b row) int {
		if a.lines != b.lines {
			return cmp.Compare(b.lines, a.lines)
		}
		return cmp.Compare(a.path, b.path)
	})
	writeLimitRows(b, rows, 25, func(r row) string {
		return fmt.Sprintf("| `%s` | %d |", r.path, r.lines)
	})
}

func writeHotspotReport(b *strings.Builder, root string) {
	fmt.Fprintln(b, "## Hotspot files (critical-file budgets)")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| File | Lines | Budget |")
	fmt.Fprintln(b, "| --- | --- | --- |")
	for _, budget := range archtest.CriticalFileBudgets {
		n, err := countFileLines(filepath.Join(root, budget.Path))
		if err != nil {
			fmt.Fprintf(b, "| `%s` | (missing) | %d |\n", budget.Path, budget.Max)
			continue
		}
		fmt.Fprintf(b, "| `%s` | %d | %d |\n", budget.Path, n, budget.Max)
	}
	fmt.Fprintln(b)
}

func writeRuntimeConvergencePackageBudgets(b *strings.Builder, root string) {
	section, err := archtest.FormatRuntimeConvergencePackageBudgets(root)
	if err != nil {
		fmt.Fprintf(b, "## Runtime-convergence package budgets\n\n(could not measure: %v)\n\n", err)
		return
	}
	fmt.Fprint(b, section)
}

func writeRuntimeConvergenceShrinkage(b *strings.Builder, root string) bool {
	section, m, err := archtest.FormatRuntimeConvergenceShrinkage(root)
	if err != nil {
		fmt.Fprintf(b, "## Runtime-convergence net shrinkage (Req 11.5)\n\n(could not measure: %v)\n\n", err)
		return false
	}
	fmt.Fprint(b, section)
	return m.Pass
}

func writeRuntimeConvergenceAffectedFanIO(b *strings.Builder, pkgs []pkgMeta, modPath string) {
	fmt.Fprintln(b, "## Runtime-convergence affected-surface fan-in/out")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Surface | Fan-out (direct internal) | Fan-in | Importers |")
	fmt.Fprintln(b, "| --- | ---: | ---: | --- |")

	internalPrefix := modPath + "/internal/"
	targets := []string{
		"internal/infra/runtimebundle",
		"internal/infra/runtimehost",
		"internal/stdhttp",
		"cmd/aipstd",
		"pkg/aipruntime",
	}
	byPath := make(map[string]pkgMeta, len(pkgs))
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	for _, rel := range targets {
		ip := modPath + "/" + rel
		p, ok := byPath[ip]
		if !ok {
			fmt.Fprintf(b, "| `%s` | (missing) | (missing) | |\n", rel)
			continue
		}
		fanOut := 0
		for _, imp := range p.Imports {
			if strings.HasPrefix(imp, internalPrefix) {
				fanOut++
			}
		}
		var importers []string
		for _, q := range pkgs {
			if q.ImportPath == ip {
				continue
			}
			if slices.Contains(q.Imports, ip) {
				importers = append(importers, strings.TrimPrefix(q.ImportPath, modPath+"/"))
			}
		}
		slices.Sort(importers)
		impCol := "(none)"
		if len(importers) > 0 {
			impCol = "`" + strings.Join(importers, "`, `") + "`"
		}
		fmt.Fprintf(b, "| `%s` | %d | %d | %s |\n", rel, fanOut, len(importers), impCol)
	}
	fmt.Fprintln(b)
}

func writeRuntimeConvergenceDeletedInventory(b *strings.Builder) {
	fmt.Fprintln(b, "## Runtime-convergence deleted production symbols/paths")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "Enforced absent by `internal/archtest` deleted-symbol / bootstrap / serve gates (allowlist empty):")
	fmt.Fprintln(b)
	for _, tok := range []string{
		"runtimebundle.Built",
		"runtimebundle.Build (compatibility orchestrator)",
		"stdhttp.RunWithRuntime",
		"requestPlaneAsBuilt",
		"NewStandardHandler",
		"standardHTTPInputFromBuilt",
		"releaseBuiltResources",
		"runClosers",
		"LegacyClosers",
		"BuildBootstrap / BootstrapResult / AttachReloadHost",
		"LoadBootstrapEffective / BootstrapMode",
		"pkg/aipruntime deprecated Options / legacy_options adapter",
		"pkg/aipruntime/reload_map.go (mirrored reload model)",
	} {
		fmt.Fprintf(b, "- `%s`\n", tok)
	}
	fmt.Fprintln(b)
	fmt.Fprintln(b, "Parallel production runtime composition paths and mirrored reload models: **zero remaining** (empty `runtime_convergence_allowlist.json`; host_path/config_load permanently zero-tolerance).")
	fmt.Fprintln(b)
}

func writeRuntimeConvergenceExceptions(b *strings.Builder, root string) {
	fmt.Fprintln(b, "## Remaining runtime-convergence compatibility exceptions")
	fmt.Fprintln(b)
	path := filepath.Join(root, "internal", "archtest", "testdata", "architecture", "runtime_convergence_allowlist.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(b, "(could not read allowlist: %v)\n\n", err)
		return
	}
	var doc struct {
		Description string `json:"description"`
		Entries     []struct {
			Gate           string `json:"gate"`
			Path           string `json:"path"`
			Identity       string `json:"identity"`
			Classification string `json:"classification"`
			RetirementTask string `json:"retirement_task"`
			Rationale      string `json:"rationale"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(b, "(could not decode allowlist: %v)\n\n", err)
		return
	}
	fmt.Fprintf(b, "%s\n\n", strings.TrimSpace(doc.Description))
	if len(doc.Entries) == 0 {
		fmt.Fprintln(b, "(none)")
		fmt.Fprintln(b)
		return
	}
	fmt.Fprintln(b, "| Gate | Path | Identity | Retirement task |")
	fmt.Fprintln(b, "| --- | --- | --- | --- |")
	for _, e := range doc.Entries {
		fmt.Fprintf(b, "| `%s` | `%s` | `%s` | %s |\n", e.Gate, e.Path, e.Identity, e.RetirementTask)
	}
	fmt.Fprintln(b)
}

func writeFanOutReport(b *strings.Builder, pkgs []pkgMeta, modPath string) {
	fmt.Fprintln(b, "## Direct internal import fan-out (top 20)")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Package | DirectInternalImports |")
	fmt.Fprintln(b, "| --- | --- |")

	internalPrefix := modPath + "/internal/"
	type row struct {
		path string
		n    int
	}
	rows := make([]row, 0, len(pkgs))
	for _, p := range pkgs {
		if !strings.HasPrefix(p.ImportPath, internalPrefix) {
			continue
		}
		n := 0
		for _, imp := range p.Imports {
			if strings.HasPrefix(imp, internalPrefix) {
				n++
			}
		}
		rows = append(rows, row{path: p.ImportPath, n: n})
	}
	slices.SortFunc(rows, func(a, b row) int {
		if a.n != b.n {
			return cmp.Compare(b.n, a.n)
		}
		return cmp.Compare(a.path, b.path)
	})
	writeLimitRows(b, rows, 20, func(r row) string {
		return fmt.Sprintf("| `%s` | %d |", r.path, r.n)
	})
}

func writeFanInReport(b *strings.Builder, pkgs []pkgMeta, modPath string) {
	fmt.Fprintln(b, "## Direct internal import fan-in (top 20)")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Package | ImportedBy |")
	fmt.Fprintln(b, "| --- | --- |")

	internalPrefix := modPath + "/internal/"
	importers := map[string]int{}
	for _, p := range pkgs {
		for _, imp := range p.Imports {
			if strings.HasPrefix(imp, internalPrefix) {
				importers[imp]++
			}
		}
	}
	type row struct {
		path string
		n    int
	}
	rows := make([]row, 0, len(importers))
	for p, n := range importers {
		rows = append(rows, row{path: p, n: n})
	}
	slices.SortFunc(rows, func(a, b row) int {
		if a.n != b.n {
			return cmp.Compare(b.n, a.n)
		}
		return cmp.Compare(a.path, b.path)
	})
	writeLimitRows(b, rows, 20, func(r row) string {
		return fmt.Sprintf("| `%s` | %d |", r.path, r.n)
	})
}

func writeExportedSymbolsReport(b *strings.Builder, root string) {
	fmt.Fprintln(b, "## Exported symbols (public contracts)")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Package | ExportedSymbols |")
	fmt.Fprintln(b, "| --- | --- |")
	for _, rel := range []string{"pkg/aipapi", "pkg/aipsdk"} {
		n, err := exportedSymbols(filepath.Join(root, rel))
		if err != nil {
			fmt.Fprintf(b, "| `%s` | (error: %v) |\n", rel, err)
			continue
		}
		fmt.Fprintf(b, "| `%s` | %d |\n", rel, n)
	}
	fmt.Fprintln(b)
}

func writeBaselineClassifications(b *strings.Builder, root string) {
	fmt.Fprintln(b, "## Hexagonal baseline classifications")
	fmt.Fprintln(b)

	raw, err := os.ReadFile(filepath.Join(root, hexagonalBaselineRelPath))
	if err != nil {
		fmt.Fprintf(b, "(could not read baseline: %v)\n\n", err)
		return
	}
	var doc baselineFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(b, "(could not decode baseline: %v)\n\n", err)
		return
	}

	fmt.Fprintln(b, "| Package | Class | Role | Retirement target / next extraction |")
	fmt.Fprintln(b, "| --- | --- | --- | --- |")
	for _, row := range doc.Packages {
		var notes string
		if row.Backlog != nil {
			notes = row.Backlog.RetirementTarget + " → " + row.Backlog.NextExtraction
		} else {
			notes = row.RetirementTrigger
		}
		role := row.Role
		if role == "" {
			role = "-"
		}
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n",
			strings.TrimPrefix(row.GoListPattern, "./"), row.Classification, role, notes)
	}
	fmt.Fprintln(b)
}

func writeLimitRows[T any](b *strings.Builder, rows []T, limit int, format func(T) string) {
	if len(rows) < limit {
		limit = len(rows)
	}
	for _, r := range rows[:limit] {
		fmt.Fprintln(b, format(r))
	}
	fmt.Fprintln(b)
}

func exportedSymbols(pkgDir string) (int, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return 0, err
	}
	fset := token.NewFileSet()
	var count int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(pkgDir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			return 0, err
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name != nil && s.Name.IsExported() {
							count++
						}
					case *ast.ValueSpec:
						for _, name := range s.Names {
							if name.IsExported() {
								count++
							}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Name != nil && d.Name.IsExported() {
					count++
				}
			}
		}
	}
	return count, nil
}

func countFileLines(path string) (n int, err error) {
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

func modulePath(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-m")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func packageImportGraph(ctx context.Context, root string) ([]pkgMeta, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-json", "-test=false", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var pkgs []pkgMeta
	for dec.More() {
		var p pkgMeta
		if err := dec.Decode(&p); err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for range 12 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not find go.mod above %s", wd)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "arch-report: %v\n", err)
	os.Exit(1)
}
