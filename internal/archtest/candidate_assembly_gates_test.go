package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateAssembly_NoExportedCandidateRuntime(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	err := walkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if !strings.HasPrefix(filepath.ToSlash(rel), "internal/infra/runtimebundle/") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, abs, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name == nil {
				return true
			}
			if ts.Name.Name == "CandidateRuntime" {
				t.Errorf("%s: CandidateRuntime must not remain; use package-private candidateAssembly", rel)
			}
			if ts.Name.Name == "ReloadHost" {
				t.Errorf("%s: ReloadHost must not remain; use concrete Host", rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCandidateAssembly_GroupedPrivateType(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal/infra/runtimebundle/candidate_http.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var assem *ast.StructType
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "candidateAssembly" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		assem = st
		return false
	})
	if assem == nil {
		t.Fatal("missing candidateAssembly struct")
	}
	flat := 0
	groups := map[string]bool{}
	for _, field := range assem.Fields.List {
		for _, name := range field.Names {
			if name == nil {
				continue
			}
			switch name.Name {
			case "execution", "security", "models", "operations", "process":
				groups[name.Name] = true
			case "ledger", "lifeMu", "lifeClaimed", "ledgerTransferred", "terminalWorkReady", "terminalWorkRT":
				// allowed transfer/lifecycle state
			default:
				flat++
				t.Errorf("unexpected top-level candidateAssembly field %s (want grouped fields)", name.Name)
			}
		}
	}
	for _, want := range []string{"execution", "security", "models", "operations", "process"} {
		if !groups[want] {
			t.Errorf("candidateAssembly missing group %s", want)
		}
	}
}

func TestCandidateAssembly_NoProcessTracingShutdownPlaceholder(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	err := walkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if !strings.Contains(string(src), "ProcessTracingShutdown") {
			return nil
		}
		if strings.Contains(filepath.ToSlash(rel), "/archtest/") {
			return nil
		}
		t.Errorf("%s: ProcessTracingShutdown placeholder must not remain", rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
