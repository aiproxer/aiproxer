package featurehost

// Composed production-path proof of the two Proxy Memory background phases
// (design §2, Requirements 1.2 and 1.3). It replaces only the mocked
// background client and the stub backend of the §16 sentinel composition: the
// process, the generation, the real feature worker, the real process
// background scheduler bound to the real generation executor runner, the real
// generic-core runtime executor and a recording backend are the production
// ones. The proof observes attribution, never money.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	continuitybunstore "github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	infraaux "github.com/aiproxer/aiproxer/internal/infra/auxiliary"
	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	// proxyMemoryExtractBackendID and proxyMemoryConsolidateBackendID are the
	// backend identifiers the two phase selectors name in the composed
	// generation, so each phase opens its own recording backend.
	proxyMemoryExtractBackendID     = "aux-extract"
	proxyMemoryConsolidateBackendID = "aux-consolidate"
)

// proxyMemoryPhaseOpen is one observed backend open of the composed proof.
type proxyMemoryPhaseOpen struct {
	backendID string
	role      string
	aLegID    string
	scope     scope.PrincipalScopeView
	// extensionKeys are the canonical extension keys the phase carried. The
	// funding decision, the sponsor key and the account never travel on a
	// canonical call, so this list stays free of them.
	extensionKeys []string
}

// proxyMemoryPhaseBackend is the recording backend of the composed proof: it
// records every open with the trusted lineage role and the detached scope, and
// answers each phase with the canned reply its parser expects. Its
// capabilities are the ones the phase calls need: streaming, the
// structured-output capability the JSON response type requires, and reasoning.
type proxyMemoryPhaseBackend struct {
	mu    sync.Mutex
	opens []proxyMemoryPhaseOpen
}

func (b *proxyMemoryPhaseBackend) open(backendID string) func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	return func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		role := ""
		if raw, ok := call.Extensions["aip.aux.lineage.v1"]; ok {
			var lineage struct {
				Role string `json:"role"`
			}
			if err := json.Unmarshal(raw, &lineage); err == nil {
				role = lineage.Role
			}
		}
		observed, _ := scope.ScopeFromContext(ctx)
		extensionKeys := make([]string, 0, len(call.Extensions))
		for key := range call.Extensions {
			extensionKeys = append(extensionKeys, key)
		}
		sort.Strings(extensionKeys)
		b.mu.Lock()
		b.opens = append(b.opens, proxyMemoryPhaseOpen{
			backendID: backendID, role: role, aLegID: call.Session.ALegID,
			scope: observed, extensionKeys: extensionKeys,
		})
		b.mu.Unlock()
		text, ok := map[string]string{
			sentinelRolePhase1: sentinelPhase1Reply,
			sentinelRolePhase2: sentinelPhase2Reply,
		}[role]
		if !ok {
			return nil, fmt.Errorf("featurehost: unexpected proxy-memory phase role %q", role)
		}
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: text},
			{
				Kind: aipapi.EventUsageDelta, InputTokens: 11, OutputTokens: 7, TotalTokens: 18, CostNanoUnits: 13, Currency: "USD", CostPresent: true,
				UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "proxy-memory-provider-" + role,
				},
			},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	}
}

func (b *proxyMemoryPhaseBackend) observed() []proxyMemoryPhaseOpen {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]proxyMemoryPhaseOpen(nil), b.opens...)
}

// proxyMemoryTerminalSink is the composed proof's recording sink: it records
// the terminal call and leg records under a mutex.
//
// The appends are synchronous on this path. The runtime appends both records
// inside its terminal owner, the background job collects the stream (and closes
// it) before its deferred finish closes the job, and the worker's Await
// returns only after that. Both worker ticks therefore complete before the
// proof reads the sink, so a post-completion snapshot observes every record.
type proxyMemoryTerminalSink struct {
	mu    sync.Mutex
	calls []usageevidence.CallUsageRecord
	legs  []usageevidence.CallLegUsageRecord
}

