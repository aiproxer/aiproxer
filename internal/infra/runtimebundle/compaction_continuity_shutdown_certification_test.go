package runtimebundle_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/state"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func TestCompactionContinuityShutdownCertification_ProcessOwnsAndClosesResources(t *testing.T) {
	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), nil, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})
	if err != nil {
		t.Fatalf("NewBackgroundScheduler: %v", err)
	}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:           processServicesCoordinatorConfig(),
		Log:           testkit.DiscardLogger(),
		Opts:          &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
		BackgroundAux: scheduler,
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if ps.BackgroundAux != scheduler {
		t.Fatal("ProcessServices did not adopt the supplied process scheduler")
	}
	// Branch-coordinator presence and behavior now live inside package
	// featurehost (TestProcess_BranchCoordinatorBehaviorAndGenerationStability);
	// this certification covers process shutdown ordering only.

	if err := ps.Close(); err != nil {
		t.Fatalf("ProcessServices.Close: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("ProcessServices.Close is not idempotent: %v", err)
	}
	_, err = scheduler.SubmitCollect(context.Background(), auxiliary.Request{Call: &aipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "after-process-close"})
	if !errors.Is(err, auxreq.ErrSchedulerClosed) {
		t.Fatalf("submission after ProcessServices.Close=%v want ErrSchedulerClosed", err)
	}
}

func TestCompactionContinuityShutdownCertification_BranchAndPreviewBounds(t *testing.T) {
	clock := &certificationClock{now: time.Unix(100, 0)}
	coordinator, err := state.NewBranchCoordinator(context.Background(), state.Config{
		MaxEntries:        2,
		MaxPreviewIntents: 1,
		TTL:               time.Minute,
		Now:               clock.Now,
	})
	if err != nil {
		t.Fatalf("NewBranchCoordinator: %v", err)
	}
	first := certificationBranchKey("first")
	second := certificationBranchKey("second")
	third := certificationBranchKey("third")
	if _, err := coordinator.Capture(context.Background(), first); err != nil {
		t.Fatalf("Capture first: %v", err)
	}
	if _, err := coordinator.Capture(context.Background(), second); err != nil {
		t.Fatalf("Capture second: %v", err)
	}
	if _, err := coordinator.Capture(context.Background(), third); !errors.Is(err, state.ErrBranchLimit) {
		t.Fatalf("third branch error=%v want ErrBranchLimit", err)
	}
	if _, err := coordinator.RecordPreviewIntent(context.Background(), first, state.PreviewIntent{Key: "preview-first", TargetSourceRevision: 1}); err != nil {
		t.Fatalf("RecordPreviewIntent first: %v", err)
	}
	if _, err := coordinator.RecordPreviewIntent(context.Background(), second, state.PreviewIntent{Key: "preview-second", TargetSourceRevision: 1}); !errors.Is(err, state.ErrPreviewIntentLimit) {
		t.Fatalf("second preview intent error=%v want ErrPreviewIntentLimit", err)
	}

	// TTL cleanup is lazy and bounded; no cleanup goroutine is needed to make
	// room for a subsequent branch.
	clock.Advance(2 * time.Minute)
	if _, found, err := coordinator.Snapshot(context.Background(), first); err != nil || found {
		t.Fatalf("expired first branch snapshot found=%v err=%v", found, err)
	}
	if _, err := coordinator.Capture(context.Background(), third); err != nil {
		t.Fatalf("Capture after bounded expiry: %v", err)
	}
}

func processServicesCoordinatorConfig() *config.Config {
	return &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 1},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Server:     config.ServerConfig{MaxConcurrentDecodes: 1, MaxInflightDecodeBytes: 1024},
	}
}

type certificationClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *certificationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *certificationClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func certificationBranchKey(id string) state.BranchKey {
	key, err := state.NewBranchKey("session-certification", "a-"+id, "principal-certification")
	if err != nil {
		panic(err)
	}
	return key
}
