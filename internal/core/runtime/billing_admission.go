package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

var (
	ErrBillingAdmissionDenied         = errors.New("executor: billing admission denied")
	ErrBillingCreditScreenDenied      = errors.New("executor: cheap credit screen denied")
	ErrBillingCreditScreenUnavailable = errors.New("executor: cheap credit screen unavailable")
)

type BillingCreditGate interface {
	Check(context.Context, string) error
}

type BillingRoutePlanInput struct {
	Call            aipapi.Call
	TraceID         string
	ALegID          string
	BillingCallID   string
	Route           *routing.Selector
	RequestSize     routing.RequestSizeEstimate
	Scope           scope.PrincipalScopeView
	SessionID       string
	MaxOutputTokens *int
	AccountID       string
	// StoreID is the frozen trusted durable-store identity of this request.
	// The binding owns the authoritative store identity, so a request that
	// names another store is refused at the admission boundary, before any
	// quote, provider call or durable financial side effect.
	StoreID string
	// RouteCaps carries the runtime-effective finite output bound for each
	// planned leaf occurrence, in the shared PlannedLeaves order. It is the
	// actual canonical request limit the attempt preflight resolves and the
	// backend is executed with; a missing/short list keeps the version-1
	// mapping.
	RouteCaps []BillingRouteOutputCap
}

// BillingRouteOutputCap is the finite output bound the attempt preflight has
// actually permitted for one planned route leaf. Present=false means no finite
// defensible bound exists (the reference quote then fails closed).
type BillingRouteOutputCap struct {
	Tokens  int64
	Present bool
}

type (
	BillingAdmissionInput    = BillingRoutePlanInput
	BillingExposureAdmission interface {
		Admit(context.Context, BillingExposureAdmissionInput) (billingport.CallExposure, error)
	}
)

type BillingExposureAdmissionInput struct {
	BillingAdmissionInput
	CallID string
}

// WireBillingCreditArgs carries bounded facts for the cheap pre-route credit screen (Req 15.6).
type WireBillingCreditArgs struct {
	Scope     scope.PrincipalScopeView
	AccountID string // optional explicit account ID override
}

// WireBillingExposureArgs carries bounded facts for post-quote atomic exposure admission (Req 15.6, 19).
type WireBillingExposureArgs struct {
	BillingCallID   usageevidence.BillingCallID
	TraceID         string
	ALegID          string
	SessionID       string
	Scope           scope.PrincipalScopeView
	AccountID       string
	Route           *routing.Selector
	RequestSize     routing.RequestSizeEstimate
	MaxOutputTokens *int
	RouteCaps       []BillingRouteOutputCap
}

// WireExposureAbortArgs carries bounded facts for recording an exposure abort closure (Req 15.6, 19).
type WireExposureAbortArgs struct {
	BillingCallID   usageevidence.BillingCallID
	SubmissionID    string
	Exposure        billingport.CallExposure
	ALegID          string
	SessionID       string
	ExpectedBLegIDs []string
	Now             time.Time
}

// WireBillingAssessmentArgs carries facts needed to assess billing compatibility under held permit (Req 15.6, 19).
type WireBillingAssessmentArgs struct {
	Scope scope.PrincipalScopeView
}

// billingWorkloadIdentity projects only trusted detached auxiliary lineage
// into the durable billing identity. Primary calls retain the legacy zero
// workload projection; an auxiliary role is accepted only after the detached
// policy has bounded it and the core billing mapper allowlists it.
func billingWorkloadIdentity(ctx context.Context) (usageevidence.WorkloadIdentity, error) {
	return billingWorkloadIdentityForALeg(ctx, diag.ALegID(ctx))
}

