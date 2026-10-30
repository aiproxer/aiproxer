package runtimebundle

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// TerminalWorkReadiness is composition-root status for terminal-work recovery (task 4.4/4.5).
type TerminalWorkReadiness struct {
	Configured            bool
	Running               bool
	UnresolvedProviderIDs []string
	StoreReady            bool
	// StoreError is deprecated and always empty; use ErrorCode.
	StoreError string
	// BacklogKnown is true only when MetricsObserver.Snapshot succeeded.
	BacklogKnown bool
	// Backlog is outstanding terminal-work count from MetricsObserver.
	Backlog int
	// ReconcilerPending is unique WorkIDs owned by the ambiguous-append reconciler.
	ReconcilerPending int
	// ErrorCode is an operator-safe code (never raw store/snapshot error text).
	ErrorCode string
}

// terminalWorkRuntime is composition-root ownership for terminal-work recovery
// (tasks 4.4–4.5): processor, intents, queries, metrics, and readiness.
type terminalWorkRuntime struct {
	Processor  *terminalworkapp.Processor
	Registry   *terminalworkapp.Registry
	Store      terminalworkapp.RecoveryStore
	Intents    *terminalworkapp.IntentService
	Queries    *terminalworkapp.QueryService
	Metrics    *terminalworkapp.MetricsObserver
	Pins       *terminalworkapp.GenerationPinTracker
	Reconciler *terminalworkapp.AmbiguousAppendReconciler

	genResolver  *generationPresentResolver
	checkReady   func(context.Context) error
	storeBacking string
	clock        func() time.Time
	prom         *metrics.TerminalWorkProm
	snapshotPub  *snapshotgen.Publisher
}

// BindGenerationManager wires generation presence lookup for exact
// RuntimeInstanceID + RuntimeGenerationID terminal-work rows (task 3.6).
func (rt *terminalWorkRuntime) BindGenerationManager(mgr *runtimehost.Manager) {
	if rt == nil || rt.genResolver == nil || mgr == nil {
		return
	}
	rt.genResolver.SetLookup(mgr.GenerationByIdentity)
}

type snapshotPendingBinder struct {
	pub *snapshotgen.Publisher
}

func (b snapshotPendingBinder) Bind(workID string, versions terminalwork.BoundVersions) (func(), bool) {
	if b.pub == nil {
		return nil, false
	}
	workID = strings.TrimSpace(workID)
	gid, err := strconv.ParseInt(strings.TrimSpace(versions.ExecutableGenerationID()), 10, 64)
	if err != nil || gid <= 0 || workID == "" {
		return nil, false
	}
	if !b.pub.AddPendingWork(gid, workID, versions.ProviderID) {
		// Idempotent no-op: do not return a clear handle that could clear the
		// winner's existing WorkID hold.
		return nil, false
	}
	return func() { b.pub.ClearPendingWork(gid, workID) }, true
}

type terminalWorkBuildInput struct {
	Store         terminalworkapp.RecoveryStore
	Providers     []terminalworkapp.EffectProvider
	OwnerID       string
	ClaimTTL      time.Duration
	ClaimLimit    int
	GlobalMax     int
	PerProvMax    int
	TickInterval  time.Duration
	RenewInterval time.Duration
	Clock         func() time.Time
	StoreBacking  string
	Prom          *metrics.TerminalWorkProm
	SnapshotPub   *snapshotgen.Publisher
}

func buildTerminalWorkFromProduction(owner *processResourceOwner, prod ProductionOptions, clock func() time.Time, bundle *metrics.Bundle, snapshotPub *snapshotgen.Publisher) (*terminalWorkRuntime, error) {
	if prod.TerminalWorkStore == nil {
		return nil, nil
	}
	if owner == nil {
		return nil, fmt.Errorf("runtimebundle: nil process owner")
	}
	providers, err := composeTerminalWorkProviders(prod)
	if err != nil {
		return nil, err
	}
	var prom *metrics.TerminalWorkProm
	if bundle != nil {
		prom = bundle.TerminalWork
	}
	return buildTerminalWorkRuntime(owner, terminalWorkBuildInput{
		Store:         prod.TerminalWorkStore,
		Providers:     providers,
		OwnerID:       prod.TerminalWorkOwnerID,
		ClaimTTL:      prod.TerminalWorkClaimTTL,
		ClaimLimit:    prod.TerminalWorkClaimLimit,
		GlobalMax:     prod.TerminalWorkGlobalMax,
		PerProvMax:    prod.TerminalWorkPerProviderMax,
		TickInterval:  prod.TerminalWorkTickInterval,
		RenewInterval: prod.TerminalWorkRenewInterval,
		Clock:         clock,
		StoreBacking:  "injected",
		Prom:          prom,
		SnapshotPub:   snapshotPub,
	})
}

