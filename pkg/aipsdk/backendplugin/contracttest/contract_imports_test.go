package contracttest_test

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// TestContractTestPackage_NoInternalOrConcreteBackendImports proves that third-party
// connector-facing contract types do not import internal packages or concrete backend implementations.
func TestContractTestPackage_NoInternalOrConcreteBackendImports(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("go", "list", "-f", "{{.Imports}}", "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/contracttest")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "github.com/aiproxer/aiproxer/internal/") {
			t.Fatalf("pkg/aipsdk/backendplugin/contracttest must not import internal packages: %s", line)
		}
		if strings.Contains(line, "/plugins/backends/") {
			t.Fatalf("pkg/aipsdk/backendplugin/contracttest must not import concrete backends: %s", line)
		}
	}
}