func billingWorkloadIdentityForALeg(ctx context.Context, aLegID string) (usageevidence.WorkloadIdentity, error) {
	if ctx == nil {
		return usageevidence.WorkloadIdentity{}, nil
	}
	meta, detached := execctx.DetachedSessionFromContext(ctx)
	sc, _ := scope.ScopeFromContext(ctx)
	if detached && strings.TrimSpace(meta.AuxiliaryRole) != "" {
		fact := metering.Fact{
			Lifecycle: metering.LifecycleAuxiliaryRequest,
			Scope:     sc,
		}
		return coremetering.ProjectWorkloadIdentity(fact, meta.AuxiliaryRole)
	}
	if st := requestAuthorityFrom(ctx); st != nil {
		if workload, ok := st.workloadForALeg(aLegID); ok {
			return workload, nil
		}
		if !st.Workload.IsZero() {
			return st.Workload, nil
		}
	}
	return usageevidence.WorkloadIdentity{}, nil
}

// (e *Executor).billingWorkloadIdentity is the terminal-path accessor. The
// request-authority carrier survives bare Recv contexts and takes precedence;
// direct context extraction remains useful for focused producer tests.
func (e *Executor) billingWorkloadIdentity(ctx context.Context) usageevidence.WorkloadIdentity {
	return e.billingWorkloadIdentityForALeg(ctx, diag.ALegID(ctx))
}

func (e *Executor) billingWorkloadIdentityForALeg(ctx context.Context, aLegID string) usageevidence.WorkloadIdentity {
	identity, err := billingWorkloadIdentityForALeg(ctx, aLegID)
	if err != nil {
		if e != nil && e.Log != nil {
			e.Log.DebugContext(ctx, "billing workload identity unavailable", "error", err)
		}
		return usageevidence.WorkloadIdentity{}
	}
	return identity
}

func (e *Executor) checkCreditScreen(ctx context.Context, accountID string) error {
	if e == nil || e.BillingCreditGate == nil {
		return nil
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("%w: %w: account identity is empty", ErrBillingAdmissionDenied, ErrBillingCreditScreenDenied)
	}
	if err := e.BillingCreditGate.Check(ctx, accountID); err != nil {
		class := ErrBillingCreditScreenUnavailable
		if errors.Is(err, billingport.ErrCreditScreenDenied) {
			class = ErrBillingCreditScreenDenied
		}
		return fmt.Errorf("%w: %w: %w", ErrBillingAdmissionDenied, class, err)
	}
	return nil
}

func (e *Executor) checkCheapCredit(ctx context.Context, prep *preparedRequest) error {
	if e == nil || e.BillingCreditGate == nil {
		return nil
	}
	if prep != nil && operatorSponsoredWork(ctx) {
		// The operator sponsors this leg: no customer money moves, so the
		// customer credit screen is not consulted.
		return nil
	}
	if prep == nil || e.BillingIdentity.AccountID == nil {
		return fmt.Errorf("%w: %w: account identity resolver is required", ErrBillingAdmissionDenied, ErrBillingCreditScreenDenied)
	}
	accountID := strings.TrimSpace(e.BillingIdentity.AccountID(ctx, *prep.call))
	return e.checkCreditScreen(ctx, accountID)
}

// wireBillingAccountID resolves the billing account from bounded wire facts using
// the exact fallback chain shared by the wire credit screen and exposure admission:
// explicit override, WireAccountID contract, WireBounded canonical callback on a zero
// Call under scoped context, then Scope.PrincipalID (Requirements 15.6, 19).
func (e *Executor) wireBillingAccountID(ctx context.Context, explicit string, sc scope.PrincipalScopeView) string {
	if e == nil {
		return strings.TrimSpace(explicit)
	}
	accountID := strings.TrimSpace(explicit)
	if accountID == "" && e.BillingIdentity.WireAccountID != nil {
		accountID = strings.TrimSpace(e.BillingIdentity.WireAccountID(ctx, sc))
	}
	if accountID == "" && e.BillingIdentity.WireBounded && e.BillingIdentity.AccountID != nil {
		scCtx := scope.WithScope(ctx, sc)
		accountID = strings.TrimSpace(e.BillingIdentity.AccountID(scCtx, aipapi.Call{}))
	}
	if accountID == "" && sc.PrincipalID.IsKnown() {
		accountID = strings.TrimSpace(sc.PrincipalID.String())
	}
	return accountID
}

