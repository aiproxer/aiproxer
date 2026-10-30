package tools_test

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	backendpluginrunner "github.com/aiproxer/aiproxer/tools/backendplugin/runner"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

func TestBoundedOrchestration_ModulePhaseLabels(t *testing.T) {
	t.Parallel()
	result := backendpluginrunner.Run(context.Background(), backendpluginrunner.Request{
		Argv: []string{os.Args[0], "-test.run=TestBoundedOrchestrationChildSuccess"},
		Dir:  t.TempDir(),
		Env:  []string{"GO_WANT_HELPER_PROCESS=1"},
		// Generous guard budget: the child is a race-instrumented re-invocation of
		// this test binary, and merely starting it can exceed a 1s deadline under
		// -race on a loaded runner. Deadline propagation is asserted separately by
		// TestBoundedOrchestration_CleansDescendants with an intentional 25ms cap.
		Timeout: 30 * time.Second,
		Output:  taskrunner.Capture,
		Label:   "module:example:build",
	})
	if result.Kind != taskrunner.Success || result.Label != "module:example:build" {
		t.Fatalf("unexpected result: kind=%s label=%q err=%v", result.Kind, result.Label, result.Err)
	}
}

func TestBoundedOrchestration_StopsDependentPhase(t *testing.T) {
	t.Parallel()
	result := backendpluginrunner.Run(context.Background(), backendpluginrunner.Request{
		Argv: []string{os.Args[0], "-test.run=TestBoundedOrchestrationChildFailure"},
		Dir:  t.TempDir(),
		Env:  []string{"GO_WANT_HELPER_PROCESS=1"},
		// Generous guard budget for the same race-instrumented child-startup reason
		// as TestBoundedOrchestration_ModulePhaseLabels.
		Timeout: 30 * time.Second,
		Output:  taskrunner.Capture,
		Label:   "module:example:test",
	})
	if result.Kind != taskrunner.ChildFailure {
		t.Fatalf("expected child failure propagation, got %s (%v)", result.Kind, result.Err)
	}
	if err := backendpluginrunner.Error(result); !strings.Contains(err.Error(), "module:example:test") {
		t.Fatalf("missing phase label in error: %v", err)
	}
}

func TestBoundedOrchestration_CleansDescendants(t *testing.T) {
	t.Parallel()
	result := backendpluginrunner.Run(context.Background(), backendpluginrunner.Request{
		Argv:    []string{os.Args[0], "-test.run=TestBoundedOrchestrationChildSleep"},
		Dir:     t.TempDir(),
		Env:     []string{"GO_WANT_HELPER_PROCESS=1"},
		Timeout: 25 * time.Millisecond,
		Output:  taskrunner.Capture,
		Label:   "module:example:timeout",
	})
	if result.Kind != taskrunner.DeadlineExceeded {
		t.Fatalf("expected timeout propagation, got %s (%v)", result.Kind, result.Err)
	}
	if !result.Cleanup.Attempted {
		t.Fatal("timeout did not attempt process-tree cleanup")
	}
}

func TestBoundedOrchestrationChildSuccess(t *testing.T) { t.Parallel() }

func TestBoundedOrchestrationChildFailure(t *testing.T) {
	t.Parallel()
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		os.Exit(23)
	}
}

func TestBoundedOrchestrationChildSleep(t *testing.T) {
	t.Parallel()
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		time.Sleep(time.Hour)
	}
}

func readRepoRootFile(t *testing.T, name ...string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join(".", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(append([]string{root}, name...)...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(name...), err)
	}
	return string(body)
}

func TestBoundedOrchestration_StaticAndFullProfiles(t *testing.T) {
	t.Parallel()
	mainGo := readRepoRootFile(t, "tools", "backendplugin", "release_gates", "main.go")
	makefile := readRepoRootFile(t, "Makefile")
	windowsTask := readRepoRootFile(t, "scripts", "windows-task.ps1")

	if !strings.Contains(mainGo, `"mode", "static"`) {
		t.Fatal("static release mode must remain the default QA path")
	}
	if !strings.Contains(mainGo, `"full"`) {
		t.Fatal("full release mode must remain an explicit opt-in")
	}
	for _, marker := range []string{
		"-mode=static", "-Timeout \"15m\"", "backend-plugin-release-gates-static", "release_gates",
		"-mode=full", "-Timeout \"120m\"",
	} {
		if !strings.Contains(windowsTask, marker) {
			t.Fatalf("windows task router lost %q", marker)
		}
	}
	for _, file := range []string{
		"release_gates/main.go", "release_gates/catalog.go",
		"release_gates/conformance.go", "release_gates/tidy_check.go",
	} {
		body := readRepoRootFile(t, "tools", "backendplugin", file)
		if !strings.Contains(body, "Label:") || !strings.Contains(body, "release_gates:") {
			t.Errorf("%s must label every runner request with a release_gates: prefix", file)
		}
	}
	if !strings.Contains(makefile, "backend-plugin-release-gates: backend-plugin-release-gates-static") {
		t.Fatal("full release mode must build on the static release gate")
	}
	if !strings.Contains(makefile, "qa: quality-checks-fast qa-tests lint vuln backend-plugin-release-gates-static") {
		t.Fatal("make qa must keep static-only release wiring (full profile stays opt-in)")
	}
	if strings.Contains(makefile, "backend-plugin-release-gates-static: backend-plugin-release-gates") {
		t.Fatal("static release must not depend on the full release profile")
	}
}

