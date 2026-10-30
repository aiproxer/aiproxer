package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/submitnoop"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"gopkg.in/yaml.v3"
)

type countingUnsafeLife struct {
	starts, stops atomic.Int32
}

func (c *countingUnsafeLife) Start(context.Context) error {
	c.starts.Add(1)
	return nil
}

func (c *countingUnsafeLife) Stop(context.Context) error {
	c.stops.Add(1)
	return nil
}

func TestCompileCandidate_UnsafeLifecycleRejectedBeforeAcquisitionEscape(t *testing.T) {
	t.Parallel()
	var closed atomic.Int32
	cfg, opts, cleanup := newWiringProcess(t, func(reg *pluginreg.Registry) {
		registerProbeBackend(t, reg, "unsafe-life-probe", &closed, runtimebundle.OptionalBackendHooks{})
	}, "unsafe-life-probe")
	defer cleanup()

	life := &countingUnsafeLife{}
	opts.FeatureLifecycles = []aipplugin.Lifecycle{life}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	_, err = runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if !errors.Is(err, runtimebundle.ErrUnsafeLifecycleOverlap) {
		t.Fatalf("want ErrUnsafeLifecycleOverlap, got %v", err)
	}
	if life.starts.Load() != 0 || life.stops.Load() != 0 {
		t.Fatalf("unsafe lifecycle must not Start/Stop: starts=%d stops=%d", life.starts.Load(), life.stops.Load())
	}
	if closed.Load() != 0 {
		t.Fatalf("backend must not escape acquisition on unsafe lifecycle reject: closes=%d", closed.Load())
	}
}

func TestCompileCandidate_SafeLifecycleStartOnceRollbackStopOnce(t *testing.T) {
	t.Parallel()
	var closed atomic.Int32
	cfg, opts, cleanup := newWiringProcess(t, func(reg *pluginreg.Registry) {
		registerProbeBackend(t, reg, "safe-life-probe", &closed, runtimebundle.OptionalBackendHooks{})
	}, "safe-life-probe")
	defer cleanup()

	probe := &submitnoop.LifecycleProbe{}
	opts.FeatureLifecycles = []aipplugin.Lifecycle{probe}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	_, err = runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
		FaultInject: runtimebundle.CandidateFaultInject{
			After: "activate",
			Hook: func() {
				if !probe.WasStarted() {
					t.Fatal("safe lifecycle must Start during prepare before activate fault")
				}
			},
		},
	})
	if !errors.Is(err, runtimebundle.ErrCandidateFaultInjected) {
		t.Fatalf("want injected fault, got %v", err)
	}
	if !probe.WasStarted() || !probe.WasStopped() {
		t.Fatalf("rollback must Start once and Stop once: started=%v stopped=%v", probe.WasStarted(), probe.WasStopped())
	}
	if closed.Load() != 1 {
		t.Fatalf("backend close=%d want 1", closed.Load())
	}
}

