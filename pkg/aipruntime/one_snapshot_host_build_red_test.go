package aipruntime_test

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

	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipruntime"
)

// TestHostBuild_PublicBuildIsOneOperation proves public Build obtains a complete
// Host from one BuildHost call (req 4.1, 4.5, 4.8).
func TestHostBuild_PublicBuildIsOneOperation(t *testing.T) {
	t.Parallel()
	assertPublicBuildIsOneHostOperation(t)
}

// TestOneSnapshot_PublicBuildStillTwoStepOwnership renamed contract: public Build
// is a thin one-shot Host facade (req 4.1, 4.5).
func TestOneSnapshot_PublicBuildStillTwoStepOwnership(t *testing.T) {
	t.Parallel()
	assertPublicBuildIsOneHostOperation(t)
}

// TestBuild_MultiUserConfigSucceedsWithoutCLIFlag proves the public facade does
// not enforce the serve-only --multi-user CLI gate (req 4.3).
func TestBuild_MultiUserConfigSucceedsWithoutCLIFlag(t *testing.T) {
	t.Parallel()
	path := writePublicMultiUserConfig(t)
	rt, err := aipruntime.Build(context.Background(), aipruntime.Options{
		ConfigPath: path,
		LogWriter:  io.Discard,
	})
	if err != nil {
		t.Fatalf("public Build with multi_user config must not require CLI flag: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	if !rt.Ready() {
		t.Fatal("expected ready Runtime")
	}
	assertBuildSourceSkipsCLIMultiUserGate(t)
}

func assertPublicBuildIsOneHostOperation(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath: repoConfigPath(t),
		LogWriter:  io.Discard,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	if !rt.Ready() {
		t.Fatal("public Build must return a ready Runtime from one Host build")
	}
	assertBuildSourceUsesBuildHostOnce(t)
}

func assertBuildSourceUsesBuildHostOnce(t *testing.T) {
	t.Helper()
	src, err := os.ReadFile("build.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "build.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var buildHost, buildCommon, bootstrap, attach int
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "Build" || fd.Body == nil {
			return true
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "buildCommon" {
					buildCommon++
				}
				return true
			}
			switch sel.Sel.Name {
			case "BuildHost":
				buildHost++
			case "BuildBootstrap":
				bootstrap++
			case "AttachReloadHost":
				attach++
			}
			return true
		})
		return false
	})
	// Task 15.2 shares one BuildHost call site behind buildCommon (see
	// TestFacadeConvergesOnOneBuildHost): Build must obtain its Host from
	// exactly one Host operation, directly or via that shared assembly.
	if buildHost+buildCommon != 1 {
		t.Fatalf("aipruntime.Build Host operations=%d (BuildHost=%d shared=%d) want 1", buildHost+buildCommon, buildHost, buildCommon)
	}
	if bootstrap != 0 || attach != 0 {
		t.Fatalf("aipruntime.Build must not call BuildBootstrap/AttachReloadHost; bootstrap=%d attach=%d", bootstrap, attach)
	}
}

func assertBuildSourceSkipsCLIMultiUserGate(t *testing.T) {
	t.Helper()
	src, err := os.ReadFile("build.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "EnforceMultiUserCLIGate: true") {
		t.Fatal("public Build must not enable the serve-only multi-user CLI gate")
	}
	if strings.Contains(string(src), "MultiUser:") {
		t.Fatal("public Build must not synthesize a CLI multi-user flag")
	}
}

func writePublicMultiUserConfig(t *testing.T) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join(filepath.Dir(repoConfigPath(t)), "examples", "dogfood-local-stub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(base)
	text = strings.Replace(text, `address: "127.0.0.1:18080"`, `address: "127.0.0.1:18501"`, 1)
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
	text = strings.Replace(text, "\nrouting:\n", "\n"+insert+"routing:\n", 1)
	if !strings.Contains(text, "auth_mode:") {
		text = strings.Replace(text, "server:\n", "server:\n  auth_mode: external\n", 1)
	}
	path := filepath.Join(t.TempDir(), "public-multi-user.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return bpkit.MaterializeExampleConfig(t, path)
}
