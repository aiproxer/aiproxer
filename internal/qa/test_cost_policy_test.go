package qa

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/archtest"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

func TestQAFastPreflight_TestCost_ArchtestGoListCallsUseDedicatedCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		src  string
		want int
	}{
		{
			name: "command literal",
			path: "internal/archtest/bad.go",
			src:  `package archtest; import "os/exec"; var _ = exec.Command("go", "list", "./...")`,
			want: 1,
		},
		{
			name: "context command append",
			path: "internal/archtest/bad.go",
			src:  `package archtest; import "os/exec"; var _ = exec.CommandContext(nil, "go", append([]string{"list"}, "./..." )...)`,
			want: 1,
		},
		{
			name: "other go command",
			path: "internal/archtest/allowed.go",
			src:  `package archtest; import "os/exec"; var _ = exec.Command("go", "run", ".")`,
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls, err := findDirectGoToolCalls(tc.path, []byte(tc.src), "list")
			if err != nil {
				t.Fatalf("find go list calls: %v", err)
			}
			if len(calls) != tc.want {
				t.Fatalf("found %d go list calls %v, want %d", len(calls), calls, tc.want)
			}
		})
	}

	root := repositoryFile(t, "internal", "archtest")
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		calls, parseErr := findDirectGoToolCalls(path, src, "list")
		if parseErr != nil {
			return parseErr
		}
		if len(calls) == 0 {
			return nil
		}
		rel, relErr := filepath.Rel(filepath.Dir(root), path)
		if relErr != nil {
			return relErr
		}
		if filepath.Base(path) != "go_list_cache_test.go" {
			violations = append(violations, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan internal/archtest: %v", err)
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("direct os/exec go list calls must stay in go_list_cache_test.go: %s", strings.Join(violations, ", "))
	}
}

func TestQAFastPreflight_TestCost_DBParityListModePrecedesFullRun(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "cheap list branch",
			src: `package main
import "dbparity"
func runCLI(mode dbparity.RunnerMode) int {
		if mode == dbparity.ModeList {
			if true { return 0 }
			return 0
		}
		return dbparity.Run()
}`,
			want: true,
		},
		{
			name: "list branch invokes full runner",
			src: `package main
import "dbparity"
func runCLI(mode dbparity.RunnerMode) int {
		if mode == dbparity.ModeList {
			_ = dbparity.Run()
			return 0
		}
		return dbparity.Run()
}`,
			want: false,
		},
		{
			name: "full runner precedes list branch",
			src: `package main
import "dbparity"
func runCLI(mode dbparity.RunnerMode) int {
		_ = dbparity.Run()
		if mode == dbparity.ModeList { return 0 }
		return 0
}`,
			want: false,
		},
	}
	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			violations := validateDBParityListModeSource([]byte(tc.src))
			if got := len(violations) == 0; got != tc.want {
				t.Fatalf("validateDBParityListModeSource() violations=%v, valid=%v, want valid=%v", violations, got, tc.want)
			}
		})
	}

	violations := validateDBParityListModeSource([]byte(readRepositoryFile(t, "internal", "testkit", "dbparity", "cmd", "main.go")))
	if len(violations) != 0 {
		t.Fatalf("dbparity list mode must return before dbparity.Run: %s", strings.Join(violations, "; "))
	}
}

