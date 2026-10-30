package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 1.4: sync.WaitGroup must not be the general request/lease refcounter (req 10.4).
func TestGenerationManager_ProhibitsWaitGroupAsRequestRefcounter(t *testing.T) {
	t.Parallel()
	t.Run("fixture_request_lease_waitgroup_detected", func(t *testing.T) {
		t.Parallel()
		src := `package runtimehost
import "sync"
type requestLease struct{ wg sync.WaitGroup }
func (l *requestLease) Acquire() { l.wg.Add(1) }
func (l *requestLease) Release() { l.wg.Done() }
`
		if hits := scanWaitGroupRequestRefcounter("generation_manager.go", src); len(hits) == 0 {
			t.Fatal("expected WaitGroup request-refcounter detection")
		}
	})
	t.Run("fixture_lifecycle_worker_waitgroup_allowed", func(t *testing.T) {
		t.Parallel()
		src := `package runtimehost
import "sync"
func (w *lifecycleWorker) run() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); w.quiesce() }()
	wg.Wait()
}
`
		if hits := scanWaitGroupRequestRefcounter("lifecycle_worker.go", src); len(hits) != 0 {
			t.Fatalf("lifecycle worker WaitGroup must be allowed: %v", hits)
		}
	})
	t.Run("live_runtimehost_tree", func(t *testing.T) {
		t.Parallel()
		root := filepath.Join("..", "infra", "runtimehost")
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				t.Skip("runtimehost package not present yet")
			}
			t.Fatal(err)
		}
		var violations []string
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(root, name)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			violations = append(violations, scanWaitGroupRequestRefcounter(filepath.ToSlash(path), string(src))...)
		}
		if len(violations) != 0 {
			t.Fatalf("WaitGroup request refcounter in runtimehost:\n%s", strings.Join(violations, "\n"))
		}
	})
}

func scanWaitGroupRequestRefcounter(filename, src string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return []string{"parse: " + err.Error()}
	}
	syncAlias := map[string]bool{}
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) != "sync" {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name != "_" && imp.Name.Name != "." {
				syncAlias[imp.Name.Name] = true
			}
			continue
		}
		syncAlias["sync"] = true
	}
	isWG := func(expr ast.Expr) bool {
		sel, ok := expr.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && syncAlias[pkg.Name] && sel.Sel.Name == "WaitGroup"
	}
	requestShape := func(name string) bool {
		n := strings.ToLower(name)
		for _, needle := range []string{"request", "lease", "pin", "retain", "refcount", "refcounter"} {
			if strings.Contains(n, needle) {
				return true
			}
		}
		return false
	}
	var hits []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || !requestShape(ts.Name.Name) {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			for _, field := range st.Fields.List {
				if isWG(field.Type) {
					hits = append(hits, fset.Position(field.Pos()).String()+": WaitGroup field on "+ts.Name.Name)
				}
			}
		}
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		recv := recvTypeName(fd.Recv.List[0].Type)
		leaseMethod := fd.Name.Name == "Acquire" || fd.Name.Name == "Release" ||
			fd.Name.Name == "Retain" || fd.Name.Name == "TryRetain"
		if !requestShape(recv) && !leaseMethod && !requestShape(fd.Name.Name) {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Add" && sel.Sel.Name != "Done" && sel.Sel.Name != "Wait") {
				return true
			}
			flag := false
			switch x := sel.X.(type) {
			case *ast.Ident:
				flag = x.Name == "wg" || strings.Contains(strings.ToLower(x.Name), "waitgroup")
			case *ast.SelectorExpr:
				flag = x.Sel.Name == "wg" || strings.Contains(strings.ToLower(x.Sel.Name), "waitgroup") || isWG(x)
			}
			if flag {
				hits = append(hits, fset.Position(call.Pos()).String()+": "+fd.Name.Name+" WaitGroup."+sel.Sel.Name)
			}
			return true
		})
	}
	return hits
}

func recvTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return recvTypeName(e.X)
	default:
		return ""
	}
}
