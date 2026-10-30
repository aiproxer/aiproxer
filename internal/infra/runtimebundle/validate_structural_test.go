package runtimebundle_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestValidateStructural_compatibleRemoteInventoryNoNetwork(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "must not be called", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)

	path := writeStructuralCompatibleConfig(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "https://example.test/v1", srv.URL+"/v1", 1)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatalf("ValidateStructural: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("provider requests during structural validation = %d", hits.Load())
	}
}

func TestValidateStructural_rejectsInvalidCompatibleConfig(t *testing.T) {
	t.Parallel()
	path := writeStructuralCompatibleConfig(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "backend_prefix: compat-structural", "backend_prefix: anthropic", 1)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err == nil {
		t.Fatal("expected reserved built-in prefix rejection")
	}
}

func TestValidateStructural_validateDistributionStillCompiles(t *testing.T) {
	t.Parallel()
	path := writeStructuralCompatibleConfig(t)
	err := runtimebundle.ValidateDistribution(context.Background(), runtimebundle.ValidateDistributionInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("ValidateDistribution unchanged for runtime path: %v", err)
	}
}

func writeStructuralCompatibleConfig(t *testing.T) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows := `    - id: compat-structural
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-structural
        base_url: https://example.test/v1
        api_key_env_var_root: COMPAT_STRUCTURAL_KEY
        tokenizer: o200k_base
        max_concurrent_requests: 2
        models:
          source: inline
          items:
            - canonical_id: compat-structural/model-a
              native_id: model-a
`
	text := strings.Replace(string(base), "  features:\n", rows+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
