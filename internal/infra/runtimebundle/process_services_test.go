package runtimebundle_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/internal/infra/tracing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func processServicesTestConfig() *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: true},
		},
		Server: config.ServerConfig{
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 1024,
		},
	}
}

func TestProcessServices_TwoCandidatesShareIdentities(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	log := testkit.DiscardLogger()
	reg := pluginreg.NewRegistry()
	opts := &runtimebundle.BuildOptions{PluginRegistry: reg}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  log,
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
			Active:   false,
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	bus1 := hooks.New(hooks.Config{})
	c1, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     bus1,
	})
	if err != nil {
		t.Fatalf("CompileCandidate #1: %v", err)
	}
	bus2 := hooks.New(hooks.Config{})
	c2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     bus2,
	})
	if err != nil {
		t.Fatalf("CompileCandidate #2: %v", err)
	}

	if c1 == nil || c2 == nil {
		t.Fatal("expected both candidates")
	}
	if c1.Executor() == nil || c2.Executor() == nil {
		t.Fatal("expected executors on both candidates")
	}
	if c1.Executor() == c2.Executor() {
		t.Fatal("candidates must own distinct executors")
	}

	// Process service identity reuse (req 6.2–6.5, 6.9).
	if ps.Metrics == nil || ps.Metrics.Registry == nil {
		t.Fatal("expected process metrics registry")
	}
	if c1.Metrics() != ps.Metrics || c2.Metrics() != ps.Metrics {
		t.Fatalf("candidates must reuse process Metrics identity: ps=%p c1=%p c2=%p", ps.Metrics, c1.Metrics(), c2.Metrics())
	}
	if c1.Metrics().Registry != ps.Metrics.Registry {
		t.Fatal("candidate must not open a duplicate Prometheus registry")
	}
	if c1.Store() != ps.Continuity || c2.Store() != ps.Continuity {
		t.Fatalf("candidates must reuse process Continuity store: ps=%p c1=%p c2=%p", ps.Continuity, c1.Store(), c2.Store())
	}
	if runtimebundle.CandidateSecureSessionStore(c1) != ps.SecureSessions || runtimebundle.CandidateSecureSessionStore(c2) != ps.SecureSessions {
		t.Fatal("candidates must reuse process SecureSessions store")
	}
	if c1.DecodeAdmission() != ps.DecodeAdmission || c2.DecodeAdmission() != ps.DecodeAdmission {
		t.Fatal("candidates must reuse process DecodeAdmission")
	}
	if c1.PluginRegistry() != ps.FactoryCatalog || c2.PluginRegistry() != reg {
		t.Fatal("candidates must reuse process FactoryCatalog / PluginRegistry")
	}
	if runtimebundle.CandidateDatabasePools(c1) != ps.DatabasePools || runtimebundle.CandidateDatabasePools(c2) != ps.DatabasePools {
		t.Fatal("candidates must reuse process DatabasePools")
	}
	if runtimebundle.CandidateTerminalWorkProcessor(c1) != ps.TerminalWorkProcessor || runtimebundle.CandidateTerminalWorkProcessor(c2) != ps.TerminalWorkProcessor {
		t.Fatal("candidates must reuse process TerminalWorkProcessor")
	}
	if ps.Tracing.Shutdown == nil {
		t.Fatal("expected process Tracing.Shutdown")
	}
}