// checkWireCheapCredit performs the cheap settled-credit screen on bounded wire facts
// without materializing or inspecting a aipapi.Call (Requirements 15.6, 19).
func (e *Executor) checkWireCheapCredit(ctx context.Context, args WireBillingCreditArgs) error {
	if e == nil || e.BillingCreditGate == nil {
		return nil
	}
	if e.BillingIdentity.HasCustomCallCallbacks() && strings.TrimSpace(args.AccountID) == "" {
		return fmt.Errorf("%w: %w: custom BillingIdentity Call callbacks cannot run on wire path", ErrBillingAdmissionDenied, ErrBillingCreditScreenDenied)
	}
	accountID := e.wireBillingAccountID(ctx, args.AccountID, args.Scope)
	return e.checkCreditScreen(ctx, accountID)
}

func (e *Executor) authorizeBillingOnce(ctx context.Context, prep *preparedRequest, plan *routePlanState) error {
	if e == nil || e.BillingExposureAdmission == nil {
		return nil
	}
	if prep != nil && operatorSponsoredWork(ctx) {
		// The operator sponsors this leg: the provider call is metered, but no
		// customer exposure is admitted, so no customer account is resolved,
		// frozen or settled.
		return nil
	}
	if prep == nil || plan == nil {
		return fmt.Errorf("%w: missing prepared route plan", ErrBillingAdmissionDenied)
	}
	if err := prep.billingCallID.Validate(); err != nil {
		return fmt.Errorf("%w: billing call identity: %v", ErrBillingAdmissionDenied, err)
	}
	// Freeze the trusted StoreID before operational exposure is admitted, so a
	// panicking resolver fails closed with zero admissions and no abort owed.
	// The once-only flag makes this a no-op for prepared requests already
	// stamped in prepareRequest; it runs on the same admission ctx, preserving
	// callback context facts and timing trust.
	if err := e.stampBillingStoreID(ctx, prep); err != nil {
		return fmt.Errorf("%w: trusted billing store identity: %w", ErrBillingAdmissionDenied, err)
	}
	fallback, err := safety.CallValue(safety.BoundaryBackend, "billing_exposure_identity", func() (billingport.CallExposure, error) {
		var identity billingport.CallExposure
		if e.BillingIdentity.AccountID != nil {
			identity.AccountID = e.BillingIdentity.AccountID(ctx, *prep.call)
		}
		if e.BillingIdentity.CustomerPricingRef != nil {
			identity.PricingRef = e.BillingIdentity.CustomerPricingRef(ctx, *prep.call)
		}
		if e.BillingIdentity.ChargePolicyRef != nil {
			identity.ChargePolicyRef = e.BillingIdentity.ChargePolicyRef(ctx, *prep.call)
		}
		return identity, nil
	})
	if err != nil {
		return fmt.Errorf("%w: billing exposure identity: %w", ErrBillingAdmissionDenied, err)
	}
	exposure, err := e.BillingExposureAdmission.Admit(ctx, BillingExposureAdmissionInput{
		BillingAdmissionInput: e.billingRoutePlanInput(ctx, prep, plan, fallback.AccountID), CallID: prep.billingCallID.String(),
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBillingAdmissionDenied, err)
	}
	e.stampExposureIdentity(ctx, prep, exposure, fallback)
	return nil
}