func composeTerminalWorkProviders(prod ProductionOptions) ([]terminalworkapp.EffectProvider, error) {
	byID := make(map[string]terminalworkapp.EffectProvider, len(prod.RequestRegistrations)+len(prod.TerminalWorkProviders)+1)
	order := make([]string, 0, len(byID))
	add := func(id string, p terminalworkapp.EffectProvider) {
		id = strings.TrimSpace(id)
		if id == "" || p == nil {
			return
		}
		if _, exists := byID[id]; !exists {
			order = append(order, id)
		}
		byID[id] = p
	}
	for _, reg := range prod.RequestRegistrations {
		id := strings.TrimSpace(reg.Descriptor.ID)
		if id == "" || reg.Provider == nil {
			continue
		}
		effect, err := terminalworkapp.NewAuthorityRequestEffectProvider(terminalworkapp.AuthorityRequestEffectConfig{
			ProviderID: id, Provider: reg.Provider, Version: requestRegistrationEffectVersion(reg),
		})
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: derive terminal work provider %q: %w", id, err)
		}
		add(id, effect)
	}
	if conc := concurrencyProviderFromProd(prod); conc != nil {
		effect, err := terminalworkapp.NewLeaseSetEffectProvider(terminalworkapp.LeaseSetEffectConfig{
			ProviderID: "concurrency", Provider: conc, Version: "1",
		})
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: derive lease-set terminal work provider: %w", err)
		}
		add(effect.ProviderID(), effect)
	}
	for _, p := range prod.TerminalWorkProviders {
		if p == nil {
			continue
		}
		id := strings.TrimSpace(p.ProviderID())
		if id == "" {
			return nil, fmt.Errorf("runtimebundle: terminal work provider: empty provider id")
		}
		add(id, p)
	}
	out := make([]terminalworkapp.EffectProvider, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

func concurrencyProviderFromProd(prod ProductionOptions) authority.ConcurrencyProvider {
	if prod.ConcurrencyRegistration != nil {
		return prod.ConcurrencyRegistration.Provider
	}
	return nil
}

func requestRegistrationEffectVersion(reg authority.RequestRegistration) string {
	type versioned interface{ Version() string }
	if v, ok := any(reg.Provider).(versioned); ok {
		if s := strings.TrimSpace(v.Version()); s != "" {
			return s
		}
	}
	return "1"
}

func buildTerminalWorkRuntime(owner *processResourceOwner, in terminalWorkBuildInput) (*terminalWorkRuntime, error) {
	if in.Store == nil {
		return nil, nil
	}
	if owner == nil {
		return nil, fmt.Errorf("runtimebundle: nil process owner")
	}
	reg := terminalworkapp.NewRegistry()
	for _, p := range in.Providers {
		if err := reg.Register(p); err != nil {
			return nil, fmt.Errorf("runtimebundle: terminal work provider: %w", err)
		}
	}
	ownerID := strings.TrimSpace(in.OwnerID)
	if ownerID == "" {
		ownerID = "runtimebundle"
	}
	claimTTL := in.ClaimTTL
	if claimTTL <= 0 {
		claimTTL = time.Minute
	}
	tickInterval := in.TickInterval
	if tickInterval <= 0 {
		tickInterval = time.Second
	}
	renewInterval := in.RenewInterval
	if renewInterval <= 0 {
		renewInterval = claimTTL / 3
		if renewInterval <= 0 {
			renewInterval = 20 * time.Second
		}
	}
	clock := in.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	metricsObs := terminalworkapp.NewMetricsObserver(in.Store, terminalworkapp.MetricsConfig{Clock: clock})
	pins := terminalworkapp.NewGenerationPinTracker()
	genResolver := &generationPresentResolver{}
	cfg := terminalworkapp.Config{
		OwnerID: ownerID, ClaimTTL: claimTTL, ClaimLimit: in.ClaimLimit,
		GlobalMax: in.GlobalMax, PerProviderMax: in.PerProvMax, TickInterval: tickInterval,
		RenewInterval: renewInterval, Clock: clockFunc{now: clock},
		GenerationPins: pins, GenerationResolver: genResolver,
	}
	var execPending terminalworkapp.ExecutablePendingBinder
	if pub := in.SnapshotPub; pub != nil {
		execPending = snapshotPendingBinder{pub: pub}
		cfg.OnTerminalDone = func(rec terminalwork.WorkRecord) {
			gid, err := strconv.ParseInt(strings.TrimSpace(rec.Versions.ExecutableGenerationID()), 10, 64)
			if err != nil || gid <= 0 {
				return
			}
			pub.ClearPendingWork(gid, rec.WorkID)
		}
	}
	if obs := newTerminalWorkProcessObserver(metricsObs, in.Prom); obs != nil {
		cfg.Metrics = obs
	}
	proc, err := terminalworkapp.NewProcessor(in.Store, reg, cfg)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: terminal work processor: %w", err)
	}
	var reconciler *terminalworkapp.AmbiguousAppendReconciler
	if as, ok := any(in.Store).(terminalworkapp.AmbiguousAppendStore); ok {
		reconciler, err = terminalworkapp.NewAmbiguousAppendReconciler(as, terminalworkapp.AmbiguousAppendReconcilerConfig{
			Clock: clock, Pins: pins, ExecutablePending: execPending,
		})
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: ambiguous append reconciler: %w", err)
		}
	}
	backing := strings.TrimSpace(in.StoreBacking)
	if backing == "" {
		backing = "injected"
	}
	intentCfg := terminalworkapp.IntentServiceConfig{Clock: clock, Pins: pins, ExecutablePending: execPending}
	if reconciler != nil {
		intentCfg.AmbiguousHandoff = reconciler
	}
	rt := &terminalWorkRuntime{
		Processor: proc, Registry: reg, Store: in.Store,
		Intents: terminalworkapp.NewIntentService(in.Store, intentCfg),
		Queries: terminalworkapp.NewQueryService(in.Store), Metrics: metricsObs,
		Pins: pins, Reconciler: reconciler, genResolver: genResolver,
		storeBacking: backing, clock: clock, prom: in.Prom, snapshotPub: in.SnapshotPub,
	}
	if ready, ok := in.Store.(interface{ CheckReadiness(context.Context) error }); ok {
		rt.checkReady = ready.CheckReadiness
	}
	reconcilerStarted := false
	if reconciler != nil {
		if err := reconciler.Start(); err != nil {
			return nil, fmt.Errorf("runtimebundle: ambiguous append reconciler start: %w", err)
		}
		reconcilerStarted = true
	}
	if err := proc.Start(context.Background()); err != nil {
		if reconcilerStarted {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = reconciler.Shutdown(ctx)
			cancel()
		}
		return nil, fmt.Errorf("runtimebundle: terminal work start: %w", err)
	}
	owner.Own(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return proc.Shutdown(ctx)
	})
	if reconciler != nil {
		owner.Own(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return reconciler.Shutdown(ctx)
		})
	}
	return rt, nil
}

