package runtimebundle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func dogfoodInspectPath(t *testing.T) string {
	t.Helper()
	return MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
}

func countingLoader(inner bootstrapEffectiveLoader, n *atomic.Int64) bootstrapEffectiveLoader {
	return func(ctx context.Context, path string, cliOverrides config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, config.StreamRecoveryOverrides, error) {
		n.Add(1)
		return inner(ctx, path, cliOverrides, capabilities)
	}
}

func TestInspectRoutes_OneStrictEffectiveLoad(t *testing.T) {
	t.Parallel()
	var loads atomic.Int64
	snap, err := inspectRoutes(t.Context(), InspectInput{
		ConfigPath: dogfoodInspectPath(t),
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	}, countingLoader(LoadBootstrapEffectiveWithSource, &loads))
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loads=%d want 1", loads.Load())
	}
	if snap.EffectiveDefaultRoute == "" {
		t.Fatal("expected effective default route")
	}
}

func TestInspectInventory_OneStrictEffectiveLoad(t *testing.T) {
	t.Parallel()
	var loads atomic.Int64
	snap, err := inspectInventory(t.Context(), InspectInput{
		ConfigPath: dogfoodInspectPath(t),
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	}, countingLoader(LoadBootstrapEffectiveWithSource, &loads))
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loads=%d want 1", loads.Load())
	}
	if len(snap.Frontends) == 0 {
		t.Fatal("expected frontend rows")
	}
}

func TestInspect_ConcurrentRepeatedNoSharedMutableLeak(t *testing.T) {
	t.Parallel()
	path := dogfoodInspectPath(t)
	in := InspectInput{ConfigPath: path, Mandatory: aipsdk.StandardDistributionRequirements()}
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers*2)
	for range workers {
		wg.Add(2)
		go func() {
			defer wg.Done()
			var loads atomic.Int64
			_, err := inspectRoutes(context.Background(), in, countingLoader(LoadBootstrapEffectiveWithSource, &loads))
			if err != nil {
				errs <- err
				return
			}
			if loads.Load() != 1 {
				errs <- fmt.Errorf("routes loads=%d want 1", loads.Load())
			}
		}()
		go func() {
			defer wg.Done()
			var loads atomic.Int64
			_, err := inspectInventory(context.Background(), in, countingLoader(LoadBootstrapEffectiveWithSource, &loads))
			if err != nil {
				errs <- err
				return
			}
			if loads.Load() != 1 {
				errs <- fmt.Errorf("inventory loads=%d want 1", loads.Load())
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestInspect_NilContextAndEmptyPath(t *testing.T) {
	t.Parallel()
	_, err := InspectRoutes(nil, InspectInput{ConfigPath: dogfoodInspectPath(t)}) //nolint:staticcheck // The nil-context case is the behavior under test.
	if err == nil {
		t.Fatal("expected nil context error")
	}
	_, err = InspectInventory(t.Context(), InspectInput{})
	if err == nil {
		t.Fatal("expected empty path error")
	}
}

func TestPrepareInspect_DiscoversLocalStubAndCleansHostStaging(t *testing.T) {
	t.Parallel()
	prep, err := PrepareInspect(t.Context(), InspectInput{
		ConfigPath: dogfoodInspectPath(t),
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if prep.Config == nil || prep.Registry == nil {
		t.Fatal("expected config and registry")
	}
	if !prep.Registry.HasBackend("local-stub") {
		t.Fatal("expected discovered local-stub factory on registry")
	}
	if prep.PluginHost() == nil {
		t.Fatal("expected process host for discovered exports")
	}
	staging := prep.stagingDir
	if staging == "" {
		t.Fatal("expected staging directory")
	}
	if err := prep.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if prep.PluginHost() != nil || prep.stagingDir != "" {
		t.Fatal("Close must clear host and staging ownership")
	}
	if err := prep.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging after Close: err=%v", err)
	}
}