func resolveExposureIdentity(exposure billingport.CallExposure, fallbackAccountID string, fallbackPricing usageevidence.VersionRef, fallbackPolicy usageevidence.VersionRef) (billingport.CallExposure, bool) {
	accountID := strings.TrimSpace(exposure.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(fallbackAccountID)
	}
	pricing := exposure.PricingRef
	if pricing == (usageevidence.VersionRef{}) {
		pricing = fallbackPricing
	}
	policy := exposure.ChargePolicyRef
	if policy == (usageevidence.VersionRef{}) {
		policy = fallbackPolicy
	}
	if accountID == "" {
		return billingport.CallExposure{}, false
	}
	return billingport.CallExposure{
		AccountID:       accountID,
		PricingRef:      pricing,
		ChargePolicyRef: policy,
	}, true
}

// stampExposureIdentity only projects already-resolved values: no configurable
// callback may run between exposure admission and installation of its abort owner.
func (e *Executor) stampExposureIdentity(ctx context.Context, prep *preparedRequest, exposure, fallback billingport.CallExposure) {
	if e == nil || prep == nil {
		return
	}
	resolved, ok := resolveExposureIdentity(exposure, fallback.AccountID, fallback.PricingRef, fallback.ChargePolicyRef)
	if !ok {
		return
	}
	prep.billingExposure = resolved
	prep.billingIdentityStamped = true
	prep.billingAccountID = resolved.AccountID
	prep.billingCustomerPricing = resolved.PricingRef
	prep.billingChargePolicy = resolved.ChargePolicyRef
	// Freeze the trusted customer scope on the per-call billing state while
	// the admission context is live, so terminal handoffs on detached
	// contexts still observe it.
	if sc, ok := scope.ScopeFromContext(ctx); ok {
		prep.billingCallState.freezeScope(sc)
	}
}

// AuthorizeWireBilling admits operational exposure from bounded wire facts post-quote
// without constructing or inspecting a aipapi.Call (Requirements 15.6, 19).
func (e *Executor) AuthorizeWireBilling(ctx context.Context, args WireBillingExposureArgs) (billingport.CallExposure, error) {
	if e == nil {
		return billingport.CallExposure{}, nil
	}
	if err := args.BillingCallID.Validate(); err != nil {
		return billingport.CallExposure{}, fmt.Errorf("%w: billing call identity: %v", ErrBillingAdmissionDenied, err)
	}
	if e.BillingIdentity.HasCustomCallCallbacks() {
		return billingport.CallExposure{}, fmt.Errorf("%w: custom BillingIdentity Call callbacks cannot run on wire path", ErrBillingAdmissionDenied)
	}
	// When BillingExposureAdmission is nil, no admission check is performed and
	// an empty CallExposure is returned. This matches canonical authorizeBillingOnce
	// behavior (lines 206-208), which returns nil without calling stampExposureIdentity,
	// leaving billingIdentityStamped = false and billingExposure empty on both paths.
	if e.BillingExposureAdmission == nil {
		return billingport.CallExposure{}, nil
	}

	// Resolve every fallible identity callback before the admission side effect.
	// The admitted exposure still takes precedence over these frozen fallbacks.
	fallback, err := safety.CallValue(safety.BoundaryBackend, "wire_billing_exposure_identity", func() (billingport.CallExposure, error) {
		identity := billingport.CallExposure{AccountID: e.wireBillingAccountID(ctx, args.AccountID, args.Scope)}
		if e.BillingIdentity.WireCustomerPricingRef != nil {
			identity.PricingRef = e.BillingIdentity.WireCustomerPricingRef(ctx)
		} else if e.BillingIdentity.WireBounded && e.BillingIdentity.CustomerPricingRef != nil {
			identity.PricingRef = e.BillingIdentity.CustomerPricingRef(ctx, aipapi.Call{})
		}
		if e.BillingIdentity.WireChargePolicyRef != nil {
			identity.ChargePolicyRef = e.BillingIdentity.WireChargePolicyRef(ctx)
		} else if e.BillingIdentity.WireBounded && e.BillingIdentity.ChargePolicyRef != nil {
			identity.ChargePolicyRef = e.BillingIdentity.ChargePolicyRef(ctx, aipapi.Call{})
		}
		return identity, nil
	})
	if err != nil {
		return billingport.CallExposure{}, fmt.Errorf("%w: billing exposure identity: %w", ErrBillingAdmissionDenied, err)
	}

	callIDStr := args.BillingCallID.String()
	admitInput := BillingExposureAdmissionInput{
		BillingAdmissionInput: BillingAdmissionInput{
			TraceID:         args.TraceID,
			ALegID:          args.ALegID,
			BillingCallID:   callIDStr,
			Route:           args.Route,
			RequestSize:     args.RequestSize,
			Scope:           args.Scope,
			SessionID:       args.SessionID,
			MaxOutputTokens: args.MaxOutputTokens,
			RouteCaps:       append([]BillingRouteOutputCap(nil), args.RouteCaps...),
			AccountID:       fallback.AccountID,
		},
		CallID: callIDStr,
	}

	exposure, err := e.BillingExposureAdmission.Admit(ctx, admitInput)
	if err != nil {
		return billingport.CallExposure{}, fmt.Errorf("%w: %w", ErrBillingAdmissionDenied, err)
	}

	stamped, _ := resolveExposureIdentity(exposure, fallback.AccountID, fallback.PricingRef, fallback.ChargePolicyRef)
	return stamped, nil
}