func applyTerminalWorkProm(prom *metrics.TerminalWorkProm, snap terminalworkapp.MetricsSnapshot) {
	if prom == nil {
		return
	}
	prom.ApplySnapshot(metrics.TerminalWorkSnapshot{
		Backlog: snap.Backlog, OldestAgeSec: snap.OldestAge.Seconds(),
		Pending: snap.Pending, Retrying: snap.Retrying, Quarantined: snap.Quarantined,
		Completed: snap.Completed, Claimed: snap.Claimed,
	})
}

// ctxOrBackground returns ctx, or Background when callers pass a nil context.
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// Readiness reports running state, unresolved providers, backlog, and store readiness.
func (rt *terminalWorkRuntime) Readiness(ctx context.Context) TerminalWorkReadiness {
	out := TerminalWorkReadiness{}
	if rt == nil || rt.Processor == nil {
		return out
	}
	out.Configured = true
	snap := rt.Processor.Readiness()
	out.Running = snap.Running
	out.UnresolvedProviderIDs = append([]string(nil), snap.UnresolvedProviderIDs...)
	if rt.Reconciler != nil {
		out.ReconcilerPending = rt.Reconciler.Pending()
	}
	if rt.snapshotPub != nil {
		for _, id := range rt.snapshotPub.UnresolvedProviderIDs() {
			out.UnresolvedProviderIDs = appendUniqueString(out.UnresolvedProviderIDs, id)
		}
	}
	ctx = ctxOrBackground(ctx)
	safeUnavailable := string(controlplane.ReasonBackingUnavailable)
	if rt.Metrics == nil {
		out.BacklogKnown, out.ErrorCode = false, safeUnavailable
	} else if metricsSnap, err := rt.Metrics.Snapshot(ctx); err != nil {
		out.BacklogKnown, out.ErrorCode = false, safeUnavailable
	} else {
		out.BacklogKnown, out.Backlog = true, metricsSnap.Backlog
	}
	if !out.Running {
		out.ErrorCode = safeUnavailable
	}
	if rt.checkReady == nil {
		out.StoreReady = true
		return out
	}
	if err := rt.checkReady(ctx); err != nil {
		out.StoreReady, out.ErrorCode = false, safeUnavailable
		return out
	}
	out.StoreReady = true
	return out
}