func (s *proxyMemoryTerminalSink) AppendCall(_ context.Context, record usageevidence.CallUsageRecord) error {
	s.mu.Lock()
	s.calls = append(s.calls, record)
	s.mu.Unlock()
	return nil
}

func (s *proxyMemoryTerminalSink) AppendLeg(_ context.Context, record usageevidence.CallLegUsageRecord) error {
	s.mu.Lock()
	s.legs = append(s.legs, record)
	s.mu.Unlock()
	return nil
}

func (s *proxyMemoryTerminalSink) snapshot() ([]usageevidence.CallUsageRecord, []usageevidence.CallLegUsageRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]usageevidence.CallUsageRecord(nil), s.calls...), append([]usageevidence.CallLegUsageRecord(nil), s.legs...)
}

// proxyMemoryBillingSeams is the composed proof's billing seams: the cheap
// credit screen admits every account and the operational-exposure seam admits
// one open exposure per admitted call, so the proof observes attribution and
// not a credit or pricing policy.
type proxyMemoryBillingSeams struct{}

func (proxyMemoryBillingSeams) Check(context.Context, string) error { return nil }

func (proxyMemoryBillingSeams) Admit(_ context.Context, in runtime.BillingExposureAdmissionInput) (billingport.CallExposure, error) {
	return billingport.CallExposure{AccountID: "acct:proxy-memory", CallID: in.CallID, Status: billingport.ExposureOpen}, nil
}

// proxyMemoryProductionComposition is the composed production path: the real
// process, the real generation, the real process background scheduler bound to
// the real generation executor runner, the real generic-core runtime executor
// and the recording backend, plus the observable surfaces the composed proofs
// read.
type proxyMemoryProductionComposition struct {
	backend    *proxyMemoryPhaseBackend
	sink       *proxyMemoryTerminalSink
	worker     *proxymemory.Worker
	store      proxymemory.Store
	metrics    *prometheus.Registry
	executor   *runtime.Executor
	foreground *sentinelBackend
}