// frozenPrepScope resolves the frozen trusted request scope: the
// identity-bound scope first, the request context scope as fallback. It
// carries values only and never invents identity.
func frozenPrepScope(ctx context.Context, prep *preparedRequest) scope.PrincipalScopeView {
	if prep != nil && prep.identity != nil && prep.identity.hasPrincipal {
		return prep.identity.scope
	}
	if sc, ok := scope.ScopeFromContext(ctx); ok {
		return sc
	}
	return scope.PrincipalScopeView{}
}

// withTerminalScope carries frozen trusted scope onto a terminal handoff
// context without touching cancellation or deadlines. The frozen admission
// scope is authoritative for monetary terminalization: when present it is
// carried regardless of absent or conflicting live context, and a live
// context may never replace it. Fields are never merged across principals.
// Without frozen scope the live context passes through unchanged, which is
// valid only for non-admitted and pre-freeze paths; admitted monetary paths
// fail closed downstream when scope is still missing.
func withTerminalScope(ctx context.Context, frozen scope.PrincipalScopeView) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if frozen.PrincipalID.IsKnown() {
		return scope.WithScope(ctx, frozen)
	}
	return ctx
}

func (e *Executor) billingRoutePlanInput(ctx context.Context, prep *preparedRequest, plan *routePlanState, frozenAccountID string) BillingRoutePlanInput {
	if prep == nil || plan == nil {
		return BillingRoutePlanInput{}
	}
	sessID := ""
	if prep.call != nil {
		sessID = prep.call.Session.AuthoritativeSessionID
	}
	var maxOut *int
	if prep.call != nil && prep.call.Options.MaxOutputTokens != nil {
		v := *prep.call.Options.MaxOutputTokens
		maxOut = &v
	}
	// The frozen trusted request scope rides the admission input exactly
	// like the wire path carries it: identity-bound scope first, request
	// context scope as the fallback. The credited account follows the
	// shared wire fallback chain so canonical and wire admission agree.
	sc := frozenPrepScope(ctx, prep)
	return BillingRoutePlanInput{
		Call:            aipapi.CloneCall(*prep.call),
		TraceID:         prep.identity.traceID,
		ALegID:          prep.identity.aLeg.ALegID,
		BillingCallID:   prep.billingCallID.String(),
		Route:           plan.sel,
		RequestSize:     e.billingRequestSize(ctx, prep, plan),
		Scope:           sc,
		SessionID:       sessID,
		MaxOutputTokens: maxOut,
		AccountID:       e.billingRoutePlanAccountID(ctx, sc, frozenAccountID),
		StoreID:         strings.TrimSpace(prep.billingStoreID),
		RouteCaps:       e.billingRouteOutputCaps(ctx, prep, plan),
	}
}

