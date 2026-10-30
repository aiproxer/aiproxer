package contract_test

import (
	"bufio"
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPackageImportClosureIsPublicAndNeutral prevents the supported semantic
// scenario corpus from acquiring internal, concrete-plugin, or provider-SDK
// dependencies. Connector authors compile against this package outside the root
// module, so its dependency boundary is an explicit contract.
func TestPackageImportClosureIsPublicAndNeutral(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("go", "list", "-deps", "-test=false", "-f", "{{.ImportPath}}", "github.com/aiproxer/aiproxer/pkg/aipsdk/contract")
	cmd.Dir = contractRepoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		imp := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(imp, "github.com/aiproxer/aiproxer/internal/") {
			t.Fatalf("pkg/aipsdk/contract must not import internal packages: %s", imp)
		}
		if strings.Contains(imp, "/internal/plugins/") ||
			strings.Contains(imp, "/connectors/") ||
			strings.Contains(imp, "/connector-support/") ||
			strings.Contains(imp, "github.com/openai/") ||
			strings.Contains(imp, "github.com/anthropics/") ||
			strings.Contains(imp, "github.com/aws/") ||
			strings.Contains(imp, "google.golang.org/genai") {
			t.Fatalf("pkg/aipsdk/contract must not import concrete provider/plugin packages: %s", imp)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan go list output: %v", err)
	}
}

func contractRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
