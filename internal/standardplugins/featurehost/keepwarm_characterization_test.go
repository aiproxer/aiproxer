package featurehost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"github.com/prometheus/client_golang/prometheus"
)

type dummyController struct{}

func (dummyController) Renew(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	return promptcache.RenewResponse{}, nil
}

func (dummyController) Release(ctx context.Context, req promptcache.ReleaseRequest) error {
	return nil
}

// TestKeepwarmAccountingFailureIsLoggedAndReturned pins the D2 observability
// correction: a durable delivery failure returns through the existing hook
// error path unchanged (so the manager's bounded retry and final-exhaustion
// metric stay in charge) and is additionally logged with bounded, non-secret
// context — the manager-generated operation plus a stable reason; never the raw
// observer message, identity fields, token amounts or payloads (R3-1).
func TestKeepwarmAccountingFailureIsLoggedAndReturned(t *testing.T) {
	t.Parallel()
	providerErr := errors.New("durable maintenance store unavailable")
	observer := billingport.ProviderMaintenanceUsageObserverFunc(func(context.Context, billingport.ProviderMaintenanceUsage) error {
		return providerErr
	})
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	hooks := keepwarmAccountingHooks(observer, log)
	if hooks.Accounting == nil {
		t.Fatal("non-nil observer must install the accounting hook")
	}

	inputTokens := int64(23)
	err := hooks.Accounting(context.Background(), keepwarm.RenewalRecord{
		OperationID: "op-log-1",
		ALegID:      "a-leg-1",
		TargetID:    "target-1",
		BackendID:   "backend-1",
		ModelID:     "model-1",
		Accounting: &promptcache.AccountingEvidence{
			InputTokens: &inputTokens,
			Presence:    aipapi.UsagePresence{InputTokens: true},
			Source:      promptcache.AccountingSourceProviderReported,
			Authority:   promptcache.AccountingAuthorityAuthoritative,
			Plane:       promptcache.AccountingPlaneProviderBillable,
			DedupeKey:   "op-log-1",
		},
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("hook error = %v, want the observer error returned unchanged", err)
	}
	logged := buf.String()
	for _, want := range []string{"keepwarm provider maintenance accounting failed", "op-log-1", "reason=accounting_failed"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("failure log missing %q:\n%s", want, logged)
		}
	}
	for _, forbidden := range []string{"InputTokens", "Present", "DedupeKey", providerErr.Error()} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("failure log must stay bounded and carry no evidence payload (%q leaked):\n%s", forbidden, logged)
		}
	}
	// A nil logger must not panic and still returns the error.
	hooks = keepwarmAccountingHooks(observer, nil)
	if err := hooks.Accounting(context.Background(), keepwarm.RenewalRecord{
		OperationID: "op-log-1",
		Accounting:  &promptcache.AccountingEvidence{},
	}); !errors.Is(err, providerErr) {
		t.Fatalf("nil-logger hook error = %v, want the observer error", err)
	}
}

func TestPromptCacheMaintenance_AdapterOperationAndFieldMapping(t *testing.T) {
	t.Parallel()
	cfg := keepwarm.DefaultConfig()
	mgr, err := keepwarm.NewManager(cfg, keepwarm.ClockFunc(func() time.Time { return time.Now().UTC() }), keepwarm.Hooks{})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	policy, err := keepwarm.NewPolicyStore(keepwarm.DefaultMaxPolicyEntries)
	if err != nil {
		t.Fatalf("failed to create policy store: %v", err)
	}
	orch := keepwarm.NewOrchestrator(mgr, policy)

	adapter := NewPromptCacheMaintenanceAdapter(orch)
	if adapter == nil {
		t.Fatal("expected non-nil adapter")
	}

	// 1. Test BeginRealTurn
	adapter.BeginRealTurn("aleg-123")

	// 2. Test ArmCommittedTurn with exact field mapping
	ctrl := dummyController{}
	adapter.ArmCommittedTurn(runtime.PromptCacheCommittedTurn{
		ALegID:              "aleg-123",
		BLegID:              "bleg-456",
		CommittedSuccessful: true,
		ToolEvents: []aipapi.ToolEvent{
			{Kind: aipapi.ToolEventFinished, Category: aipapi.ToolCategoryOSCommand},
		},
		Observations: []promptcache.Observation{
			{Handle: promptcache.Handle("h-1")},
		},
		BackendInstanceID: "b-inst-1",
		CanonicalModelID:  "model-1",
		Controller:        ctrl,
	})

	// 3. Test EndSession
	adapter.EndSession("aleg-123")
}

