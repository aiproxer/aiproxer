package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
)

// TestFeatureHost_NoRequestPathResolver verifies featurehost exposes NO
// service locator, dynamic resolver, or request-time lookup methods (Req 8.4, Task 2.2).
func TestFeatureHost_NoRequestPathResolver(t *testing.T) {
	t.Parallel()

	rtType := reflect.TypeFor[*featurehost.Runtime]()
	forbidden := []string{"Resolve", "Get", "Lookup", "Service", "Services", "GetService", "ResolveService"}

	for m := range rtType.Methods() {
		methodName := m.Name
		for _, f := range forbidden {
			if strings.EqualFold(methodName, f) {
				t.Fatalf("featurehost.Runtime must not expose request-path resolver/service locator: %s", methodName)
			}
		}
	}
}

// TestFeatureHost_PrivacyFromRuntimeBundle verifies that runtimebundle does not
// access unexported fields or internal private subpackages of featurehost.
func TestFeatureHost_PrivacyFromRuntimeBundle(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	root := filepath.Join("..", "infra", "runtimebundle")

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}

		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(importPath, "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/") {
				t.Errorf("runtimebundle file %s illegally imports private featurehost subpackage: %s", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}
}

// TestFeatureHost_CoreDoesNotImportFeatureHost verifies that core packages do not import featurehost.
func TestFeatureHost_CoreDoesNotImportFeatureHost(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	root := filepath.Join("..", "core")

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}

		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(importPath, "standardplugins/featurehost") {
				t.Errorf("core file %s illegally imports featurehost: %s", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}
}

// TestFeatureHost_NoPreHandoffFeatureConstruction verifies that featurehost
// exposes no production-capable pre-handoff feature constructor inputs or
// test-injection APIs (Tasks 2.1/2.3, Requirements 2.5/8.4). Before the atomic
// Task 7.3 handoff, no external caller may construct, adopt, or inject
// feature-owned process resources or closers through the facade.
func TestFeatureHost_NoPreHandoffFeatureConstruction(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	root := filepath.Join("..", "standardplugins", "featurehost")

	forbiddenIdents := map[string]bool{
		"TerminalPolicyConfig":       true,
		"NewProcessWithStepsForTest": true,
		"ConstructionStep":           true,
		"RegisterCloserForTest":      true,
		"NewRuntimeForTest":          true,
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		ast.Inspect(file, func(n ast.Node) bool {
			if x, ok := n.(*ast.Ident); ok {
				if forbiddenIdents[x.Name] {
					t.Errorf("featurehost file %s must not declare or reference pre-handoff injection API %s", path, x.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}
}

// TestFeatureHost_NoConcreteFeatureAccessors verifies that featurehost does
// not expose concrete coordinator/parent-port/detector accessors outside its
// package: only the minimal detector consumer port in generation output and
// the transition-guard observers belong to the public surface (Task 3.3).
// Distinctness and counting checks live in package-local featurehost tests.
func TestFeatureHost_NoConcreteFeatureAccessors(t *testing.T) {
	t.Parallel()

	forbidden := []string{"BranchCoordinator", "CompactionParentPort"}
	fset := token.NewFileSet()
	root := filepath.Join("..", "standardplugins", "featurehost")

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				return true
			}
			for _, name := range forbidden {
				if fn.Name.Name == name {
					t.Errorf("featurehost file %s must not expose concrete accessor %s", path, name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}
}

// TestFeatureHost_NoPackageLevelReasoningEntryPointsInRuntimeBundle verifies that runtimebundle prod
// code does not expose separate validation/binding paths alongside Runtime.CompileGeneration (R4).
func TestFeatureHost_NoPackageLevelReasoningEntryPointsInRuntimeBundle(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "infra", "runtimebundle")
	retiredFile := filepath.Join(root, "reasoning_preservation_compression.go")
	if _, err := os.Stat(retiredFile); err == nil {
		t.Fatalf("reasoning_preservation_compression.go must not exist; reasoning options have been removed from runtimebundle")
	}

	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if fn.Name.Name == "validateReasoningPreservationCompressionGeneration" ||
				fn.Name.Name == "bindReasoningPreservationCompression" {
				t.Fatalf("runtimebundle file %s must not expose package-level reasoning entry point %s (R4)", path, fn.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestFeatureHost_RegistrationCollectionsNotUsedInRequestHotPaths verifies that
// request-time and runtime hot packages (internal/core/*, internal/stdhttp/*)
// do not import or use pkg/aipsdk/featurehost registration collections (Task 8.1, Req 9.4).
func TestFeatureHost_RegistrationCollectionsNotUsedInRequestHotPaths(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	roots := []string{
		filepath.Join("..", "core"),
		filepath.Join("..", "stdhttp"),
	}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}

			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				if strings.Contains(importPath, "pkg/aipsdk/featurehost") {
					t.Errorf("hot path file %s illegally imports registration collections from %s", path, importPath)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WalkDir failed for root %s: %v", root, err)
		}
	}
}
