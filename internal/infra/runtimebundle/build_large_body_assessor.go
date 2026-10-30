package runtimebundle

import (
	"crypto/rand"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// buildProcessSpoolLedger constructs and binds the process-owned large payload spool ledger.
func buildProcessSpoolLedger(cfg *config.Config, b *metrics.Bundle) (*largebody.SpoolLedger, error) {
	if cfg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	spoolLedger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      cfg.Server.LargePayloadFastPath.EffectiveMemorySpoolBytes(),
		MaxInflightSpoolBytes: cfg.Server.LargePayloadFastPath.EffectiveMaxInflightSpoolBytes(),
	})
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: spool ledger: %w", err)
	}
	if b != nil {
		spoolLedger.SetObserver(b.LargePayloadDiagnostics())
	}
	return spoolLedger, nil
}

// largeBodyAssessorInput captures the dependencies needed to assemble the production
// LargeBodyAssessor in runtimebundle (Phase 4).
type largeBodyAssessorInput struct {
	Cfg                           *config.Config
	Bctx                          buildContext
	Opts                          *BuildOptions
	In                            executorBuildInput
	RoutingRT                     runtime.RoutingRuntime
	AccountingRT                  runtime.AccountingRuntime
	Prod                          ProductionOptions
	DefBE                         string
	AliasResolver                 *routing.AliasResolver
	ExecResolver                  routing.BackendExecutionResolver
	ExecPolicy                    config.ExecutionCompositionPolicy
	OverrideReader                routeoverride.Reader
	CapsResolverWireProofSubsumed bool
	// DefaultRouteBindings are the generation-owned model bindings the derived
	// default route needs. The wire lane consults them for a request whose
	// route origin is that default route, so the wire lane and the canonical
	// lane address the same model.
	DefaultRouteBindings routing.DefaultRouteBindings
}

// compositionIDPrefix scopes assessor/executor generation identities to one
// runtime composition incarnation (Requirement 2).
const compositionIDPrefix = "composition-"

// newCompositionGenID allocates one bounded collision-resistant composition
// incarnation identity per buildLargeBodyAssessor construction. The nonce is
// ephemeral: never persisted and never derived from economic publication or
// any process-global sequence, so coexisting compositions always disagree
// even when their economic publication is unchanged or absent.
func newCompositionGenID() string {
	return compositionIDPrefix + rand.Text()
}

