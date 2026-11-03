package runtimebundle_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestOpenResponsesCompatibleStartup_checkConfigAcceptedRowCompiles proves a
// valid custom-openresponses-compatible row that declares capabilities passes
// the startup compile that check-config already accepts. The startup credential
// and admission pre-scans must decode the row with its own kind schema.
func TestOpenResponsesCompatibleStartup_checkConfigAcceptedRowCompiles(t *testing.T) {
	t.Parallel()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows := `    - id: ors-startup
      kind: custom-openresponses-compatible
      enabled: true
      config:
        backend_prefix: ors-startup
        base_url: https://example.test/v1
        api_key_env_var_root: ORS_STARTUP_KEY
        capabilities: [structured_outputs, ordered_items, streaming]
        models:
          source: inline
          items:
            - canonical_id: ors-startup/model-a
              native_id: model-a
`
	text := strings.Replace(string(base), "  features:\n", rows+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	}); err != nil {
		t.Fatalf("ValidateStructural (check-config): %v", err)
	}
	if err := runtimebundle.ValidateDistribution(context.Background(), runtimebundle.ValidateDistributionInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	}); err != nil {
		t.Fatalf("ValidateDistribution (startup compile): %v", err)
	}
}