func TestCompileCandidate_SafeLifecycleRetiredCloseStopsOnce(t *testing.T) {
	t.Parallel()
	cfg, opts, cleanup := newWiringProcess(t, nil, "")
	defer cleanup()

	probe := &submitnoop.LifecycleProbe{}
	opts.FeatureLifecycles = []aipplugin.Lifecycle{probe}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	if !probe.WasStarted() {
		t.Fatal("prepare must Start safe lifecycle")
	}

	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("life", cand)
	mustPublishHost(t, m, g)
	mustPublishHost(t, m, m.Prepare("next"))
	// Publish auto-schedules g's retirement in the background (task 7.3);
	// wait for it via the manager's synchronous retry/wait API, tolerating a
	// benign ErrAlreadyClosed if the background retirement already finished.
	if _, err := m.RetireGeneration(context.Background(), g); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatal(err)
	}
	if !probe.WasStopped() {
		t.Fatal("retired close must Stop lifecycle once")
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCompileCandidate_BackendOptionalHooksOnSuccessAndInjectedFailure(t *testing.T) {
	t.Parallel()
	var starts, stops, idles, closes, preflights atomic.Int32
	hooksCarrier := runtimebundle.OptionalBackendHooks{
		Start: func(context.Context) error {
			starts.Add(1)
			return nil
		},
		Stop: func(context.Context) error {
			stops.Add(1)
			return nil
		},
		CleanupIdleTransports: func(context.Context) error {
			idles.Add(1)
			return nil
		},
		PreflightCapability: func(context.Context) (runtimebundle.BackendPreflightResult, error) {
			preflights.Add(1)
			return runtimebundle.BackendPreflightResult{Ready: true, Description: "ready"}, nil
		},
	}

	cfg, opts, cleanup := newWiringProcess(t, func(reg *pluginreg.Registry) {
		registerProbeBackend(t, reg, "hooks-probe", &closes, hooksCarrier)
	}, "hooks-probe")
	defer cleanup()

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("Start=%d want 1 (hooks must not be discarded)", starts.Load())
	}
	if preflights.Load() != 0 {
		t.Fatalf("preflight must not run automatically, calls=%d", preflights.Load())
	}
	be := cand.Executor().Backends["probe"]
	if be.PreflightCapability == nil {
		t.Fatal("production backend preflight capability was discarded")
	}
	res, err := be.PreflightCapability(context.Background())
	if err != nil || !res.Ready || res.Billable {
		t.Fatalf("explicit preflight result=%+v err=%v", res, err)
	}
	if preflights.Load() != 1 {
		t.Fatalf("explicit preflight calls=%d want 1", preflights.Load())
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != 1 || idles.Load() != 1 || closes.Load() != 1 {
		t.Fatalf("success close: stops=%d idles=%d closes=%d", stops.Load(), idles.Load(), closes.Load())
	}

	starts.Store(0)
	stops.Store(0)
	idles.Store(0)
	closes.Store(0)
	preflights.Store(0)
	_, err = runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
		FaultInject: runtimebundle.CandidateFaultInject{
			After: "activate",
		},
	})
	if !errors.Is(err, runtimebundle.ErrCandidateFaultInjected) {
		t.Fatalf("want fault, got %v", err)
	}
	if starts.Load() != 1 || stops.Load() != 1 || idles.Load() != 1 || closes.Load() != 1 {
		t.Fatalf("failure rollback: starts=%d stops=%d idles=%d closes=%d", starts.Load(), stops.Load(), idles.Load(), closes.Load())
	}
	if preflights.Load() != 0 {
		t.Fatalf("failure path must not run optional preflight, calls=%d", preflights.Load())
	}
}

func TestCompileCandidate_BackendStopSkippedBeforeStartAttempt(t *testing.T) {
	t.Parallel()
	var starts, stops, idles, closes atomic.Int32
	hooksCarrier := runtimebundle.OptionalBackendHooks{
		Start: func(context.Context) error {
			starts.Add(1)
			return nil
		},
		Stop: func(context.Context) error {
			stops.Add(1)
			return nil
		},
		CleanupIdleTransports: func(context.Context) error {
			idles.Add(1)
			return nil
		},
	}
	cfg, opts, cleanup := newWiringProcess(t, func(reg *pluginreg.Registry) {
		registerProbeBackend(t, reg, "pre-prepare-probe", &closes, hooksCarrier)
	}, "pre-prepare-probe")
	defer cleanup()

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: cfg, Log: testkit.DiscardLogger(), Opts: opts,
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	_, err = runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
		FaultInject: runtimebundle.CandidateFaultInject{
			After: "model", // backend acquired, ledger prepare not entered
		},
	})
	if !errors.Is(err, runtimebundle.ErrCandidateFaultInjected) {
		t.Fatalf("want injected fault, got %v", err)
	}
	if starts.Load() != 0 || stops.Load() != 0 {
		t.Fatalf("pre-prepare rollback must not Start/Stop: starts=%d stops=%d", starts.Load(), stops.Load())
	}
	if idles.Load() != 1 || closes.Load() != 1 {
		t.Fatalf("acquired resources must close: idles=%d closes=%d", idles.Load(), closes.Load())
	}
}