func TestBoundedOrchestration_NoUnboundedProductionExec(t *testing.T) {
	t.Parallel()
	root := filepath.Join(".")
	files := []string{
		"crossplatform_qa/main.go",
		"package_plugins/main.go",
		"isolated_root_qa/main.go",
		"installed_plugin_smoke/main.go",
		"release_gates/main.go",
		"release_gates/catalog.go",
		"release_gates/conformance.go",
		"release_gates/tidy_check.go",
	}
	for _, name := range files {
		path := filepath.Join(root, name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if strings.Contains(text, "exec.Command(") || strings.Contains(text, ".CombinedOutput()") || strings.Contains(text, ".Output()") {
			t.Errorf("%s still contains an unbounded production subprocess", name)
		}
	}
}

func extractASTString(expr ast.Expr) (string, bool) {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			s, err := strconv.Unquote(v.Value)
			if err == nil {
				return s, true
			}
			return strings.Trim(v.Value, `"`), true
		}
	case *ast.BinaryExpr:
		if v.Op == token.ADD {
			left, okL := extractASTString(v.X)
			right, okR := extractASTString(v.Y)
			if okL && okR {
				return left + right, true
			}
		}
	}
	return "", false
}

func hasSelectFlag(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		if s, ok := extractASTString(arg); ok {
			if s == "-select" || strings.HasPrefix(s, "-select=") || strings.HasPrefix(s, "-select ") {
				return true
			}
		}
	}
	return false
}

func isToolInvocationCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name == "runTool" || fn.Name == "runToolExpectError"
	case *ast.SelectorExpr:
		if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "exec" && (fn.Sel.Name == "Command" || fn.Sel.Name == "CommandContext") {
			return true
		}
	}
	return false
}

func TestBoundedOrchestration_OrdinaryIntegrationTestsExplicitlySelect(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	testFiles, err := filepath.Glob(filepath.Join(".", "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(testFiles) == 0 {
		t.Fatal("no test files found in tools/backendplugin")
	}

	var violations []string

	for _, file := range testFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		f, err := parser.ParseFile(fset, file, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// Only inspect actual test functions, distinguishing them from helper setup
			if !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				if !isToolInvocationCall(call) {
					return true
				}

				isPackagePlugins := false
				isCrossPlatformQA := false
				isFullProfile := false

				for i, arg := range call.Args {
					s, ok := extractASTString(arg)
					if !ok {
						continue
					}
					if strings.Contains(s, "package_plugins") {
						isPackagePlugins = true
					}
					if strings.Contains(s, "crossplatform_qa") {
						isCrossPlatformQA = true
					}
					if s == "-profile" && i+1 < len(call.Args) {
						if next, ok := extractASTString(call.Args[i+1]); ok && next == "full" {
							isFullProfile = true
						}
					}
					if strings.HasPrefix(s, "-profile=full") {
						isFullProfile = true
					}
				}

				hasSelect := hasSelectFlag(call)
				pos := fset.Position(call.Pos())

				if isPackagePlugins && isFullProfile && !hasSelect {
					violations = append(violations, fmt.Sprintf("%s:%d: %s invokes package_plugins with -profile full without explicit -select (must bound fixture breadth with -select)", filepath.Base(pos.Filename), pos.Line, fn.Name.Name))
				}
				if isCrossPlatformQA && !hasSelect {
					violations = append(violations, fmt.Sprintf("%s:%d: %s invokes crossplatform_qa without explicit -select (must bound fixture breadth with -select)", filepath.Base(pos.Filename), pos.Line, fn.Name.Name))
				}
				return true
			})
		}
	}

	if len(violations) > 0 {
		t.Fatalf("found %d unbounded backend-plugin test invocations in ordinary integration tests:\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}
