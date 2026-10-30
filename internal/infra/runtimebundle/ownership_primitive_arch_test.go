package runtimebundle_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// TestOwnershipPrimitiveStaysPrivate locks the new ownership primitives to
// package-private composition infrastructure. It fails if a helper becomes
// exported or gains service-locator method names (req 1.3, 2.5, 6.4).
func TestOwnershipPrimitiveStaysPrivate(t *testing.T) {
	t.Parallel()

	files := []string{"process_owner.go", "generation_loop.go"}
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name == nil {
					continue
				}
				// Free functions must stay unexported. Receiver methods are on
				// unexported types and remain inaccessible outside the package.
				if d.Recv == nil && isExported(d.Name.Name) {
					t.Fatalf("%s: exported helper %q in ownership primitive (req 2.5, 6.4)", name, d.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					if isExported(ts.Name.Name) {
						t.Fatalf("%s: exported type %q in ownership primitive (req 2.5, 6.4)", name, ts.Name.Name)
					}
				}
			}
		}
	}

	// The process owner must expose no service-locator methods.
	src, err := os.ReadFile("process_owner.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "process_owner.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	locatorMethods := map[string]bool{"Get": true, "Resolve": true, "Provide": true, "Lookup": true, "Fetch": true}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Name == nil {
			continue
		}
		if locatorMethods[fd.Name.Name] {
			t.Fatalf("process ownership primitive grew service-locator method %q (req 1.3, 2.5)", fd.Name.Name)
		}
	}
}

// TestMigratedBuildersDropCloserSlices locks the migrated process builders to
// no longer return caller-visible closer lists (req 3.1, 3.5, 6.5).
func TestMigratedBuildersDropCloserSlices(t *testing.T) {
	t.Parallel()

	builders := map[string]string{
		"buildConcurrencyAuthorityRuntime":  "concurrency_authority.go",
		"buildConcurrencyLeaseStore":        "concurrency_authority.go",
		"buildPersistenceRuntime":           "build_persistence.go",
		"buildProcessAccountingStores":      "token_accounting.go",
		"buildMeteringRuntime":              "metering.go",
		"openDurableMeteringJournal":        "metering.go",
		"buildTerminalWorkWithSetReconcile": "lease_set_reconcile.go",
		"buildTerminalWorkFromProduction":   "terminal_work.go",
		"buildTerminalWorkRuntime":          "terminal_work.go",
	}

	for fnName, file := range builders {
		fd := funcDeclFromFile(t, file, fnName)
		if fd == nil {
			t.Fatalf("builder %s not found in %s", fnName, file)
		}
		if resultsReturnFuncErrorSlice(fd) {
			t.Fatalf("builder %s still returns a caller-visible []func() error closer list (req 3.1, 6.5)", fnName)
		}
	}
}

// TestGenerationLoopOwnershipCentralized locks the model-registry refresh path
// to the structured loop helper instead of manual cancel/join wiring (req 4.1).
func TestGenerationLoopOwnershipCentralized(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("build_model.go")
	if err != nil {
		t.Fatal(err)
	}
	// The migrated path must use the structured helper and must no longer
	// construct a manual refreshCancel/refreshWG pair for the registry loop.
	if !strings.Contains(string(src), "startOwnedLoop") {
		t.Fatal("build_model.go must centralize model-registry refresh ownership via startOwnedLoop (req 4.1)")
	}
	if strings.Contains(string(src), "model-registry-refresh") && strings.Contains(string(src), "refreshCancel") {
		t.Fatal("build_model.go must not retain manual cancel wiring for the registry refresh loop (req 4.1)")
	}
}

func funcDeclFromFile(t *testing.T, file, name string) *ast.FuncDecl {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != name {
			continue
		}
		return fd
	}
	return nil
}

func resultsReturnFuncErrorSlice(fd *ast.FuncDecl) bool {
	if fd.Type == nil || fd.Type.Results == nil {
		return false
	}
	for _, field := range fd.Type.Results.List {
		if isFuncErrorSliceType(field.Type) {
			return true
		}
	}
	return false
}

func isExported(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}