func TestQAFastPreflight_TestCost_DBParityPostgresMakePropagation(t *testing.T) {
	t.Parallel()

	const representative = "control-plane-ledger"
	catalog := dbparity.DefaultCatalog()
	component, ok := catalog.ComponentByID(representative)
	if !ok {
		t.Fatalf("catalog is missing representative component %q", representative)
	}
	if len(component.TestPackages) != 1 || component.TestPackages[0] != "internal/infra/controlplane/ledgerstore" {
		t.Fatalf("representative component must remain a single canonical ledger package, got %#v", component.TestPackages)
	}
	plans, err := dbparity.Plan(dbparity.ModePostgresDirect, dbparity.PlanOptions{
		Catalog:     catalog,
		ComponentID: representative,
		GoTestFlags: []string{"-run", "^$", "-count=1"},
		BaseEnv:     []string{dbparity.EnvTestPostgresDSN + "=postgres://user@localhost:5432/aip_test"},
	})
	if err != nil {
		t.Fatalf("plan representative PostgreSQL parity: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("representative PostgreSQL parity scope planned %d commands, want one", len(plans))
	}
	plan := plans[0]
	if plan.ComponentID != representative || plan.Package != "internal/infra/controlplane/ledgerstore" {
		t.Fatalf("unexpected representative plan: %#v", plan)
	}
	if !containsAll(plan.Args, "-run", "^$", "-count=1") {
		t.Fatalf("GO_TEST_FLAGS were not propagated into representative plan: %#v", plan.Args)
	}
	if !containsAll(plan.Env, dbparity.EnvRequirePostgres+"=1") {
		t.Fatalf("PostgreSQL plan is not fail-closed: %#v", plan.Env)
	}
}

func TestQAFastPreflight_TestCost_WalkProductionGoFilesUsesProcessCache(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	rel := filepath.FromSlash("pkg/cache_probe.go")
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	firstSource := []byte("package cacheprobe\nconst Marker = \"first\"\n")
	if err := os.WriteFile(path, firstSource, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	collect := func() map[string][]byte {
		t.Helper()
		files := make(map[string][]byte)
		err := archtest.WalkProductionGoFiles(root, func(rel, _ string, src []byte) error {
			files[rel] = append([]byte(nil), src...)
			return nil
		})
		if err != nil {
			t.Fatalf("WalkProductionGoFiles: %v", err)
		}
		return files
	}

	first := collect()
	if !bytes.Equal(first["pkg/cache_probe.go"], firstSource) {
		t.Fatalf("first walk did not read fixture: %q", first["pkg/cache_probe.go"])
	}
	secondSource := []byte("package cacheprobe\nconst Marker = \"second\"\n")
	if err := os.WriteFile(path, secondSource, 0o600); err != nil {
		t.Fatalf("mutate fixture: %v", err)
	}
	second := collect()
	if !bytes.Equal(second["pkg/cache_probe.go"], firstSource) {
		t.Fatalf("WalkProductionGoFiles did not reuse the process-level snapshot: %q", second["pkg/cache_probe.go"])
	}
}

func TestQAFastPreflight_TestCost_BackendPluginStageCacheOwnsConnectorBuild(t *testing.T) {
	t.Parallel()

	root := repositoryFile(t, "internal", "testkit", "backendplugin")
	cachePath := filepath.Join(root, "stage_cache.go")
	cacheSource, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read stage_cache.go: %v", err)
	}
	for _, marker := range []string{"connectorCache", "getCachedConnectorBinary", "stageCachedBinary"} {
		if !strings.Contains(string(cacheSource), marker) {
			t.Fatalf("stage_cache.go is missing cache owner marker %q", marker)
		}
	}

	var buildCallFiles []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		calls, parseErr := findDirectGoToolCalls(path, src, "build")
		if parseErr != nil {
			return parseErr
		}
		if len(calls) != 0 {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			buildCallFiles = append(buildCallFiles, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan backendplugin stages: %v", err)
	}
	if len(buildCallFiles) != 1 || buildCallFiles[0] != "stage_cache.go" {
		t.Fatalf("connector build must be owned only by stage_cache.go, found %v", buildCallFiles)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read backendplugin stages: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "stage_") || entry.Name() == "stage_cache.go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		src, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		if readErr != nil {
			t.Fatalf("read %s: %v", entry.Name(), readErr)
		}
		if !strings.Contains(string(src), "getCachedConnectorBinary") {
			t.Errorf("ordinary stage %s must use stage_cache.go", entry.Name())
		}
	}
}

func containsAll(values []string, wanted ...string) bool {
	for _, item := range wanted {
		if !slices.Contains(values, item) {
			return false
		}
	}
	return true
}

func findDirectGoToolCalls(filename string, src []byte, subcommand string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}
	aliases := make(map[string]struct{})
	dotImported := false
	for _, imp := range file.Imports {
		path, unquoteErr := strconv.Unquote(imp.Path.Value)
		if unquoteErr != nil || path != "os/exec" {
			continue
		}
		name := "exec"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			dotImported = true
		} else if name != "_" {
			aliases[name] = struct{}{}
		}
	}

	var calls []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		commandName := ""
		executableIndex := 0
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fun.Sel.Name != "Command" && fun.Sel.Name != "CommandContext" {
				return true
			}
			packageName, ok := fun.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, ok := aliases[packageName.Name]; !ok {
				return true
			}
			commandName = fun.Sel.Name
		case *ast.Ident:
			if !dotImported || (fun.Name != "Command" && fun.Name != "CommandContext") {
				return true
			}
			commandName = fun.Name
		default:
			return true
		}
		if commandName == "CommandContext" {
			executableIndex = 1
		}
		if len(call.Args) <= executableIndex || staticString(call.Args[executableIndex]) != "go" {
			return true
		}
		for _, arg := range call.Args[executableIndex+1:] {
			if containsStaticString(arg, subcommand) {
				calls = append(calls, filename)
				break
			}
		}
		return true
	})
	return calls, nil
}

