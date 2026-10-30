package observers_test

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

// TestObserversImportsStayClean proves the control-plane source adapters do not
// import provider SDKs, concrete plugins, SQL/Bun, net/http, runtimebundle,
// stdhttp, or testkit (design "Allowed Dependencies"; requirements 9.5, 10.5,
// 10.7; task 4.5). They may import pkg/aipsdk/* (safe SDK contracts),
// internal/core/* (core ports/normalizer/recorder), internal/infra/controlplane
// (sibling store helpers), and stdlib only.
func TestObserversImportsStayClean(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"/internal/plugins",
		"/internal/stdhttp",
		"/internal/pluginreg",
		"/internal/infra/runtimebundle",
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
	for _, imp := range observersListImports(t, "./internal/infra/controlplane/observers") {
		low := strings.ToLower(imp)
		for _, bad := range forbidden {
			if strings.Contains(low, bad) {
				t.Fatalf("internal/infra/controlplane/observers imports forbidden dependency %q: %s", bad, imp)
			}
		}
	}
}

func observersListImports(t *testing.T, pattern string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-test=false", "-json", pattern)
	cmd.Dir = observersRepoRoot(t)
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
	return imports
}

func observersRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("could not locate caller")
	}
	dir := filepath.Dir(file)
	for range 12 {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil &&
			strings.HasPrefix(string(b), "module github.com/aiproxer/aiproxer") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate repo root")
	return ""
}