// billingRoutePlanAccountID freezes the credited account for the canonical
// admission.
//
// A custom identity (call-shaped callbacks that are not wire-bounded) owns
// the account: it resolved once at the identity boundary and its exact value
// is carried here unchanged, so the route plan never recomputes it and never
// substitutes the known principal. A stock wire-bounded identity keeps the
// shared bounded fallback chain unchanged.
func (e *Executor) billingRoutePlanAccountID(ctx context.Context, sc scope.PrincipalScopeView, frozenAccountID string) string {
	if e != nil && e.BillingIdentity.HasCustomCallCallbacks() {
		return strings.TrimSpace(frozenAccountID)
	}
	return e.wireBillingAccountID(ctx, "", sc)
}

// billingRouteOutputCaps resolves the actual finite output bound the attempt
// preflight permits for every planned leaf occurrence, in the shared leaf
// traversal order. It reuses the same preflight authority and model facts the
// attempt itself uses, so the quote can never assume an enforcement the
// canonical request does not carry: an explicit client max, an enforced
// clamp/default, and the model/backend maximum all resolve through
// accountingpreflight exactly as they do for the executed request.
func (e *Executor) billingRouteOutputCaps(ctx context.Context, prep *preparedRequest, plan *routePlanState) []BillingRouteOutputCap {
	if e == nil || prep == nil || prep.call == nil || plan == nil {
		return nil
	}
	leaves := routing.PlannedLeaves(plan.sel)
	if len(leaves) == 0 {
		return nil
	}
	caps := make([]BillingRouteOutputCap, 0, len(leaves))
	for _, leaf := range leaves {
		caps = append(caps, e.effectiveRouteOutputCap(ctx, prep, leaf))
	}
	return caps
}

// effectiveRouteOutputCap resolves one leaf's permitted output bound exactly
// as the attempt open path resolves the executed request: the rederive
// preflight adjusts the base call on the pre-merge call, then the leaf route
// generation query is merged and wins (backendCallWithRouteParams). The merged
// call's bound is the actual wire authority; the preflight decision output is a
// last resort for custom counters, and no adjusted/merged/decision bound means
// this occurrence has no defensible finite bound.
func (e *Executor) effectiveRouteOutputCap(ctx context.Context, prep *preparedRequest, leaf routing.PlannedLeaf) BillingRouteOutputCap {
	candidate := routing.AttemptCandidate{Primary: routing.Primary{
		Backend: leaf.Backend, Model: leaf.Model, Params: leaf.Params,
	}}
	requested := prep.call.Options.MaxOutputTokens
	preflightEnabled := e.Preflight != nil && e.Preflight.Enabled()
	var decision accountingpreflight.Decision
	if preflightEnabled {
		var facts modelcatalog.ModelFacts
		if backend, ok := e.Backends[strings.TrimSpace(leaf.Backend)]; ok {
			facts = e.effectiveFactsForAttempt(ctx, backend, *prep.call, candidate).Facts
		} else if e.CatalogResolver != nil {
			facts = e.CatalogResolver.Resolve(ctx, candidate, *prep.call, aipapi.BackendCaps{}).Facts
		}
		decision = e.Preflight.Check(ctx, accountingpreflight.Input{
			Backend:                  leaf.Backend,
			Model:                    leaf.Model,
			CallID:                   prep.call.ID,
			Call:                     *prep.call,
			RequestedMaxOutputTokens: requested,
			Facts:                    facts,
		})
	}
	// Mirror the attempt open order: adjust the base call with the preflight
	// decision, then merge the leaf route query, which overrides the client
	// value. The base call is a value copy; the shared selector params are
	// read-only and never mutated.
	attempt := *prep.call
	if preflightEnabled && decision.AdjustedMaxOutputTokens != nil && *decision.AdjustedMaxOutputTokens > 0 {
		adjusted := *decision.AdjustedMaxOutputTokens
		attempt.Options.MaxOutputTokens = &adjusted
	}
	merged, err := backendCallWithRouteParams(attempt, candidate)
	if err != nil {
		return BillingRouteOutputCap{}
	}
	if merged.Options.MaxOutputTokens != nil {
		// An explicit merged value is the authoritative wire limit. Zero (an
		// explicit route clear) or any non-positive value means the executed
		// request has no positive finite output cap: it is resolved unbounded
		// and must never borrow the superseded pre-merge count.
		if value := *merged.Options.MaxOutputTokens; value > 0 {
			return BillingRouteOutputCap{Tokens: int64(value), Present: true}
		}
		return BillingRouteOutputCap{}
	}
	if preflightEnabled && decision.Count.OutputTokens > 0 {
		return BillingRouteOutputCap{Tokens: int64(decision.Count.OutputTokens), Present: true}
	}
	return BillingRouteOutputCap{}
}