// composeProxyMemoryProduction compiles the production proxy-memory
// generation with the given operator funding decision, runs one captured
// session through both background phases and returns the observable surfaces.
//
// It is the SINGLE composition of the composed proofs: the production
// execution proof and the funding proofs share it, so the fixture exists once.
//
// The billing seams are stubs (a permissive credit gate, a permissive exposure
// admission and an in-memory terminal sink), so this composition is NOT a
// monetary host and never claims one. The customer-funded path on a real
// monetary host is proved by the monetary-host composition.
func composeProxyMemoryProduction(t *testing.T, fundingMode string, foregroundExtensions map[string]json.RawMessage) proxyMemoryProductionComposition {
	t.Helper()
	ctx := context.Background()
	clock := newSentinelClock()
	registry := prometheus.NewRegistry()

	continuityDB := certificationOpenSQLite(t, certificationSQLiteDSN(filepath.Join(t.TempDir(), "proxy-memory-production.db")))
	memoryDB := certificationOpenSQLite(t, certificationSQLiteDSN(filepath.Join(t.TempDir(), "proxy-memory-production-memory.db")))
	require.NoError(t, proxymemorystore.EnsureSchema(ctx, memoryDB))
	continuity, err := continuitybunstore.New(continuityDB)
	require.NoError(t, err)
	rt, err := NewProcess(ctx, ProcessInput{
		Logger: slog.Default(), BunDB: continuityDB, ContinuityStore: continuity, MetricsRegistry: registry,
		FeatureDatabases: &proxyMemoryTestDatabases{database: memoryDB, identity: "production-memory"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })

	memoryStore := proxymemorystore.New(memoryDB, proxymemorystore.NewALegReader(continuityDB))
	writeProbe := &proxyMemorySentinelWriteProbe{Store: memoryStore, captured: make(chan struct{})}
	proxyMemoryInstallCompositionStore(t, func(store proxymemory.Store) proxymemory.Store {
		writeProbe.Store = store
		return writeProbe
	})

	// The REAL process background scheduler, bound to the REAL generation
	// executor runner: the worker reaches the real runtime executor through
	// it, exactly as the production composition does.
	scheduler, err := auxreq.NewBackgroundScheduler(ctx, nil, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 8})
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheduler.Close() })
	genRunner := infraaux.NewGenerationExecutorRunner()

	var worker *proxymemory.Worker
	previousHook := proxyMemoryWorkerHook
	proxyMemoryWorkerHook = func(w *proxymemory.Worker) { worker = w }
	t.Cleanup(func() { proxyMemoryWorkerHook = previousHook })

	// The sentinel's registration, with the phase selectors naming the
	// auxiliary backend identifiers explicitly.
	//
	// The funding mode is declared explicitly: the composition binds the
	// operator's funding decision to every background job, and an undeclared
	// mode leaves the background work refused before any provider open.
	registration := "principals:\n  - " + sentinelPrincipalA +
		"\nextract_route: " + proxyMemoryExtractBackendID + ":model" +
		"\nconsolidate_route: " + proxyMemoryConsolidateBackendID + ":model"
	if fundingMode != "" {
		registration += "\nfunding_mode: " + fundingMode
	}
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(registration), &node))
	out, err := rt.CompileGeneration(ctx, GenerationInput{
		Registrations: []aipsdk.Registration{{
			Kind: aipsdk.PluginKindFeature, ID: proxymemory.ID, Enabled: true, Config: aipsdk.ConfigPayload{Node: node},
		}},
		BackgroundClient: scheduler.BindRunner(genRunner),
		NowFn:            clock.Now,
	})
	require.NoError(t, err)
	require.NotNil(t, worker, "the worker hook must capture the composed worker")
	// Without a declared model policy the feature binds no pre-open guard,
	// so the generation's traffic runs with no guard at all (design §2).
	require.Nil(t, out.CorePorts.AttemptGuard, "an undeclared model policy must bind no attempt guard")

	// The capture lifecycle is started; the worker is driven by hand, so no
	// wall-clock ticker runs.
	require.GreaterOrEqual(t, len(out.Lifecycles), 2)
	captureLife := out.Lifecycles[len(out.Lifecycles)-2]
	require.NoError(t, captureLife.Start(ctx))
	t.Cleanup(func() { _ = captureLife.Stop(context.Background()) })

	backend := &proxyMemoryPhaseBackend{}
	sink := &proxyMemoryTerminalSink{}
	foreground := &sentinelBackend{}
	ex := sentinelExecutor(t, continuityDB, out, foreground)
	phaseCaps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityStructuredOutputs, aipapi.CapabilityReasoning)
	ex.Backends = map[string]execbackend.Backend{
		"only":                          foreground.backend(),
		proxyMemoryExtractBackendID:     {Caps: phaseCaps, Open: backend.open(proxyMemoryExtractBackendID)},
		proxyMemoryConsolidateBackendID: {Caps: phaseCaps, Open: backend.open(proxyMemoryConsolidateBackendID)},
	}
	ex.BillingIdentity = runtime.BillingIdentity{
		AccountID: func(context.Context, aipapi.Call) string { return "acct:proxy-memory" },
		// No monetary-host capability: these billing seams are stubs, not a
		// validated monetary authority. The composition is therefore the
		// operator-sponsored, usage-only one (see the funding mode above).
		CustomerPricingRef: func(context.Context, aipapi.Call) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "pricing:proxy-memory", Version: "1"}
		},
		ChargePolicyRef: func(context.Context, aipapi.Call) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "policy:proxy-memory", Version: "1"}
		},
		OperatorRateRef: func(context.Context, string, string) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "operator:proxy-memory", Version: "1"}
		},
	}
	ex.BillingCreditGate = proxyMemoryBillingSeams{}
	ex.BillingExposureAdmission = proxyMemoryBillingSeams{}
	ex.TerminalUsageSink = sink
	genRunner.Bind(ex)

	// One foreground turn captures the session the phases work on. The
	// foregroundExtensions are the client-supplied canonical claims of the
	// negative case: they must never become funding authority.
	foregroundCall := sentinelCodexTurn("proxy-memory-production")
	if len(foregroundExtensions) > 0 {
		foregroundCall.Extensions = map[string]json.RawMessage{}
		maps.Copy(foregroundCall.Extensions, foregroundExtensions)
	}
	sentinelServe(t, ex, sentinelScope(sentinelPrincipalA), foregroundCall, foreground)
	// The capture prerequisite is bounded: a dropped capture must fail the test
	// instead of hanging the package until its own timeout.
	captureCtx, cancelCapture := context.WithTimeout(ctx, 30*time.Second)
	defer cancelCapture()
	select {
	case <-writeProbe.captured:
	case <-captureCtx.Done():
		t.Fatal("the captured session was not committed before the worker ticks")
	}
	require.NoError(t, writeProbe.captureErr)

	// Seven hours later the worker extracts and consolidates on the real path.
	clock.Advance(7 * time.Hour)
	worker.Tick(ctx)
	worker.Tick(ctx)

	return proxyMemoryProductionComposition{
		backend:    backend,
		sink:       sink,
		worker:     worker,
		store:      memoryStore,
		metrics:    registry,
		executor:   ex,
		foreground: foreground,
	}
}

