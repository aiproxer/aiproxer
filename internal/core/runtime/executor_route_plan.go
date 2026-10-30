package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type candidateFailureHistory struct {
	CapabilityReject aipapi.NegotiationResult
	TransportReject  aipapi.TransportNegotiationResult
	AdmissionErr     error
	// SwallowedCapabilityReject is the last swallowed Open failure that
	// matches aipapi.ErrCapabilityReject. Unlike transient swallowed
	// failures, a capability mismatch names a request the candidate cannot
	// serve, so it is kept for the terminal error instead of being dropped
	// with the planning error.
	SwallowedCapabilityReject error
	// SwallowedUpstreamFailure is the last swallowed Open failure that names
	// an upstream fault rather than a routing verdict. A backend that opened
	// no stream is such a fault, so it is kept for the terminal error: an
	// exhausted route must report the real cause instead of a routing
	// exhaustion. It is the last verdict FinalError consults, so it never
	// masks a more specific reason the route failed.
	SwallowedUpstreamFailure error
	ContextLimit             bool
	TransformExcludes        *transformExcludeTracker
	ParallelFailure          error
	AffinityReset            string
	progress                 *recoveryController
}

func (h *candidateFailureHistory) FinalError(base error) error {
	if h == nil {
		return base
	}
	switch {
	case h.TransportReject.Kind == aipapi.NegotiationReject:
		return h.TransportReject.Err()
	case h.AdmissionErr != nil:
		return h.AdmissionErr
	case h.CapabilityReject.Kind == aipapi.NegotiationReject:
		return h.CapabilityReject.Err()
	case h.SwallowedCapabilityReject != nil:
		// A tried candidate's capability verdict outranks the request-shape
		// verdicts below: context limits and transform exclusions describe
		// the request, while this names a backend that refused it. It stays
		// below the admission-time verdicts above so no previously
		// recognized outcome changes when both are recorded.
		return h.SwallowedCapabilityReject
	case h.ContextLimit:
		return aipapi.ErrAllCandidatesContextLimitExceeded
	case h.TransformExcludes != nil && h.TransformExcludes.allExcludedError() != nil:
		return h.TransformExcludes.allExcludedError()
	case h.SwallowedUpstreamFailure != nil:
		// An observed upstream fault outranks the parallel-race summary
		// below, because it names the exact backend and phase that failed.
		// It stays below the request-shape verdicts above: a request that no
		// candidate can serve explains the route failure better than a
		// backend fault observed on the way there.
		return h.SwallowedUpstreamFailure
	case h.ParallelFailure != nil:
		return h.ParallelFailure
	default:
		return base
	}
}

type routeFacts struct {
	sel         *routing.Selector
	requestSize routing.RequestSizeEstimate
	affinityKey affinity.Key
	affinitySet bool
	rng         routing.Rng
	failoverReq capabilities.FailoverRequirementSet
}
type routePlanState struct {
	routeFacts
	progress *recoveryController
}

// prepareRouteSelector applies this Executor's routing configuration.
// Callers keep model-resolver selection and error wrapping.
func (e *Executor) prepareRouteSelector(selector string, resolver routing.NativeModelResolver) (*routing.Selector, error) {
	return routing.PrepareSelector(selector, e.SelectorAliases, e.DefaultBackend,
		e.BackendExecutionResolver, e.ExecutionCompositionPolicy, resolver)
}

