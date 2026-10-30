package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestHostBuild_ServeUsesSingleHostBuildCall proves serve-shaped startup obtains
// a complete Host from one BuildHost call (req 4.1, 4.5).
func TestHostBuild_ServeUsesSingleHostBuildCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgPath := bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))

	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	if !host.Ready() || host.ExecutorView() == nil || host.HTTPHandler() == nil {
		t.Fatal("BuildHost must return a complete Host")
	}
	if host.ActiveGenerationID() != 1 {
		t.Fatalf("BuildHost must publish generation 1")
	}
	assertServeCommandCallsBuildHostOnce(t)
}

// TestOneSnapshot_ServePathMustNotDoubleLoadEffective proves production serve
// no longer pre-loads the gate separately from host construction (req 4.2-4.3).
func TestOneSnapshot_ServePathMustNotDoubleLoadEffective(t *testing.T) {
	t.Parallel()
	assertServeCommandDoesNotCallValidateGate(t)

	ctx := context.Background()
	path := writeServeMarkerConfig(t, "127.0.0.1:18301", accessmode.ModeMultiUser)
	flagTrue := true
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:              path,
		Mandatory:               aipsdk.StandardDistributionRequirements(),
		LogWriter:               io.Discard,
		HandlerComposer:         stdhttp.ComposeStandardHTTP,
		EnforceMultiUserCLIGate: true,
		MultiUser:               &flagTrue,
	})
	if err != nil {
		t.Fatalf("BuildHost with multi-user gate: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	wantFP := host.Effective().Identity.PublicFingerprint
	if wantFP == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	if host.ActivePublicFingerprint() != wantFP {
		t.Fatal("generation fingerprint must match accepted Effective")
	}
}

func assertServeCommandCallsBuildHostOnce(t *testing.T) {
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
	var buildHostCalls, bootstrapCalls, attachCalls int
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
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			switch sel.Sel.Name {
			case "BuildHost":
				buildHostCalls++
			case "BuildBootstrap":
				bootstrapCalls++
			case "AttachReloadHost":
				attachCalls++
			}
			return true
		})
		return false
	})
	if buildHostCalls != 1 {
		t.Fatalf("runServeCommand BuildHost calls=%d want 1", buildHostCalls)
	}
	if bootstrapCalls != 0 || attachCalls != 0 {
		t.Fatalf("runServeCommand must not call BuildBootstrap/AttachReloadHost; bootstrap=%d attach=%d", bootstrapCalls, attachCalls)
	}
}

func assertServeCommandDoesNotCallValidateGate(t *testing.T) {
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
				if fun.Name == "validateServeMultiUserGate" {
					t.Fatal("runServeCommand must not call validateServeMultiUserGate; BuildHost owns the gate")
				}
			}
			return true
		})
		return false
	})
}

func writeServeMarkerConfig(t *testing.T, address string, mode accessmode.Mode) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "serve-one-snapshot.yaml")
	if err := os.WriteFile(path, applyServeMarker(t, string(base), address, mode), 0o600); err != nil {
		t.Fatal(err)
	}
	return bpkit.MaterializeExampleConfig(t, path)
}

func applyServeMarker(t *testing.T, text, address string, mode accessmode.Mode) []byte {
	t.Helper()
	text = strings.Replace(text, `address: "127.0.0.1:18080"`, `address: "`+address+`"`, 1)
	if !strings.Contains(text, address) {
		t.Fatal("failed to rewrite server address marker")
	}
	if mode == accessmode.ModeMultiUser {
		insert := "" +
			"access:\n" +
			"  mode: multi_user\n" +
			"auth:\n" +
			"  handler: local_api_key\n" +
			"  required_level: api_key\n" +
			"  local_api_keys:\n" +
			"    - key_id: k1\n" +
			"      principal_id: p1\n" +
			"      key: \"test-key-at-least-16-chars\"\n"
		if strings.Contains(text, "\naccess:\n") {
			t.Fatal("dogfood fixture unexpectedly declares access block")
		}
		text = strings.Replace(text, "\nrouting:\n", "\n"+insert+"routing:\n", 1)
		if !strings.Contains(text, "auth_mode:") {
			text = strings.Replace(text, "server:\n", "server:\n  auth_mode: external\n", 1)
		}
	}
	return []byte(text)
}