func TestProcessServices_CandidateCloseDoesNotCloseSharedServices(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	log := testkit.DiscardLogger()
	var tracingClosed atomic.Bool
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  log,
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error {
				tracingClosed.Store(true)
				return nil
			},
			Active: false,
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}

	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}

	if err := cand.Close(); err != nil {
		t.Fatalf("Candidate.Close: %v", err)
	}
	if tracingClosed.Load() {
		t.Fatal("candidate close must not invoke process tracing shutdown")
	}
	if ps.Metrics == nil || ps.Metrics.Registry == nil {
		t.Fatal("process metrics must survive candidate close")
	}
	if ps.Continuity == nil {
		t.Fatal("process continuity store must survive candidate close")
	}
	if ps.Closed() {
		t.Fatal("ProcessServices must not be closed by candidate Close")
	}

	// Process services remain usable for a second candidate after first close.
	cand2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate after candidate close: %v", err)
	}
	if err := cand2.Close(); err != nil {
		t.Fatalf("cand2.Close: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("ProcessServices.Close: %v", err)
	}
	if !ps.Closed() {
		t.Fatal("expected ProcessServices closed")
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
}

func TestProcessServices_CandidateExposesCoreCapabilitiesAndClosesCleanly(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if b.Metrics() == nil {
		t.Fatal("candidate must still expose Metrics")
	}
	if b.DecodeAdmission() == nil {
		t.Fatal("candidate must still expose DecodeAdmission")
	}
	if b.Store() == nil {
		t.Fatal("candidate must still expose Store")
	}
	// Pure in-memory builds may have an empty ledger (historical semantics).
	// When ledger entries exist, reverse-order disposal must remain safe.
	if err := b.Close(); err != nil {
		t.Fatalf("candidate close: %v", err)
	}
}

func TestProcessServices_OwnershipHoistedSharedMutableState(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	if ps.ALegLifecycle == nil || ps.AffinityStore == nil || ps.CandidateHealth == nil || ps.ExtensionState == nil {
		t.Fatal("expected hoisted A-leg, affinity, health, and extension state on ProcessServices")
	}
}

func TestProcessServices_DuplicateCompileDoesNotDuplicateTerminalWork(t *testing.T) {
	t.Parallel()

	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "tw-process-dup"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := processServicesTestConfig()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry: pluginreg.NewRegistry(),
			Production: runtimebundle.ProductionOptions{
				TerminalWorkStore: store,
				TerminalWorkProviders: []terminalworkapp.EffectProvider{
					stubEffectProvider{id: "prov-process"},
				},
				TerminalWorkOwnerID: "process-worker",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if ps.TerminalWorkProcessor == nil {
		t.Fatal("expected process terminal-work processor")
	}

	c1, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtimebundle.CandidateTerminalWorkProcessor(c1) != ps.TerminalWorkProcessor || runtimebundle.CandidateTerminalWorkProcessor(c2) != ps.TerminalWorkProcessor {
		t.Fatal("duplicate terminal-work processors across candidates")
	}
	_ = c1.Close()
	_ = c2.Close()
}

func TestProcessServices_AcceptsTracingResultShape(t *testing.T) {
	t.Parallel()

	// Ensure tracing.Result shape still wires through ProcessTracing without
	// requiring callers to change Host fields.
	res := tracing.Result{Shutdown: func(context.Context) error { return nil }, Active: false}
	pt := runtimebundle.ProcessTracing{Shutdown: res.Shutdown, Active: res.Active}
	if pt.Shutdown == nil {
		t.Fatal("ProcessTracing must accept tracing.Result fields")
	}
}

func TestProcessServices_OwnsBackgroundAuxiliaryScheduler(t *testing.T) {
	t.Parallel()
	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
		return backgroundProcessRunner{}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:           processServicesTestConfig(),
		Log:           testkit.DiscardLogger(),
		Opts:          &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
		BackgroundAux: scheduler,
	})
	if err != nil {
		_ = scheduler.Close()
		t.Fatalf("NewProcessServices: %v", err)
	}
	if ps.BackgroundAux != scheduler {
		t.Fatal("BackgroundAux scheduler was not transferred to ProcessServices")
	}
	id, err := ps.BackgroundAux.SubmitCollect(context.Background(), auxiliary.Request{Call: &aipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "process-owned"})
	if err != nil {
		_ = ps.Close()
		t.Fatal(err)
	}
	if _, err := ps.BackgroundAux.Await(context.Background(), id); err != nil {
		_ = ps.Close()
		t.Fatal(err)
	}
	if err := ps.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.BackgroundAux.SubmitCollect(context.Background(), auxiliary.Request{Call: &aipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "after-close"}); !errors.Is(err, auxreq.ErrSchedulerClosed) {
		t.Fatalf("SubmitCollect after process close=%v want ErrSchedulerClosed", err)
	}
}

type backgroundProcessRunner struct{}

func (backgroundProcessRunner) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
}
