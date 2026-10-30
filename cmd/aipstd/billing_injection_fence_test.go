package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestBuildHost_DoesNotInjectBillingWithoutProduction locks the stock binary
// fence: runServeCommand must not set Production on BuildHost, must not call
// ComposeBilling, and a flag-unset serve-shaped BuildHost must not cut over
// billing (req 1.1, 1.2, 8.6).
func TestBuildHost_DoesNotInjectBillingWithoutProduction(t *testing.T) {
	t.Parallel()
	assertServeCommandBuildHostOmitsProduction(t)
	assertServeCommandDoesNotCallComposeBilling(t)

	ctx := context.Background()
	cfgPath := bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost without Production: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })
}

func assertServeCommandBuildHostOmitsProduction(t *testing.T) {
	t.Helper()
	src, err := os.ReadFile("command.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "command.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var foundLit int
	inspectRunServeCommand(t, f, func(body *ast.BlockStmt) {
		ast.Inspect(body, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isBuildHostInputLit(lit) {
				return true
			}
			foundLit++
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if ok && key.Name == "Production" {
					t.Fatal("runServeCommand BuildHostInput must not set Production")
				}
			}
			return true
		})
	})
	if foundLit != 1 {
		t.Fatalf("runServeCommand BuildHostInput literals=%d want 1", foundLit)
	}
}

func assertServeCommandDoesNotCallComposeBilling(t *testing.T) {
	t.Helper()
	src, err := os.ReadFile("command.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "command.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	inspectRunServeCommand(t, f, func(body *ast.BlockStmt) {
		ast.Inspect(body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if x.Name == "ComposeBilling" {
					t.Fatal("runServeCommand must not call ComposeBilling")
				}
			case *ast.SelectorExpr:
				if x.Sel != nil && x.Sel.Name == "ComposeBilling" {
					t.Fatal("runServeCommand must not call ComposeBilling")
				}
			}
			return true
		})
	})
}

func inspectRunServeCommand(t *testing.T, f *ast.File, fn func(*ast.BlockStmt)) {
	t.Helper()
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "runServeCommand" || fd.Body == nil {
			return true
		}
		found = true
		fn(fd.Body)
		return false
	})
	if !found {
		t.Fatal("command.go must declare runServeCommand")
	}
}

func isBuildHostInputLit(lit *ast.CompositeLit) bool {
	if lit == nil {
		return false
	}
	switch typ := lit.Type.(type) {
	case *ast.Ident:
		return typ.Name == "BuildHostInput"
	case *ast.SelectorExpr:
		return typ.Sel != nil && typ.Sel.Name == "BuildHostInput"
	default:
		return false
	}
}