// TestProxyMemoryProductionExecution_BothPhasesReachTheBackend is the
// composed real-path proof of the two background phases: exactly one backend
// open per phase, the trusted detached lineage of each phase, a distinct
// billing call identity per phase, and one terminal leg record per phase with
// the auxiliary class and the exact role.
//
// The composition is the operator-sponsored, usage-only one: the phases open
// the backend, the usage is metered, and no customer account is closed. The
// customer-funded path on a real monetary host is proved by the monetary-host
// composition, not here.
func TestProxyMemoryProductionExecution_BothPhasesReachTheBackend(t *testing.T) {
	ctx := context.Background()
	composition := composeProxyMemoryProduction(t, "operator_sponsored", nil)
	backend := composition.backend
	sink := composition.sink
	registry := composition.metrics
	memoryStore := composition.store
	foreground := composition.foreground

	// Both phases ran to their published result, not merely to a backend open.
	require.Equal(t, 1.0, proxyMemoryCounterValue(t, registry, proxymemory.EventPhase1Succeeded))
	require.Equal(t, 1.0, proxyMemoryCounterValue(t, registry, proxymemory.EventPhase2Published))
	published, _, err := memoryStore.PublishedSummary(ctx, proxymemory.Owner{
		TenantID: sentinelTenant, PrincipalID: sentinelPrincipalA,
		ProjectKey: proxymemory.ProjectKey(proxymemory.NormalizePath(sentinelProjectPath)),
	})
	require.NoError(t, err)
	require.Equal(t, sentinelSummary, published, "the phase 2 result must be published")

	// ---- exactly one backend open per phase, counted at the backend ----
	opens := backend.observed()
	require.Len(t, opens, 2, "only the two phases may open the auxiliary backends")
	byRole := map[string]proxyMemoryPhaseOpen{}
	for _, o := range opens {
		require.NotContains(t, byRole, o.role, "a phase must open the backend exactly once")
		byRole[o.role] = o
	}
	require.Equal(t, proxyMemoryExtractBackendID, byRole[sentinelRolePhase1].backendID)
	require.Equal(t, proxyMemoryConsolidateBackendID, byRole[sentinelRolePhase2].backendID)

	// ---- the trusted detached lineage of each phase ----
	parentALeg := foreground.callAt(t, 0).Session.ALegID
	require.NotEmpty(t, parentALeg)
	for _, role := range []string{sentinelRolePhase1, sentinelRolePhase2} {
		o := byRole[role]
		require.NotEmpty(t, o.aLegID, "the detached child must own a private A-leg")
		require.NotEqual(t, parentALeg, o.aLegID, "the detached child A-leg must differ from the parent turn")
		require.Equal(t, scope.OriginInternal, o.scope.Origin)
		require.Equal(t, scope.SubjectService, o.scope.SubjectKind)
		require.Equal(t, sentinelPrincipalA, o.scope.PrincipalID.String())
		require.Equal(t, sentinelTenant, o.scope.TenantID.String())
	}
	require.NotEqual(t, byRole[sentinelRolePhase1].aLegID, byRole[sentinelRolePhase2].aLegID,
		"each phase owns its own private A-leg")

	// ---- one terminal leg record per phase, and no customer closure ----
	//
	// The operator sponsors the background work, so each phase meters its
	// usage as a terminal leg record and no customer account is closed: the
	// call closure needs an account, and the operator-sponsored decision
	// admits no customer exposure.
	calls, legs := sink.snapshot()
	auxiliaryCalls := 0
	for _, c := range calls {
		if c.Workload.Class == usageevidence.WorkloadClassAuxiliary {
			auxiliaryCalls++
		}
	}
	require.Zero(t, auxiliaryCalls, "an operator-sponsored phase must not close a customer account")
	callIDs := map[string]usageevidence.BillingCallID{}
	legCount := map[string]int{}
	for _, l := range legs {
		if l.Workload.Class != usageevidence.WorkloadClassAuxiliary {
			continue
		}
		callIDs[string(l.Workload.Role)] = l.CallID
		legCount[string(l.Workload.Role)]++
	}
	require.Len(t, callIDs, 2, "each phase must own one auxiliary terminal leg record")
	require.NotEqual(t, callIDs[sentinelRolePhase1], callIDs[sentinelRolePhase2],
		"each phase must own a distinct billing call identity")
	for _, role := range []string{sentinelRolePhase1, sentinelRolePhase2} {
		require.Equal(t, 1, legCount[role], "role %q terminal leg records", role)
	}
}

