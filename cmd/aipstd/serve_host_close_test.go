package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestServe_PostHostFailureUsesHostCloseSeam proves access/auth and management
// startup failures delegate to closeServeHostAfterBuild / Host.Close and do not
// reconstruct Manager/Process ownership or call ShutdownTracing directly.
func TestServe_PostHostFailureUsesHostCloseSeam(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("command.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "command.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var closeCalls, rollbackCalls, processClose int
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "runServeCommand" || fd.Body == nil {
			return true
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				switch fun.Name {
				case "closeServeHostAfterBuild":
					closeCalls++
				case "serveHostRollback", "serveStartupRollback", "serveRollbackCore":
					rollbackCalls++
				case "closeServeProcessServices", "retireServeGenerations", "deferHostTracingShutdown":
					processClose++
				}
			}
			return true
		})
		return false
	})
	if closeCalls < 2 {
		t.Fatalf("runServeCommand closeServeHostAfterBuild calls=%d want >=2 (access/auth + management)", closeCalls)
	}
	if rollbackCalls != 0 {
		t.Fatalf("runServeCommand must not use legacy rollback reconstruction; calls=%d", rollbackCalls)
	}
	if processClose != 0 {
		t.Fatalf("runServeCommand must not close process/generations/tracing directly; hits=%d", processClose)
	}
	text := string(src)
	if strings.Contains(text, "serveHostRollback") {
		t.Fatal("serveHostRollback must remain deleted from serve path")
	}
	// Task 7.4: the serve return path is one Host.Close (tracing-last is owned
	// by the host), so the temporary tracing defer boundary is gone.
	for _, gone := range []string{"tracingDeferred", "deferHostTracingShutdown"} {
		if strings.Contains(text, gone) {
			t.Fatalf("%s must be deleted; serve shutdown is one Host.Close", gone)
		}
	}
}

// TestServe_PassesCompleteHostToGenerationServeAdapter proves cmd hands the
// complete Host to stdhttp instead of decomposing Manager/Process/Coordinator.
func TestServe_PassesCompleteHostToGenerationServeAdapter(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("command.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "command.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "GenerationHostInput" {
			return true
		}
		found = true
		hostField := false
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "Manager", "Process", "Coordinator", "ShutdownTracing":
				t.Fatalf("serve input must not carry host internal %s", key.Name)
			case "Host":
				hostField = true
				if id, ok := kv.Value.(*ast.Ident); !ok || id.Name != "host" {
					t.Fatalf("serve input Host must be the complete host value, got %T", kv.Value)
				}
			}
		}
		if !hostField {
			t.Fatal("serve input must pass the complete Host")
		}
		return false
	})
	if !found {
		t.Fatal("runServeCommand must construct stdhttp.GenerationHostInput")
	}
}

// TestCloseServeHostAfterBuild_SourceOwnsHostCloseOnly locks the helper to a
// single Host.Close seam without Manager/Process field extraction.
func TestCloseServeHostAfterBuild_SourceOwnsHostCloseOnly(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("serve_rollback.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "func closeServeHostAfterBuild") {
		t.Fatal("missing closeServeHostAfterBuild")
	}
	if strings.Contains(text, "serveHostRollback") {
		t.Fatal("serveHostRollback must be removed")
	}
	// Extract only the closeServeHostAfterBuild function body for field checks.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "serve_rollback.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "closeServeHostAfterBuild" || fd.Body == nil {
			return true
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			switch sel.Sel.Name {
			case "Manager", "Process", "ShutdownTracing", "GenerationManager", "ProcessServices":
				t.Fatalf("closeServeHostAfterBuild must not extract Host field %s", sel.Sel.Name)
			}
			return true
		})
		return false
	})
}

// TestServe_DoesNotExtractHostShutdownFields proves production command wiring
// never extracts Manager, Process, Coordinator, or ShutdownTracing from Host.
func TestServe_DoesNotExtractHostShutdownFields(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"command.go", "serve_rollback.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			switch sel.Sel.Name {
			case "Manager", "Process", "Coordinator", "ShutdownTracing":
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "host" {
					t.Fatalf("%s must not extract host.%s", file, sel.Sel.Name)
				}
			}
			return true
		})
	}
}

// TestServe_TracingShutdownFileIsDeleted proves the duplicated CLI tracing
// shutdown orchestration is gone for good.
func TestServe_TracingShutdownFileIsDeleted(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("tracing_shutdown.go"); err == nil {
		t.Fatal("cmd/aipstd/tracing_shutdown.go must be deleted; Host.Close owns tracing-last")
	}
}