func (e *Executor) buildRoutePlan(ctx context.Context, prep *preparedRequest) (*routePlanState, error) {
	var sel *routing.Selector
	var requestSize routing.RequestSizeEstimate
	var failoverReq capabilities.FailoverRequirementSet
	var aLegID string
	var weightedFirstConsumed bool
	var recvViews execctx.Views
	var recvViewsOK bool

	if prep.wirePayload != nil {
		wp := prep.wirePayload
		var err error
		if wp.selector != nil {
			sel = wp.selector
		} else {
			selModel := wp.turnFacts.Route.RouteSelector
			if selModel == "" {
				selModel = wp.turnFacts.Route.CandidateModel
			}
			if selModel == "" {
				selModel = wp.turnFacts.Route.ClientModel
			}
			sel, err = e.prepareRouteSelector(
				selModel,
				wireRouteModelResolver(ctx),
			)
			if err != nil {
				return nil, fmt.Errorf("executor: route selector: %w", err)
			}
		}
		requestSize = routing.RequestSizeEstimate{Available: true, Tokens: wp.turnFacts.Source.BodyBytes, Basis: "wire_body_bytes"}
		aLegID = prep.aLegID
		weightedFirstConsumed = wp.weightedFirstConsumed
		recvViews = prep.recvViews
		recvViewsOK = prep.recvViewsOK
	} else {
		e.noteSelectorAuthority(ctx, prep.identity.traceID, prep.identity.routeAuth)
		var err error
		sel, err = e.prepareRouteSelector(
			prep.call.Route.Selector,
			prep.nativeResolver,
		)
		if err != nil {
			if errors.Is(err, aipapi.ErrUnresolvedModelOnlySelector) {
				return nil, fmt.Errorf("executor: %w", err)
			}
			return nil, fmt.Errorf("executor: parse route selector: %w", err)
		}
		requestSize = e.requestSizeEstimateForRouting(ctx, sel, *prep.call)
		failoverReq = capabilities.NewFailoverRequirementSet(*prep.call)
		aLegID = prep.identity.aLeg.ALegID
		weightedFirstConsumed = prep.identity.aLeg.WeightedFirstConsumed
		recvViews = prep.recvViews
		recvViewsOK = prep.recvViewsOK
	}

	affinityKey, affinityKeyOK, err := e.resolveAffinityKey(sel, recvViews, recvViewsOK)
	if err != nil {
		return nil, fmt.Errorf("executor: affinity identity: %w", err)
	}
	interleaved, err := e.loadInterleavedState(ctx, aLegID)
	if err != nil {
		return nil, fmt.Errorf("executor: load interleaved state: %w", err)
	}
	failures := &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
	budget := &attemptBudget{
		max:      e.effectiveMaxAttempts(),
		used:     0,
		failures: failures,
	}
	ttft := newTTFTBudget(e.now(), sel)
	sessionState := &routing.SessionRoutingState{FirstRequestConsumed: weightedFirstConsumed}
	excluded := map[string]struct{}{}
	rng := e.rng()
	progress := newRecoveryController(recoveryControllerInput{
		e:              e,
		affinityStore:  e.AffinityStore,
		log:            e.Log,
		streamRecovery: e.StreamRecovery,
		opener:         newReplacementOpener(e, prep.bus, prep.aScope),
		budget:         budget,
		ttft:           ttft,
		sel:            sel,
		requestSize:    requestSize,
		session:        sessionState,
		excluded:       excluded,
		rng:            rng,
		affinityKey:    affinityKey,
		affinitySet:    affinityKeyOK,
		interleaved:    interleaved,
	})
	rf := routeFacts{
		sel:         sel,
		requestSize: requestSize,
		affinityKey: affinityKey,
		affinitySet: affinityKeyOK,
		rng:         rng,
		failoverReq: failoverReq,
	}
	return &routePlanState{
		routeFacts: rf,
		progress:   progress,
	}, nil
}

func (p *routePlanState) facts() routeFacts {
	if p == nil {
		return routeFacts{}
	}
	return p.routeFacts
}

func (plan *routePlanState) ensureProgress(e *Executor, prep *preparedRequest) {
	if plan == nil || plan.progress != nil {
		return
	}
	plan.progress = newRecoveryController(recoveryControllerInput{
		e:              e,
		affinityStore:  e.AffinityStore,
		log:            e.Log,
		streamRecovery: e.StreamRecovery,
		opener:         newReplacementOpener(e, prep.bus, prep.aScope),
		budget:         &attemptBudget{max: e.effectiveMaxAttempts()},
		ttft:           newTTFTBudget(e.now(), plan.sel),
		sel:            plan.sel,
		requestSize:    plan.requestSize,
		session:        &routing.SessionRoutingState{FirstRequestConsumed: prep.identity.aLeg.WeightedFirstConsumed},
		excluded:       map[string]struct{}{},
		rng:            plan.rng,
		affinityKey:    plan.affinityKey,
		affinitySet:    plan.affinitySet,
	})
}