// TestProxyMemoryPhaseRolesMatchTheBillingAllowlist pins the feature-side role
// constants against the billing allowlist, so the two closed lists cannot
// drift. The feature keeps its own unexported constants; the pin compares the
// roles the exported request builders actually stamp with the role the closed
// billing allowlist registers and projects.
func TestProxyMemoryPhaseRolesMatchTheBillingAllowlist(t *testing.T) {
	cfg := proxymemory.DefaultConfig()
	cfg.Principals = []string{sentinelPrincipalA}
	cfg.ExtractRoute = proxyMemoryExtractBackendID + ":model"
	cfg.ConsolidateRoute = proxyMemoryConsolidateBackendID + ":model"
	require.NoError(t, cfg.Validate())

	phase1, err := proxymemory.BuildPhase1Request(cfg, proxymemory.Phase1Input{
		SessionRef: "session-0123456789ab", ProjectLabel: "app", Transcript: "a captured transcript",
	})
	require.NoError(t, err)
	require.Equal(t, sentinelRolePhase1, phase1.Role)

	phase2, _, err := proxymemory.BuildPhase2Request(cfg, proxymemory.Phase2Input{ProjectLabel: "app"})
	require.NoError(t, err)
	require.Equal(t, sentinelRolePhase2, phase2.Role)

	// The roles the feature stamps are the roles the closed billing allowlist
	// projects, so the two lists cannot drift.
	for _, role := range []string{phase1.Role, phase2.Role} {
		identity, err := usageevidence.WorkloadIdentityFromAuxiliaryRole(role)
		require.NoError(t, err, "the feature role %q must be a registered billing role", role)
		require.Equal(t, usageevidence.WorkloadClassAuxiliary, identity.Class)
		require.Equal(t, role, string(identity.Role))
	}
}