// buildLargeBodyAssessor compiles the generation wire eligibility summary, builds
// the authority, initial, and route-override gates, and unifies them into a ProductionLargeBodyAssessor.
func buildLargeBodyAssessor(in largeBodyAssessorInput) (*runtime.ProductionLargeBodyAssessor, string, error) {
	genID := newCompositionGenID()

	wireBackendMap := make(largebody.WireBackendMap, len(in.In.Model.Backends))
	knownBackends := make(map[string]struct{}, len(in.In.Model.Backends))
	for id, be := range in.In.Model.Backends {
		wireBackendMap[id] = be.AsWireBackend()
		knownBackends[id] = struct{}{}
	}
	validator := routing.NewGenerationSelectorValidator(in.AliasResolver, in.DefBE, knownBackends, in.ExecResolver, in.ExecPolicy)
	initialRouteGate := largebody.NewInitialRouteAssessmentGate(in.AliasResolver, in.DefBE, in.ExecResolver, in.ExecPolicy, nil, wireBackendMap)
	initialRouteGate.DefaultRouteBindings = in.DefaultRouteBindings
	wireProofGate := largebody.NewBackendWireProofGate(
		initialRouteGate,
		largebody.NewRouteOverrideAssessmentGate(in.OverrideReader, validator, wireBackendMap), nil, wireBackendMap)

	census := largebody.NewStandardDependencyCensus(genID)
	var contribs *aipfeature.ContributionSet
	hasTrafficPlanes := false
	if in.Opts != nil && !in.Opts.FeaturePlanes.IsZero() {
		contribs = in.Opts.FeaturePlanes.ToContributions()
		for i := range census.Planes {
			if contribs.Has(census.Planes[i].ID) {
				census.Planes[i].Occupied = true
			}
		}
		hasTrafficPlanes = contribs.Has("traffic_observers") || contribs.Has("raw_capture_sinks") || contribs.Has("traffic_redactors")
	}
	if in.Bctx.Bus != nil {
		s, r, resp, tl := in.Bctx.Bus.HookChainLengths()
		census.Hooks = largebody.HookEligibilityInput{SubmitOccupied: s > 0, RequestPartOccupied: r > 0, ResponsePartOccupied: resp > 0, ToolOccupied: tl > 0}
	}

	hasLocalTurn := contribs != nil && contribs.Has("local_turn_handlers")
	hasTerminalDecision := contribs != nil && contribs.Has("terminal_decision_provider")

	census.Ports.BackendsEmpty = len(in.In.Model.Backends) == 0
	in.In.CorePorts.AddToCensus(&census, in.In.ConversationStore != nil, hasLocalTurn, hasTerminalDecision)
	census.Ports.ExposureAdmissionOccupied = in.Prod.BillingExposureAdmission != nil
	census.Ports.BillingIdentityCustomCallbacks = in.Prod.BillingIdentity.HasCustomCallCallbacks()
	census.Ports.CapsResolverOccupied = in.RoutingRT.CapsResolver != nil
	census.Ports.CapsResolverWireProofSubsumed = in.CapsResolverWireProofSubsumed
	census.Ports.CatalogResolverOccupied = in.RoutingRT.CatalogResolver != nil
	census.Ports.EligibilityResolverOccupied = in.RoutingRT.EligibilityResolver != nil
	census.Ports.RequestTokenEstimatorOccupied = in.RoutingRT.RequestTokenEstimator != nil
	census.Ports.PreflightEnabled = in.AccountingRT.Preflight != nil
	census.Ports.StreamUsageOccupied = in.AccountingRT.StreamUsage != nil
	census.Ports.AdminCountServiceOccupied = in.AccountingRT.AdminCountService != nil
	census.Ports.TrafficCapturing = len(in.Prod.TrafficObservers) > 0 || hasTrafficPlanes

	// Item 5: Register security.session_recorder port occupancy. Wire execution exercises
	// the recorder through canonical retryRecvStream ownership (recordClientFacing /
	// RecordPostHookStreamEvent), so an occupied recorder no longer blocks wire eligibility.
	hasSecureRecorder := in.In.Persistence != nil &&
		in.In.Persistence.SecureSession != nil &&
		in.In.Persistence.SecureSession.recorder != nil
	census.AddPort("security.session_recorder", hasSecureRecorder)

	census.TwoPhaseExecutorAvailable = true
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: true,
	}, in.Cfg.Server.LargePayloadFastPath.EffectiveMaxSemanticFactBytes())
	if err != nil {
		return nil, "", fmt.Errorf("runtimebundle: compile wire eligibility summary: %w", err)
	}

	authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	return runtime.NewProductionLargeBodyAssessor(genID, genID, authGate, wireProofGate, runtime.StandardLaneDomainPolicies()), genID, nil
}

// buildStandardLargePayloadConfig compiles the frontend fast-path candidate configuration
// from the frozen server config and candidate process references.
func buildStandardLargePayloadConfig(cand *candidateAssembly, frozen *config.Config) httpcontract.LargePayloadInput {
	if frozen == nil || !frozen.Server.LargePayloadFastPath.Enabled {
		return httpcontract.LargePayloadInput{}
	}
	out := httpcontract.LargePayloadInput{
		Enabled:              true,
		ThresholdBytes:       frozen.Server.LargePayloadFastPath.EffectiveThresholdBytes(),
		MemorySpoolBytes:     frozen.Server.LargePayloadFastPath.EffectiveMemorySpoolBytes(),
		MaxSemanticFactBytes: frozen.Server.LargePayloadFastPath.EffectiveMaxSemanticFactBytes(),
		SpoolDir:             frozen.Server.LargePayloadFastPath.EffectiveSpoolDir(),
		Diagnostics:          largebody.NoopDiagnosticsObserver{},
	}
	if cand != nil {
		out.SpoolLedger = cand.process.spoolLedger
		if cand.process.metrics != nil {
			out.Diagnostics = cand.process.metrics.LargePayloadDiagnostics()
		}
		if ex := cand.execution.executor; ex != nil {
			if pa, ok := ex.LargeBodyAssessor.(*runtime.ProductionLargeBodyAssessor); ok && pa != nil && pa.AuthorityGate != nil {
				out.WireEligibility = pa.AuthorityGate.Summary
			}
		}
	}
	return out
}
