package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

const (
	hostOwnershipReloadHostPath = "internal/infra/runtimebundle/reload_host.go"
	hostOwnershipHostBuildPath  = "internal/infra/runtimebundle/host_build.go"
	aipruntimeHostAdapterPath   = "pkg/aipruntime/host.go"
	aipstdCommandPath           = "cmd/aipstd/command.go"
)

// Ownership field names that must stay private on the concrete Host type.
var hostOwnershipPrivateFields = map[string]bool{
	"coordinator": true, "manager": true, "process": true, "executor": true,
	"dispatcher": true, "source": true, "effective": true, "config": true,
	"shutdownTracing": true, "closeMu": true, "closeAttempt": true, "closed": true,
	"processClosed": true, "tracingClosed": true, "logger": true,
	"activeSource": true, "fixedStreamRecovery": true,
}

// Exported aliases of ownership fields that must not reappear.
var hostOwnershipForbiddenExported = map[string]bool{
	"Coordinator": true, "Manager": true, "Process": true, "Executor": true,
	"Source": true, "Effective": true, "Config": true, "ShutdownTracing": true,
	"ActiveSource": true, "FixedStreamRecovery": true, "Logger": true,
	"Dispatcher": true,
}

func TestHostOwnership_NoReloadHostSymbol(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	err := walkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if strings.Contains(filepath.ToSlash(rel), "/archtest/") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, abs, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if x.Name == "ReloadHost" {
					t.Errorf("%s: ReloadHost identifier must not remain in production code", rel)
				}
			case *ast.TypeSpec:
				if x.Name != nil && x.Name.Name == "ReloadHost" {
					t.Errorf("%s: ReloadHost type must be renamed to Host", rel)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHostOwnership_ConcreteHostFieldsPrivate(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, hostOwnershipReloadHostPath)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var hostType *ast.StructType
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Host" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		hostType = st
		return false
	})
	if hostType == nil {
		t.Fatalf("%s: missing concrete type Host struct", hostOwnershipReloadHostPath)
	}
	for _, field := range hostType.Fields.List {
		for _, name := range field.Names {
			if name == nil {
				continue
			}
			if ast.IsExported(name.Name) {
				t.Errorf("Host field %s must be private (ownership encapsulation)", name.Name)
			}
			// Additional private fields are allowed; only exported fields are forbidden above.
		}
	}
}

func TestHostOwnership_NoHostTypeAlias(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, hostOwnershipHostBuildPath)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Host" {
			return true
		}
		if _, isStruct := ts.Type.(*ast.StructType); isStruct {
			return true
		}
		if _, isIdent := ts.Type.(*ast.Ident); isIdent {
			t.Fatalf("%s: Host must be a concrete struct, not a type alias", hostOwnershipHostBuildPath)
		}
		if _, isSel := ts.Type.(*ast.SelectorExpr); isSel {
			t.Fatalf("%s: Host must be a concrete struct, not a type alias", hostOwnershipHostBuildPath)
		}
		return true
	})
}

func TestHostOwnership_AdaptHostUsesPublicSurface(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, aipruntimeHostAdapterPath)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var adapt *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "adaptHost" {
			return true
		}
		adapt = fd
		return false
	})
	if adapt == nil || adapt.Body == nil {
		t.Fatal("missing adaptHost")
	}
	forbidden := []string{"Manager", "Process", "Executor", "Coordinator", "ShutdownTracing"}
	ast.Inspect(adapt.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		for _, name := range forbidden {
			if sel.Sel.Name == name {
				t.Errorf("adaptHost must not inspect ownership field %s", name)
			}
		}
		return true
	})
}

func TestHostOwnership_AipstdUsesPublicSurface(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, aipstdCommandPath)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	callFuns := map[*ast.SelectorExpr]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			callFuns[sel] = true
		}
		return true
	})
	// PrepareInspect returns *InspectPrepared (inspect/doctor session), not *Host.
	// Public session fields Config/Registry are allowed only on those bindings.
	inspectSessionIdents := prepareInspectSessionIdents(f)
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || callFuns[sel] {
			return true
		}
		if !hostOwnershipForbiddenExported[sel.Sel.Name] {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && inspectSessionIdents[id.Name] {
			switch sel.Sel.Name {
			case "Config", "Registry":
				return true
			}
		}
		t.Errorf("%s: production aipstd must not access Host ownership field %s", aipstdCommandPath, sel.Sel.Name)
		return true
	})
}

// prepareInspectSessionIdents returns local idents bound from runtimebundle.PrepareInspect.
func prepareInspectSessionIdents(f *ast.File) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "PrepareInspect" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "runtimebundle" {
			return true
		}
		if len(assign.Lhs) == 0 {
			return true
		}
		if id, ok := assign.Lhs[0].(*ast.Ident); ok && id.Name != "" && id.Name != "_" {
			out[id.Name] = true
		}
		return true
	})
	return out
}