func (e *Executor) billingRequestSize(ctx context.Context, prep *preparedRequest, plan *routePlanState) routing.RequestSizeEstimate {
	if plan != nil && plan.requestSize.Available {
		return plan.requestSize
	}
	if e == nil || prep == nil || e.RequestTokenEstimator == nil {
		return routing.RequestSizeEstimate{}
	}
	est := e.RequestTokenEstimator.EstimateRequestTokens(ctx, *prep.call)
	return routing.RequestSizeEstimate{Available: est.Available, Tokens: est.Input, Basis: est.Basis}
}

// exposureAbort is the single exit for an admitted operational exposure when
// the call fails before a terminal owner takes it: Abort records the
// exposure-abort closure once. Canonical and wire execution both create it at
// admission. A nil exposureAbort means no exposure was admitted.
type exposureAbort struct {
	e    *Executor
	args WireExposureAbortArgs
	// scope, when set, rides the closure on contexts that may be detached.
	scope *scope.PrincipalScopeView
	state *billingCallState
	done  bool
}

// Abort records the exposure-abort closure with the B-legs allocated so far.
// It is idempotent.
func (a *exposureAbort) Abort(ctx context.Context) {
	if a == nil || a.done {
		return
	}
	a.done = true
	args := a.args
	if a.state != nil {
		args.ExpectedBLegIDs = a.state.freezeAllocatedBLegs()
	}
	args.Now = a.e.now()
	if a.scope != nil {
		ctx = withTerminalScope(ctx, *a.scope)
	}
	_ = a.e.appendAbortClosureRecord(ctx, args)
}

// canonicalExposureAbort returns the abort exit for a canonical call whose
// exposure admission stamped a billing identity.
func (e *Executor) canonicalExposureAbort(ctx context.Context, prep *preparedRequest) *exposureAbort {
	if e == nil || prep == nil || e.BillingExposureAdmission == nil || !e.hasTerminalCallSink() || !prep.billingIdentityStamped {
		return nil
	}
	sessID := ""
	if prep.call != nil {
		sessID = prep.call.Session.AuthoritativeSessionID
	}
	sc := frozenPrepScope(ctx, prep)
	return &exposureAbort{
		e: e,
		args: WireExposureAbortArgs{
			BillingCallID: prep.billingCallID,
			SubmissionID:  submissionIDForBilling(prep.submission),
			Exposure:      prep.billingExposure,
			ALegID:        strings.TrimSpace(prep.identity.aLeg.ALegID),
			SessionID:     sessID,
		},
		scope: &sc,
		state: prep.billingCallState,
	}
}

