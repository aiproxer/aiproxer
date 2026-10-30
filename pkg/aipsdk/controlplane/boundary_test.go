package controlplane_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPackageImportsStayMinimalAndSafe proves the public control-plane SDK contract does not
// depend on internal/core, SQL/Bun, HTTP, frontend wire, backend plugins, or provider SDK
// packages (requirements 1.7, 9.5; design "Allowed Dependencies"). Only stdlib and
// pkg/aipsdk/scope (safe attribution) are permitted.
func TestPackageImportsStayMinimalAndSafe(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"/internal/core",
		"/internal/infra",
		"/internal/plugins",
		"/internal/stdhttp",
		"/internal/pluginreg",
		"/internal/refbackend",
		"/internal/refclient",
		"/internal/testkit",
		"database/sql",
		"uptrace/bun",
		"modernc.org/sqlite",
		"net/http",
		"openai/openai-go",
		"anthropics/anthropic-sdk-go",
		"google.golang.org/genai",
		"aws/aws-sdk-go-v2",
	}
	for _, imp := range listDirectImports(t, "./pkg/aipsdk/controlplane") {
		low := strings.ToLower(imp)
		for _, bad := range forbidden {
			if strings.Contains(low, bad) {
				t.Fatalf("pkg/aipsdk/controlplane imports forbidden dependency %q: %s", bad, imp)
			}
		}
	}
}

func listDirectImports(t *testing.T, pattern string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-test=false", "-json", pattern)
	cmd.Dir = repoRootDir(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %s: %v", pattern, err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	var imports []string
	for dec.More() {
		var pkg struct {
			Imports []string `json:"Imports"`
		}
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		imports = append(imports, pkg.Imports...)
	}
	if len(imports) == 0 {
		t.Fatalf("no imports resolved for %s", pattern)
	}
	return imports
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for range 10 {
		if isRepoRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate repo root from %s", file)
	return ""
}

func isRepoRoot(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	return err == nil && strings.HasPrefix(string(b), "module github.com/aiproxer/aiproxer")
}