func (rt *terminalWorkRuntime) publishMetrics(ctx context.Context) error {
	if rt == nil || rt.Metrics == nil || rt.prom == nil {
		return nil
	}
	snap, err := rt.Metrics.Snapshot(ctxOrBackground(ctx))
	if err != nil {
		return err
	}
	applyTerminalWorkProm(rt.prom, snap)
	return nil
}

func (rt *terminalWorkRuntime) readinessComponent(ctx context.Context) (controlplane.ReadinessComponentStatus, error) {
	if rt == nil || rt.Processor == nil {
		return controlplane.ReadinessComponentStatus{
			Component: controlplane.ReadinessComponentTerminalRecovery, State: controlplane.CapabilityDisabled,
			Reason: controlplane.ReasonDisabled, EnforcementScope: controlplane.EnforcementScopeDisabled,
		}, nil
	}
	ctx = ctxOrBackground(ctx)
	row := controlplane.ReadinessComponentStatus{
		Component:        controlplane.ReadinessComponentTerminalRecovery,
		EnforcementScope: controlplane.EnforcementScopeForStoreBacking(rt.storeBacking, strings.EqualFold(rt.storeBacking, "postgres")),
		StoreBacking:     rt.storeBacking,
	}
	unavailable := func() (controlplane.ReadinessComponentStatus, error) {
		row.State, row.Reason = controlplane.CapabilityUnavailable, controlplane.ReasonBackingUnavailable
		return row, nil
	}
	if rt.checkReady != nil {
		if err := rt.checkReady(ctx); err != nil {
			return unavailable()
		}
	}
	if !rt.Processor.Running() {
		return unavailable()
	}
	snap := rt.Processor.Readiness()
	if len(snap.UnresolvedProviderIDs) > 0 {
		row.State, row.Reason = controlplane.CapabilityDegraded, controlplane.ReasonPendingTerminalWork
		row.ProviderIDs = append([]string(nil), snap.UnresolvedProviderIDs...)
		return row, nil
	}
	if rt.Metrics == nil {
		return unavailable()
	}
	metricsSnap, err := rt.Metrics.Snapshot(ctx)
	if err != nil {
		return unavailable()
	}
	_ = rt.publishMetrics(ctx)
	if metricsSnap.Backlog > 0 || (rt.Reconciler != nil && rt.Reconciler.Pending() > 0) {
		row.State, row.Reason = controlplane.CapabilityDegraded, controlplane.ReasonPendingTerminalWork
		return row, nil
	}
	row.State, row.Reason = controlplane.CapabilityReady, controlplane.ReasonNone
	return row, nil
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
