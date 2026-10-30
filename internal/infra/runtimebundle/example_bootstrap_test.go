package runtimebundle_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestValidateDistribution_omittedContinuityBlockBoots proves a configuration
// without a continuity block passes the whole service bootstrap: the process
// services build the continuity store the configuration declares, and the
// generation compiles against it.
func TestValidateDistribution_omittedContinuityBlockBoots(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(base), "continuity:\n  in_memory: true\n  store: memory\n", "", 1)
	if text == string(base) {
		t.Fatal("config/config.yaml must carry the documented continuity block")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	err = runtimebundle.ValidateDistribution(ctx, runtimebundle.ValidateDistributionInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("ValidateDistribution: %v", err)
	}
}

// TestConfigExamples_checkConfigAcceptsContinuityBootstrapAccepts proves the two
// operator entry points agree on every shipped example: a configuration
// check-config accepts is also a configuration the serving runtime can
// bootstrap.
//
// The continuity backing is the process-owned service both paths resolve, and it
// is where the two used to disagree: the configuration validator resolved an
// omitted continuity block to the process-local backing while the store
// constructor refused the same configuration. The test therefore runs the
// check-config validation and then the bootstrap's continuity construction over
// the same effective configuration.
func TestConfigExamples_checkConfigAcceptsContinuityBootstrapAccepts(t *testing.T) {
	t.Parallel()
	root := repoRootFromRuntimebundleTest(t)
	dir := filepath.Join(root, "config", "examples")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var yamls []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".yaml") || strings.HasSuffix(strings.ToLower(e.Name()), ".yml") {
			yamls = append(yamls, filepath.Join(dir, e.Name()))
		}
	}
	if len(yamls) == 0 {
		t.Fatal("no yaml files in config/examples")
	}
	mandatory := aipsdk.StandardDistributionRequirements()
	for _, path := range yamls {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			cfgPath := runtimebundle.MaterializeExampleConfigForTest(t, path)
			if _, err := runtimebundle.ValidateStructural(ctx, runtimebundle.ValidateStructuralInput{
				ConfigPath: cfgPath,
				Mandatory:  mandatory,
			}); err != nil {
				t.Skipf("check-config refuses this example: %v", err)
			}
			effective, _, ownerSlot, _, err := runtimebundle.LoadBootstrapEffectiveWithSource(
				ctx, cfgPath, config.StreamRecoveryOverrides{}, nil,
			)
			if ownerSlot != nil {
				t.Cleanup(func() { _ = ownerSlot.Close(ctx) })
			}
			if err != nil {
				t.Fatalf("load effective config: %v", err)
			}
			if effective == nil || effective.Config == nil {
				t.Fatal("nil effective config")
			}
			store, err := runtimebundle.OpenContinuityStore(ctx, effective.Config)
			if err != nil {
				t.Fatalf("bootstrap continuity store: %v", err)
			}
			if store == nil {
				t.Fatal("nil continuity store")
			}
		})
	}
}