func TestPromptCacheMaintenance_NilSafety(t *testing.T) {
	t.Parallel()
	adapter := NewPromptCacheMaintenanceAdapter(nil)
	if adapter != nil {
		t.Fatalf("expected nil adapter for nil orchestrator, got %v", adapter)
	}

	// Typed nil adapter must not panic on any method
	var typedNil *promptCacheMaintenanceAdapter
	typedNil.BeginRealTurn("aleg-1")
	typedNil.EndSession("aleg-1")
	typedNil.ArmCommittedTurn(runtime.PromptCacheCommittedTurn{})

	// Empty adapter with nil orchestrator must not panic
	empty := &promptCacheMaintenanceAdapter{}
	empty.BeginRealTurn("aleg-1")
	empty.EndSession("aleg-1")
	empty.ArmCommittedTurn(runtime.PromptCacheCommittedTurn{})
}

func TestKeepwarm_ProcessOwnership(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := NewProcess(context.Background(), ProcessInput{
		Logger: log,
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	if r.KeepwarmPolicy() == nil {
		t.Fatal("expected non-nil KeepwarmPolicy on featurehost.Runtime")
	}
	if r.KeepwarmRegistry() == nil {
		t.Fatal("expected non-nil KeepwarmRegistry on featurehost.Runtime")
	}
}

func TestKeepwarm_ConstructionCountingAndGenerationOutput(t *testing.T) { //nolint:paralleltest // swaps package-level constructor seams; must remain serial.
	origNewPolicy := newKeepwarmPolicyStore
	origNewRegistry := newKeepwarmManagerRegistry
	defer func() {
		newKeepwarmPolicyStore = origNewPolicy
		newKeepwarmManagerRegistry = origNewRegistry
	}()

	var policyConstructions, registryConstructions int
	newKeepwarmPolicyStore = func(maxEntries int) (*keepwarm.PolicyStore, error) {
		policyConstructions++
		return origNewPolicy(maxEntries)
	}
	newKeepwarmManagerRegistry = func() *keepwarm.ManagerRegistry {
		registryConstructions++
		return origNewRegistry()
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := NewProcess(context.Background(), ProcessInput{Logger: log})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	if policyConstructions != 1 {
		t.Fatalf("policy constructions: got %d, want 1", policyConstructions)
	}
	if registryConstructions != 1 {
		t.Fatalf("registry constructions: got %d, want 1", registryConstructions)
	}

	// Two overlapping generations must not construct another process-scoped policy or registry
	gen1, err := r.CompileGeneration(context.Background(), GenerationInput{
		KeepwarmConfig: keepwarm.DefaultConfig(),
	})
	if err != nil {
		t.Fatalf("gen1 compile: %v", err)
	}
	if gen1.CorePorts.PromptCacheMaintenance == nil {
		t.Fatal("expected non-nil PromptCacheMaintenance in gen1 CorePorts")
	}

	gen2, err := r.CompileGeneration(context.Background(), GenerationInput{
		KeepwarmConfig: keepwarm.DefaultConfig(),
	})
	if err != nil {
		t.Fatalf("gen2 compile: %v", err)
	}
	if gen2.CorePorts.PromptCacheMaintenance == nil {
		t.Fatal("expected non-nil PromptCacheMaintenance in gen2 CorePorts")
	}

	if policyConstructions != 1 {
		t.Fatalf("overlapping compile created extra policy store: %d", policyConstructions)
	}
	if registryConstructions != 1 {
		t.Fatalf("overlapping compile created extra registry: %d", registryConstructions)
	}
}

// gatheredSeriesNames returns the metric family names present in a registry.
func gatheredSeriesNames(t *testing.T, reg *prometheus.Registry) []string {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var names []string
	for _, f := range families {
		names = append(names, f.GetName())
	}
	return names
}

func hasSeries(names []string, want string) bool {
	return slices.Contains(names, want)
}

func TestCompileKeepwarmMetricsSwapAndAdminProjection(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := prometheus.NewRegistry()
	r, err := NewProcess(context.Background(), ProcessInput{Logger: log, MetricsRegistry: reg})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	gen, err := r.CompileGeneration(context.Background(), GenerationInput{
		KeepwarmConfig: keepwarm.DefaultConfig(),
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	// The swap must not run during composition: generic runtimebundle invokes
	// it once per published generation, preserving publication-time behavior.
	if gen.HostProjections.MetricsSwap == nil {
		t.Fatal("expected non-nil MetricsSwap port for enabled keep-warm")
	}
	if got := gatheredSeriesNames(t, reg); hasSeries(got, "aip_prompt_cache_keepwarm_active_epochs") {
		t.Fatalf("metrics swap ran during composition: series already present: %v", got)
	}
	gen.HostProjections.MetricsSwap()
	if got := gatheredSeriesNames(t, reg); !hasSeries(got, "aip_prompt_cache_keepwarm_active_epochs") {
		t.Fatalf("expected keep-warm series after exactly one swap delivery, got %v", got)
	}
	// Admin projection travels opaquely with the process-owned service.
	if !gen.HostProjections.KeepwarmAdmin.Enabled {
		t.Fatal("expected enabled keep-warm admin projection")
	}
	if gen.HostProjections.KeepwarmAdmin.Service == nil {
		t.Fatal("expected non-nil keep-warm admin service")
	}
	if gen.HostProjections.TerminalPolicyProjection == nil {
		t.Fatal("expected non-nil terminal policy projection factory")
	}
	// The lifecycle side-channel owns manager release.
	var kwLife *keepwarmGenerationLifecycle
	for _, life := range gen.Lifecycles {
		if kw, ok := life.(*keepwarmGenerationLifecycle); ok {
			kwLife = kw
			break
		}
	}
	if kwLife == nil {
		t.Fatal("expected keep-warm generation lifecycle in output lifecycles")
	}
	if err := kwLife.Start(context.Background()); err != nil {
		t.Fatalf("lifecycle Start: %v", err)
	}
	if err := kwLife.Stop(context.Background()); err != nil {
		t.Fatalf("lifecycle Stop: %v", err)
	}
	// Second Stop tolerates the already-released registration (idempotent).
	if err := kwLife.Stop(context.Background()); err != nil {
		t.Fatalf("lifecycle second Stop: %v", err)
	}
}

func TestCompileKeepwarmDisabledOmitsPorts(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := NewProcess(context.Background(), ProcessInput{Logger: log})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	gen, err := r.CompileGeneration(context.Background(), GenerationInput{
		Registrations: []aipsdk.Registration{{
			Kind:    aipsdk.PluginKindFeature,
			ID:      keepwarm.ID,
			Enabled: false,
		}},
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	if gen.HostProjections.MetricsSwap != nil {
		t.Fatal("expected nil MetricsSwap port for disabled keep-warm")
	}
	for _, life := range gen.Lifecycles {
		if _, ok := life.(*keepwarmGenerationLifecycle); ok {
			t.Fatal("disabled keep-warm must not contribute a generation lifecycle")
		}
	}
}

type compositionAccountingController struct {
	response promptcache.RenewResponse
	started  chan promptcache.RenewRequest
}

func (c *compositionAccountingController) Renew(_ context.Context, request promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	c.started <- request
	return c.response, nil
}

func (c *compositionAccountingController) Release(context.Context, promptcache.ReleaseRequest) error {
	return nil
}

func TestCompileKeepwarmDeliversMaintenanceAccounting(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	current := now
	clockNow := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return current
	}
	advance := func(delta time.Duration) {
		clockMu.Lock()
		current = current.Add(delta)
		clockMu.Unlock()
	}
	cfg := keepwarm.DefaultConfig()
	cfg.MaxRefreshesPerIdleEpoch = 1
	cfg.MaxConcurrentRenewals = 1
	cfg.RenewTimeout = time.Second

	inputTokens := int64(23)
	var observed billingport.ProviderMaintenanceUsage
	observedUsage := make(chan billingport.ProviderMaintenanceUsage, 1)
	observer := billingport.ProviderMaintenanceUsageObserverFunc(func(_ context.Context, usage billingport.ProviderMaintenanceUsage) error {
		observedUsage <- usage
		return nil
	})
	renewedObservation := promptcache.Observation{
		ALegID:            "a-leg-1",
		BLegID:            "b-leg-1",
		BackendInstanceID: "backend-1",
		TargetID:          "target-1",
		GenerationID:      "generation-renewed",
		Lifecycle:         promptcache.LifecycleSlidingExpiry,
		Timing: promptcache.Timing{
			ObservedAt: now,
			ExpiresAt:  func() *time.Time { t := now.Add(time.Hour); return &t }(),
		},
		Renewable: true,
		Handle:    promptcache.Handle("renewed-handle"),
	}
	controller := &compositionAccountingController{
		started: make(chan promptcache.RenewRequest, 1),
		response: promptcache.RenewResponse{
			Result: promptcache.RenewResult{
				Status:      promptcache.Renewed,
				Observation: &renewedObservation,
			},
			Accounting: &promptcache.AccountingEvidence{
				InputTokens: &inputTokens,
				Presence:    aipapi.UsagePresence{InputTokens: true},
				Source:      promptcache.AccountingSourceProviderReported,
				Authority:   promptcache.AccountingAuthorityAuthoritative,
				Plane:       promptcache.AccountingPlaneProviderBillable,
				DedupeKey:   "controller-dedupe-key",
			},
		},
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := NewProcess(context.Background(), ProcessInput{Logger: log})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}

	gen, err := r.CompileGeneration(context.Background(), GenerationInput{
		KeepwarmConfig:     cfg,
		NowFn:              clockNow,
		KeepwarmAccounting: observer,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	// The keep-warm handle is ledger-owned via the lifecycle side-channel:
	// Start launches the manager (candidate prepare), Stop releases it
	// (rollback/close). Metrics/admin projections travel as opaque CorePorts.
	var kwLife *keepwarmGenerationLifecycle
	for _, life := range gen.Lifecycles {
		if kw, ok := life.(*keepwarmGenerationLifecycle); ok {
			kwLife = kw
			break
		}
	}
	if kwLife == nil {
		t.Fatal("expected keep-warm generation lifecycle in output lifecycles")
	}
	if err := kwLife.Start(context.Background()); err != nil {
		t.Fatalf("lifecycle Start: %v", err)
	}
	defer func() {
		_ = kwLife.Stop(context.Background())
	}()

	kwMgr := kwLife.manager
	if kwMgr == nil {
		t.Fatal("expected non-nil KeepwarmManager")
	}

	inputTokensForObservation := int64(31)
	armed := kwMgr.ArmFromCommittedTurn(keepwarm.ArmInput{
		ALegID:              "a-leg-1",
		BLegID:              "b-leg-1",
		CommittedSuccessful: true,
		ToolEvents: []aipapi.ToolEvent{{
			Kind:     aipapi.ToolEventFinished,
			Category: aipapi.ToolCategoryOSCommand,
		}},
		Observations: []promptcache.Observation{{
			ALegID:            "a-leg-1",
			BLegID:            "b-leg-1",
			BackendInstanceID: "backend-1",
			TargetID:          "target-1",
			GenerationID:      "generation-foreground",
			Lifecycle:         promptcache.LifecycleSlidingExpiry,
			Timing: promptcache.Timing{
				ObservedAt: now,
				ExpiresAt:  func() *time.Time { t := now.Add(time.Minute); return &t }(),
			},
			Renewable: true,
			Handle:    promptcache.Handle("foreground-handle"),
			Evidence:  promptcache.CacheEvidence{TotalTokens: &inputTokensForObservation},
		}},
		BackendInstanceID: "backend-1",
		CanonicalModelID:  "model-1",
		Controller:        controller,
	})
	if !armed.Armed {
		t.Fatalf("arm result = %+v", armed)
	}

	advance(55 * time.Second)
	kwMgr.RunDue(context.Background())

	var request promptcache.RenewRequest
	select {
	case request = <-controller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for composed keep-warm renewal")
	}
	select {
	case observed = <-observedUsage:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for maintenance accounting delivery")
	}

	if request.OperationID == "" {
		t.Fatal("renewal operation ID is empty")
	}
	if observed.OperationID != request.OperationID || observed.ALegID != "a-leg-1" ||
		observed.TargetID != "target-1" || observed.BackendID != "backend-1" || observed.ModelID != "model-1" {
		t.Fatalf("composed maintenance usage = %+v, request = %+v", observed, request)
	}
	if observed.Evidence.InputTokens.Value != inputTokens || !observed.Evidence.InputTokens.Present ||
		observed.Evidence.OutputTokens.Present || observed.Evidence.TotalTokens.Present {
		t.Fatalf("composed maintenance evidence = %+v", observed.Evidence)
	}
	if observed.Evidence.Source != billingport.ProviderMaintenanceSourceProviderReported ||
		observed.Evidence.Authority != billingport.ProviderMaintenanceAuthorityAuthoritative ||
		observed.Evidence.DedupeKey != request.OperationID {
		t.Fatalf("composed maintenance metadata = %+v", observed.Evidence)
	}
}