// wireExposureAbort returns the abort exit for a wire call whose exposure
// admission resolved a billing account.
func (e *Executor) wireExposureAbort(exposure billingport.CallExposure, callID usageevidence.BillingCallID, aLegID, sessionID string, state *billingCallState) *exposureAbort {
	if e == nil || exposure.AccountID == "" {
		return nil
	}
	return &exposureAbort{
		e: e,
		args: WireExposureAbortArgs{
			BillingCallID: callID,
			Exposure:      exposure,
			ALegID:        aLegID,
			SessionID:     sessionID,
		},
		state: state,
	}
}

// appendAbortClosureRecord is the exact shared helper for recording an exposure abort
// closure to the terminal usage sink, shared between canonical and wire execution (Requirements 15.6, 19).
func (e *Executor) appendAbortClosureRecord(ctx context.Context, args WireExposureAbortArgs) error {
	if e == nil || !e.hasTerminalCallSink() {
		return nil
	}
	if err := args.BillingCallID.Validate(); err != nil {
		return err
	}
	accountID := strings.TrimSpace(args.Exposure.AccountID)
	if accountID == "" {
		return nil
	}
	now := args.Now
	if now.IsZero() {
		now = e.now()
	}
	record := usageevidence.CallUsageRecord{
		SchemaVersion:      usageevidence.CurrentRecordSchemaVersion,
		CallID:             args.BillingCallID,
		SubmissionID:       strings.TrimSpace(args.SubmissionID),
		AccountID:          accountID,
		ALegID:             strings.TrimSpace(args.ALegID),
		SessionID:          strings.TrimSpace(args.SessionID),
		StartedAt:          now,
		FinishedAt:         now,
		Outcome:            usageevidence.TurnOutcomeFailed,
		CustomerPricingRef: args.Exposure.PricingRef,
		ChargePolicyRef:    args.Exposure.ChargePolicyRef,
		ExpectedBLegIDs:    args.ExpectedBLegIDs,
		Workload:           e.billingWorkloadIdentityForALeg(ctx, args.ALegID),
	}
	sealed, err := record.Seal()
	if err != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "billing exposure abort closure seal failed", "error", err)
		}
		return err
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	if err := safety.Call(safety.BoundaryStream, "billing_exposure_abort_closure", func() error {
		return e.TerminalUsageSink.AppendCall(persistCtx, sealed)
	}); err != nil {
		e.logBillingUsageAppendFailure(persistCtx, "billing_call_closure_append_critical", "billing exposure abort closure append failed", err)
		return err
	}
	return nil
}

// appendWireExposureAbort records an exposure abort closure for a failed wire attempt
// sharing exact record sealing and sink handoff logic with canonical aborts (Requirements 15.6, 19).
func (e *Executor) appendWireExposureAbort(ctx context.Context, args WireExposureAbortArgs) error {
	return e.appendAbortClosureRecord(ctx, args)
}

// assessWireBilling evaluates billing eligibility under the held decode permit (Requirements 15.6, 19).
// It accepts whenever no custom BillingIdentity Call callbacks block the wire path,
// regardless of whether credit/exposure gates are configured (unconfigured gates
// accept the same way on the canonical path).
// If custom BillingIdentity Call callbacks exist without a wire-bounded contract,
// it declines with (AssessmentDecisionDecline, DeclineReasonAuthorityBlocker).
func (e *Executor) assessWireBilling(
	ctx context.Context,
	args WireBillingAssessmentArgs,
) (largebody.AssessmentDecision, largebody.DeclineReason, error) {
	if e == nil {
		return largebody.AssessmentDecisionAccept, largebody.DeclineReasonNone, nil
	}

	// Custom Call-shaped callbacks are blockers unless certified as wire-bounded (Req 15.6).
	if e.BillingIdentity.HasCustomCallCallbacks() {
		return largebody.AssessmentDecisionDecline, largebody.DeclineReasonAuthorityBlocker,
			fmt.Errorf("%w: custom BillingIdentity Call callbacks cannot run on wire path", ErrBillingAdmissionDenied)
	}

	return largebody.AssessmentDecisionAccept, largebody.DeclineReasonNone, nil
}