func staticString(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return value
}

func containsStaticString(expr ast.Expr, want string) bool {
	if staticString(expr) == want {
		return true
	}
	switch node := expr.(type) {
	case *ast.CompositeLit:
		for _, element := range node.Elts {
			if containsStaticString(element, want) {
				return true
			}
		}
	case *ast.CallExpr:
		if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "append" && len(node.Args) > 0 {
			return containsStaticString(node.Args[0], want)
		}
	case *ast.ParenExpr:
		return containsStaticString(node.X, want)
	}
	return false
}

func validateDBParityListModeSource(src []byte) []string {
	file, err := parser.ParseFile(token.NewFileSet(), "dbparity_cmd.go", src, parser.SkipObjectResolution)
	if err != nil {
		return []string{"parse: " + err.Error()}
	}
	var runCLI *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "runCLI" {
			runCLI = fn
			break
		}
	}
	if runCLI == nil || runCLI.Body == nil {
		return []string{"runCLI function is missing"}
	}

	var listBranch *ast.IfStmt
	var runCalls []*ast.CallExpr
	ast.Inspect(runCLI.Body, func(node ast.Node) bool {
		switch current := node.(type) {
		case *ast.IfStmt:
			if isDBParityModeList(current.Cond) && listBranch == nil {
				listBranch = current
			}
		case *ast.CallExpr:
			if isDBParityRun(current) {
				runCalls = append(runCalls, current)
			}
		}
		return true
	})

	if listBranch == nil {
		return []string{"runCLI has no mode == dbparity.ModeList branch"}
	}
	var violations []string
	if containsDBParityRun(listBranch.Body) {
		violations = append(violations, "list branch invokes dbparity.Run")
	}
	if !astBlockAlwaysReturns(listBranch.Body) {
		violations = append(violations, "list branch does not return on every path")
	}
	if len(runCalls) == 0 {
		violations = append(violations, "runCLI has no dbparity.Run call for non-list modes")
	} else {
		for _, call := range runCalls {
			if call.Pos() <= listBranch.Pos() {
				violations = append(violations, "dbparity.Run precedes list-mode handling")
				break
			}
		}
	}
	return violations
}

func selectorPath(expr ast.Expr) []string {
	switch node := expr.(type) {
	case *ast.Ident:
		return []string{node.Name}
	case *ast.SelectorExpr:
		path := selectorPath(node.X)
		if path == nil {
			return nil
		}
		return append(path, node.Sel.Name)
	case *ast.ParenExpr:
		return selectorPath(node.X)
	default:
		return nil
	}
}

func isDBParityModeList(expr ast.Expr) bool {
	condition, ok := expr.(*ast.BinaryExpr)
	return ok && condition.Op == token.EQL &&
		((strings.EqualFold(strings.Join(selectorPath(condition.X), "."), "mode") && strings.Join(selectorPath(condition.Y), ".") == "dbparity.ModeList") ||
			(strings.EqualFold(strings.Join(selectorPath(condition.Y), "."), "mode") && strings.Join(selectorPath(condition.X), ".") == "dbparity.ModeList"))
}

func isDBParityRun(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && strings.Join(selectorPath(selector), ".") == "dbparity.Run"
}

func containsDBParityRun(block *ast.BlockStmt) bool {
	if block == nil {
		return false
	}
	found := false
	ast.Inspect(block, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && isDBParityRun(call) {
			found = true
			return false
		}
		return !found
	})
	return found
}

func astBlockAlwaysReturns(block *ast.BlockStmt) bool {
	if block == nil {
		return false
	}
	return slices.ContainsFunc(block.List, astStatementAlwaysReturns)
}

func astStatementAlwaysReturns(statement ast.Stmt) bool {
	switch node := statement.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BlockStmt:
		return astBlockAlwaysReturns(node)
	case *ast.IfStmt:
		return node.Else != nil && astBlockAlwaysReturns(node.Body) && astElseAlwaysReturns(node.Else)
	default:
		return false
	}
}

func astElseAlwaysReturns(statement ast.Stmt) bool {
	if nested, ok := statement.(*ast.IfStmt); ok {
		return astStatementAlwaysReturns(nested)
	}
	return astStatementAlwaysReturns(statement)
}