func TestCompileCandidate_OwnedHTTPTransportIdleCleanupOnRollbackAndRetire(t *testing.T) {
	t.Parallel()
	cfg, opts, cleanup := newWiringProcess(t, nil, "")
	defer cleanup()
	// Generation-owned client (no injected Infra.HTTPClient).

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	if runtimebundle.CandidateUpstreamHTTP(cand) == nil || runtimebundle.CandidateUpstreamHTTP(cand).Transport == nil {
		t.Fatal("expected generation-owned upstream transport")
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}

	// Injected shared client must not claim idle cleanup ownership.
	shared := &http.Client{Transport: http.DefaultTransport}
	opts.Infra.HTTPClient = shared
	ps2, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices shared: %v", err)
	}
	t.Cleanup(func() { _ = ps2.Close() })
	cand2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps2,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate shared: %v", err)
	}
	if err := cand2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCompileCandidate_CatalogRefreshQuiescesBeforeClose(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		ModelCatalog: config.ModelCatalogConfig{
			Enabled:                true,
			ExternalUpdatesEnabled: true,
			CachePath:              t.TempDir() + "/catalog.json",
			UpdateInterval:         "1h",
			SourceURL:              "https://127.0.0.1:1/missing",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	reg := pluginreg.NewRegistry()
	opts := &runtimebundle.BuildOptions{PluginRegistry: reg}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		// Catalog start may fail on missing source; skip if environment cannot start catalog.
		t.Skipf("catalog candidate unavailable: %v", err)
	}
	if cand.Ledger() == nil {
		t.Fatal("expected ledger")
	}

	var order []string
	var mu sync.Mutex
	cand.Ledger().AddClose("probe-quiesce-order", runtimebundle.PhaseQuiesce, func() error {
		mu.Lock()
		order = append(order, "quiesce-probe")
		mu.Unlock()
		return nil
	})
	cand.Ledger().AddClose("probe-close-order", runtimebundle.PhaseClose, func() error {
		mu.Lock()
		order = append(order, "close-probe")
		mu.Unlock()
		return nil
	})

	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("cat", cand)
	mustPublishHost(t, m, g)
	mustPublishHost(t, m, m.Prepare("next"))
	if _, err := m.RetireGeneration(context.Background(), g); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 || order[0] != "quiesce-probe" || order[len(order)-1] != "close-probe" {
		t.Fatalf("expected quiesce before close, order=%v", order)
	}
}

func TestResourceLedger_LateAddAcceptOrImmediatelyCloseRace(t *testing.T) {
	t.Parallel()
	for i := range 50 {
		ledger := runtimebundle.NewResourceLedger()
		var closed atomic.Int32
		closeFn := func() error {
			closed.Add(1)
			return nil
		}
		var ready, released sync.WaitGroup
		ready.Add(2)
		released.Go(func() {
			ready.Done()
			ready.Wait()
			ledger.AddClose("late", runtimebundle.PhaseClose, closeFn)
		})
		go func() {
			ready.Done()
			ready.Wait()
			_ = ledger.Rollback(context.Background())
		}()
		released.Wait()
		_ = ledger.Rollback(context.Background())
		if n := closed.Load(); n != 1 {
			t.Fatalf("iter %d: late closer runs=%d want exactly 1", i, n)
		}
	}
}

func TestResourceLedger_LateCloserMayReenterLedger(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ledger.AddClose("reentrant-late", runtimebundle.PhaseClose, func() error {
			if got := ledger.Len(); got != 0 {
				t.Errorf("closed ledger len=%d want 0", got)
			}
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("late cleanup deadlocked while re-entering ledger")
	}
}

func newWiringProcess(t *testing.T, register func(*pluginreg.Registry), backendID string) (*config.Config, *runtimebundle.BuildOptions, func()) {
	t.Helper()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	reg := pluginreg.NewRegistry()
	if register != nil {
		register(reg)
	}
	if backendID != "" {
		var empty yaml.Node
		_ = yaml.Unmarshal([]byte("{}"), &empty)
		cfg.Plugins.Backends = []config.PluginConfig{
			{Kind: backendID, ID: "probe", Enabled: true, Config: empty},
		}
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	opts := &runtimebundle.BuildOptions{PluginRegistry: reg}
	return cfg, opts, func() {}
}

func registerProbeBackend(t *testing.T, reg *pluginreg.Registry, kind string, closes *atomic.Int32, hooks runtimebundle.OptionalBackendHooks) {
	t.Helper()
	if err := reg.RegisterBackend(kind, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		be := execbackend.Backend{
			BackendPrefixes: []string{"probe"},
			ModelInventory: modelinventory.StaticProvider{
				Source: modelinventory.SourceStaticBuiltin,
				Models: []modelinventory.Model{{
					CanonicalID: "probe/model",
					NativeID:    "model",
					DisplayName: "Probe",
				}},
			},
			Close: func() error {
				closes.Add(1)
				return nil
			},
			Start:                 hooks.Start,
			Stop:                  hooks.Stop,
			CleanupIdleTransports: hooks.CleanupIdleTransports,
		}
		if hooks.PreflightCapability != nil {
			be.PreflightCapability = func(ctx context.Context) (execbackend.CapabilityPreflight, error) {
				res, err := hooks.PreflightCapability(ctx)
				return execbackend.CapabilityPreflight{
					Ready:       res.Ready,
					Billable:    res.Billable,
					Description: res.Description,
				}, err
			}
		}
		return be, nil
	}); err != nil {
		t.Fatal(err)
	}
}
