package billingbinding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	runtimecore "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkbilling "github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

var (
	// ErrInvalidBinding identifies a binding the adapter refuses to compose.
	ErrInvalidBinding = errors.New("billingbinding: invalid binding")
	// ErrCreditScreen identifies a failed cheap credit screen translation.
	ErrCreditScreen = errors.New("billingbinding: credit screen failed")
	// ErrAdmission identifies a failed quote/admission translation.
	ErrAdmission = errors.New("billingbinding: exposure admission failed")
	// ErrTerminal identifies a failed terminal handoff translation.
	ErrTerminal = errors.New("billingbinding: terminal handoff failed")
	// ErrLifecycle identifies a binding-owned resource lifecycle failure.
	ErrLifecycle = errors.New("billingbinding: lifecycle failed")
)

const (
	// quoteInputVersion is the public quote contract version used for
	// admission-time quotes.
	quoteInputVersion = 1
	// limitInputTokens bounds provider-bound input size for one admission.
	limitInputTokens = "input_tokens"
	// limitMaxOutputTokens bounds provider output for one admission.
	limitMaxOutputTokens = "max_output_tokens"
	// limitUnitToken is the exact-integer token unit for admission limits.
	limitUnitToken = "token"
)

// Adapter binds one validated public billing.Binding to the internal runtime
// billing chokepoints. It is safe for concurrent use by the executor. The
// optional sink/quoter declarations are negotiated exactly once at
// construction and never change afterwards.
type Adapter struct {
	binding   sdkbilling.Binding
	scope     sdkbilling.BindingScope
	lifecycle sdkbilling.Lifecycle
	// carriesRecords is set when the terminal sink declares RecordCarrier; the
	// adapter then emits version-2 envelopes carrying the exact sealed record.
	carriesRecords bool
	// carriesRouteTariffs is set when the quoter declares RouteTariffCarrier;
	// the adapter then carries planned routes and requires the quote mode.
	carriesRouteTariffs bool
	// referenceFacts is the optional quoter refinement supplying the frozen
	// customer-pricing, charge-policy, and per-route operator-rate references.
	// Nil keeps the version-1 scope mapping.
	referenceFacts economics.ReferenceFacts
	// identity is the optional owner-supplied request-identity resolver. It is
	// non-nil only when the binding declared one; the adapter never
	// synthesizes a resolver and never replaces a supplied callback.
	identity *sdkbilling.IdentityResolver
	// preflight is the optional pre-quote admission refinement supplied by
	// the binding's admission port. Nil keeps the exact pre-quote behavior of
	// a binding whose admission port does not opt in.
	preflight sdkbilling.ExposureAdmissionPreflighter
	// backendKinds is the immutable generation-resolved instance-to-factory-kind
	// inventory used to stamp the planned-route facts of an admission. It is
	// metadata carriage only: the adapter never applies a policy on it. Nil
	// keeps an admission without route facts.
	backendKinds map[string]string

	// state is the shared owned-resource lifecycle state. A generation-local
	// adapter copy shares it with the process adapter, so one process owns
	// exactly one set of binding-owned resources.
	state *adapterState
}

// adapterState is the shared mutable lifecycle state of the adapter copies.
type adapterState struct {
	mu        sync.Mutex
	started   bool
	closeOnce sync.Once
}

var (
	_ runtimecore.BillingCreditGate        = (*Adapter)(nil)
	_ runtimecore.BillingExposureAdmission = (*Adapter)(nil)
	_ billingport.TerminalUsageSink        = (*Adapter)(nil)
)

// NewAdapter validates the binding before publication. It rejects
// invalid, typed-nil, and incomplete bindings; duplicate monetary bindings
// are rejected by sdkbilling.ValidateBindings at the facade. After validation
// the optional carrier declarations are probed once.
func NewAdapter(binding sdkbilling.Binding) (*Adapter, error) {
	if err := binding.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidBinding, err)
	}
	adapter := &Adapter{
		binding:   binding,
		scope:     binding.Scope.Clone(),
		lifecycle: binding.Lifecycle.Clone(),
		state:     &adapterState{},
	}
	// binding.Validate rejects typed-nil ports, so these probes cannot observe
	// a boxed nil implementation.
	if _, ok := binding.Terminal.(sdkbilling.RecordCarrier); ok {
		adapter.carriesRecords = true
	}
	if _, ok := binding.Quoter.(economics.RouteTariffCarrier); ok {
		adapter.carriesRouteTariffs = true
	}
	if facts, ok := binding.Quoter.(economics.ReferenceFacts); ok {
		adapter.referenceFacts = facts
	}
	// The optional pre-quote admission refinement is carried by the binding's
	// admission port. A binding whose admission port does not implement it
	// keeps the exact pre-quote behavior of the host.
	if preflight, ok := binding.Admission.(sdkbilling.ExposureAdmissionPreflighter); ok {
		adapter.preflight = preflight
	}
	// The owner-supplied identity resolver is captured unchanged. The binding
	// validation already refused a typed-nil resolver and a resolver without
	// an account callback, so this probe cannot observe either.
	adapter.identity = binding.IdentityResolver
	return adapter, nil
}

// Chokepoints exposes the adapter as the three runtime billing chokepoints
// plus the billing identity. The host composition assigns them onto the same
// production ports as the internal reference composition; the adapter never
// invents an internal store.
func (a *Adapter) Chokepoints() (runtimecore.BillingCreditGate, runtimecore.BillingExposureAdmission, billingport.TerminalUsageSink, runtimecore.BillingIdentity) {
	if a == nil {
		return nil, nil, nil, runtimecore.BillingIdentity{}
	}
	return a, a, a, a.Identity()
}

// TerminalSink exposes the wrapped binding's public terminal port for
// process-level composition probes. The open-source adapter returns the SDK
// interface value unchanged and never interprets a money-domain type; the
// temporary runtimebundle billing admin mount (tasks 4.2/5.1) uses this only
// to discover the binding-owned durable reporting reader that feeds the
// original process economic-health collector. Removed together with that
// mount in task 5.4.
func (a *Adapter) TerminalSink() sdkbilling.TerminalSink {
	if a == nil {
		return nil
	}
	return a.binding.Terminal
}

// MaintenanceObserver exposes the wrapped binding's optional provider
// maintenance attachment (nil when the binding declares none). The adapter is
// the single binding-translation surface: BuildWithBilling installs the
// returned observer on the existing production maintenance port unchanged.
func (a *Adapter) MaintenanceObserver() sdkbilling.MaintenanceObserver {
	if a == nil {
		return nil
	}
	return a.binding.Maintenance
}

// scopeOf resolves the trusted customer scope from the request context.
func scopeOf(ctx context.Context) (scope.PrincipalScopeView, bool) {
	sc, ok := scope.ScopeFromContext(ctx)
	if !ok || !sc.PrincipalID.IsKnown() {
		return scope.PrincipalScopeView{}, false
	}
	return sc, true
}

// accountOf resolves the billing account from trusted scope only. It never
// inspects provider-shaped request data; an unknown principal fails closed.
func accountOf(sc scope.PrincipalScopeView) string {
	if sc.PrincipalID.IsKnown() {
		return strings.TrimSpace(sc.PrincipalID.String())
	}
	return ""
}

// Check implements the cheap pre-route credit screen by translating the
// account identity into a public credit-screen input. Unknown scope fails
// closed without calling the binding. A deny maps onto the internal denied
// class so the executor classifies it exactly like the reference path. The
// binary gate cannot carry degraded guarantees, so degraded fails closed
// through the existing unavailable class before provider execution; it is
// never ordinary allow. Any other failure stays unavailable.
func (a *Adapter) Check(ctx context.Context, accountID string) error {
	if a == nil {
		return fmt.Errorf("%w: nil adapter", ErrCreditScreen)
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("%w: account identity is required", ErrCreditScreen)
	}
	sc, ok := scopeOf(ctx)
	if !ok {
		return fmt.Errorf("%w: trusted customer scope is required", ErrCreditScreen)
	}
	in := sdkbilling.CreditScreenInput{
		Scope:     sc,
		StoreID:   a.scope.StoreID,
		AccountID: accountID,
		Policy:    a.scope.Policy,
	}
	if err := in.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrCreditScreen, err)
	}
	result, err := a.binding.CreditScreen.Check(ctx, in)
	if err != nil {
		return fmt.Errorf("%w: binding screen: %v", ErrCreditScreen, err)
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("%w: invalid screen result: %v", ErrCreditScreen, err)
	}
	// A foreign result is never a decision for the requested account,
	// whatever its allow/deny value.
	if !result.MatchesInput(in) {
		return fmt.Errorf("%w: result store %q account %q for request %q/%q: %w",
			ErrCreditScreen, result.StoreID, result.AccountID, in.StoreID, in.AccountID,
			sdkbilling.ErrCreditResultMismatch)
	}
	switch result.Decision {
	case sdkbilling.CreditAllow:
		return nil
	case sdkbilling.CreditDegraded:
		return fmt.Errorf("%w: binding degraded account %q: %w", ErrCreditScreen, accountID, billingport.ErrCreditScreenUnavailable)
	case sdkbilling.CreditDeny:
		return fmt.Errorf("%w: binding denied account %q", billingport.ErrCreditScreenDenied, accountID)
	default:
		return fmt.Errorf("%w: unknown decision %q", ErrCreditScreen, result.Decision)
	}
}

// Admit implements atomic exposure admission by quoting the frozen offer
// through the binding quoter and admitting the frozen quote through the
// binding admission port. It exercises the same runtime admission path as
// the reference composition: the executor calls Admit once per route plan.
func (a *Adapter) Admit(ctx context.Context, in runtimecore.BillingExposureAdmissionInput) (billingport.CallExposure, error) {
	if a == nil {
		return billingport.CallExposure{}, fmt.Errorf("%w: nil adapter", ErrAdmission)
	}
	subject := metering.SubjectRef{
		Kind:          metering.SubjectBillingCall,
		StoreID:       a.scope.StoreID,
		BillingCallID: strings.TrimSpace(in.BillingCallID),
	}
	if err := subject.Validate(); err != nil {
		return billingport.CallExposure{}, fmt.Errorf("%w: subject: %v", ErrAdmission, err)
	}
	// The trusted admission scope and credited account gate everything
	// downstream: no quote or admission port runs without customer identity.
	// A context scope that names a different principal than the trusted
	// input scope is a foreign mixing and fails here as well.
	if !in.Scope.PrincipalID.IsKnown() {
		return billingport.CallExposure{}, fmt.Errorf("%w: trusted admission scope with known principal required", ErrAdmission)
	}
	if ctxScope, ok := scopeOf(ctx); ok && ctxScope.PrincipalID.String() != in.Scope.PrincipalID.String() {
		return billingport.CallExposure{}, fmt.Errorf("%w: admission scope principal %q differs from context principal %q", ErrAdmission, in.Scope.PrincipalID.String(), ctxScope.PrincipalID.String())
	}
	// The binding owns the authoritative store identity. A request whose
	// trusted store identity names another store is refused here, before the
	// quote, the admission port, the provider and every durable financial
	// side effect.
	if requestStore := strings.TrimSpace(in.StoreID); requestStore != "" && requestStore != a.scope.StoreID {
		return billingport.CallExposure{}, fmt.Errorf("%w: request store %q differs from the binding store %q: %w",
			ErrAdmission, requestStore, a.scope.StoreID, sdkbilling.ErrStoreMismatch)
	}
	accountID := strings.TrimSpace(in.AccountID)
	// A supplied identity resolver owns the credited account. Its exact
	// resolved value is authoritative and is never replaced by a
	// principal-derived account: an empty result stays missing and fails
	// closed here, before any quote or admission side effect.
	if accountID == "" && a.identity == nil {
		accountID = accountOf(in.Scope)
	}
	if accountID == "" && a.identity == nil {
		if sc, ok := scopeOf(ctx); ok {
			accountID = accountOf(sc)
		}
	}
	if accountID == "" {
		return billingport.CallExposure{}, fmt.Errorf("%w: billing account is required", ErrAdmission)
	}
	limits, err := admissionLimits(in)
	if err != nil {
		return billingport.CallExposure{}, err
	}
	quoteIn := economics.QuoteInput{
		Version:         quoteInputVersion,
		Subject:         subject,
		CandidateLimits: limits,
		Tariff:          a.scope.Tariff,
		Policy:          a.scope.Policy,
	}
	// Only a declaring quoter receives the ordered planned-leaf occurrence
	// list; a version-1 quoter keeps today's exact request mapping.
	if a.carriesRouteTariffs {
		quoteIn.Routes = plannedRouteCandidates(in.Route, in.RouteCaps)
	}
	if err := quoteIn.Validate(); err != nil {
		return billingport.CallExposure{}, fmt.Errorf("%w: quote input: %v", ErrAdmission, err)
	}
	// The ordered planned-route facts are the only facts the host can offer
	// before it prices anything. The optional preflight refinement runs after
	// the trusted-input checks above and before the quote, so a refusal costs
	// no price, no exposure write, and no provider open.
	routes := a.admissionRouteFacts(in.Route)
	if a.preflight != nil {
		preflightIn := sdkbilling.ExposureAdmissionPreflightInput{Routes: routes}
		if err := preflightIn.Validate(); err != nil {
			return billingport.CallExposure{}, fmt.Errorf("%w: admission preflight input: %v", ErrAdmission, err)
		}
		if err := a.preflight.PreflightExposureAdmission(ctx, preflightIn); err != nil {
			// %w keeps the binding-refused class (for example
			// billing.ErrEstimateInvalid) in the chain next to the adapter
			// identity.
			return billingport.CallExposure{}, fmt.Errorf("%w: admission preflight: %w", ErrAdmission, err)
		}
	}
	quote, err := a.binding.Quoter.Quote(ctx, quoteIn)
	if err != nil {
		return billingport.CallExposure{}, fmt.Errorf("%w: binding quote: %v", ErrAdmission, err)
	}
	if err := quote.Validate(); err != nil {
		// A declaring carrier's frozen mode with an entirely missing binding
		// list is simultaneously an invalid quote (the SDK rejects it) and the
		// typed quote mismatch required for missing frozen coverage. Preserve
		// every class for this one failure — admission, the SDK invalid-quote
		// cause, and the mismatch — so each consumer's errors.Is probe keeps
		// working. A non-declaring quoter and every other invalid quote keep
		// the unchanged generic validation error.
		if a.carriesRouteTariffs && quote.RouteTariffMode == economics.RouteTariffModeFrozen && len(quote.RouteTariffs) == 0 {
			return billingport.CallExposure{}, fmt.Errorf("%w: invalid quote: %w: %w", ErrAdmission, err, sdkbilling.ErrQuoteMismatch)
		}
		return billingport.CallExposure{}, fmt.Errorf("%w: invalid quote: %v", ErrAdmission, err)
	}
	// The quote must bind the requested call and the binding's frozen
	// snapshots before atomic admission is invoked. Foreign subjects or
	// snapshots fail here, with no admission port call.
	if err := a.checkQuoteBinding(quote, subject); err != nil {
		return billingport.CallExposure{}, err
	}
	routeTariffs, err := admittedRouteTariffs(quote, in.Route, a.carriesRouteTariffs)
	if err != nil {
		return billingport.CallExposure{}, err
	}
	// The frozen reference facts are resolved exactly once, before the atomic
	// admission port, and projected onto the returned exposure. The runtime
	// prefers these admitted refs over its fallback resolvers, so the
	// request's terminal facts stay exactly the declared ones; a zero fact
	// stays the zero ref and is never replaced by the scope snapshots. A
	// non-opting quoter keeps the version-1 mapping byte-for-byte.
	pricingRef, policyRef := a.admittedReferenceFacts(ctx, quote)
	admitIn := sdkbilling.ExposureAdmissionInput{
		Subject:         subject,
		Scope:           in.Scope,
		AccountID:       accountID,
		Quote:           quote,
		ExecutionLimits: limits,
		Routes:          routes,
	}
	if err := admitIn.Validate(); err != nil {
		return billingport.CallExposure{}, fmt.Errorf("%w: admission input: %v", ErrAdmission, err)
	}
	handle, err := a.binding.Admission.Admit(ctx, admitIn)
	if err != nil {
		// %w keeps the binding-returned class (for example
		// billingport.ErrAccountNotReady) in the chain, matching the reference
		// admission path; the outer ErrAdmission stays the adapter identity.
		return billingport.CallExposure{}, fmt.Errorf("%w: binding admit: %w", ErrAdmission, err)
	}
	if err := handle.Validate(); err != nil {
		return billingport.CallExposure{}, fmt.Errorf("%w: invalid handle: %v", ErrAdmission, err)
	}
	// The handle must authorize exactly the admitted request. A foreign
	// store, call, or quote identity cannot authorize this call.
	if !handle.MatchesAdmission(admitIn) {
		return billingport.CallExposure{}, fmt.Errorf("%w: handle store %q call %q quote %q for request %q/%q: %w",
			ErrAdmission, handle.StoreID, handle.BillingCallID, handle.QuoteID,
			admitIn.Subject.StoreID, admitIn.Subject.BillingCallID,
			sdkbilling.ErrAdmissionMismatch)
	}
	exposure := billingport.CallExposure{
		AccountID:       accountID,
		CallID:          strings.TrimSpace(in.BillingCallID),
		PricingRef:      pricingRef,
		ChargePolicyRef: policyRef,
		RouteTariffs:    routeTariffs,
	}
	// The inert money-free maximum projection: trim and require a non-empty
	// currency, copy the exact nano amount, no money arithmetic or
	// representation. An absent maximum stays the zero value; it is never a
	// fabricated zero.
	if quote.Maximum.Present {
		currency := strings.TrimSpace(quote.Maximum.Currency)
		if currency == "" {
			return billingport.CallExposure{}, fmt.Errorf("%w: quote maximum requires a currency", ErrAdmission)
		}
		exposure.Max.Nano = quote.Maximum.NanoUnits
		exposure.Max.Currency = currency
	}
	return exposure, nil
}

// admittedReferenceFacts resolves the reference facts installed on the admitted
// exposure. A declaring ReferenceFacts quoter is read exactly once here, before
// the atomic admission port, so one request stamps one frozen pair of refs
// (timestamps included) and an absent fact stays the zero ref. A non-declaring
// quoter keeps the version-1 mapping: the frozen quote refs with the binding
// scope as fallback, identity fields only.
func (a *Adapter) admittedReferenceFacts(ctx context.Context, quote economics.ExposureQuote) (usageevidence.VersionRef, usageevidence.VersionRef) {
	if facts := a.referenceFacts; facts != nil {
		return referenceVersionRef(facts.CustomerPricingRef(ctx)), referenceVersionRef(facts.ChargePolicyRef(ctx))
	}
	pricing := quote.Tariff
	if pricing.ID == "" {
		pricing = a.scope.Tariff
	}
	chargePolicy := quote.Policy
	if chargePolicy.ID == "" {
		chargePolicy = a.scope.Policy
	}
	return usageevidence.VersionRef{ID: pricing.ID, Version: pricing.Version},
		usageevidence.VersionRef{ID: chargePolicy.ID, Version: chargePolicy.Version}
}

// admissionRouteFacts projects the shared planned-leaf traversal into the
// ordered admission route facts. Occurrence order and multiplicity survive
// exactly; a leaf without a concrete backend instance carries no fact, and a
// concrete instance whose factory kind the generation did not resolve carries
// an empty kind so a consumer that needs it fails closed instead of guessing.
//
// The facts exist only when the host published a generation-resolved kind
// inventory. An adapter without one carries no route facts at all: the
// carriage is not in use, and a consumer must not read an absent inventory as
// a set of unresolved routes. The facts are metadata only: the adapter
// applies no policy on them.
func (a *Adapter) admissionRouteFacts(sel *routing.Selector) []sdkbilling.AdmissionRouteFact {
	if len(a.backendKinds) == 0 {
		return nil
	}
	leaves := routing.PlannedLeaves(sel)
	if len(leaves) == 0 {
		return nil
	}
	out := make([]sdkbilling.AdmissionRouteFact, 0, len(leaves))
	for _, leaf := range leaves {
		backend := strings.TrimSpace(leaf.Backend)
		if backend == "" {
			continue
		}
		out = append(out, sdkbilling.AdmissionRouteFact{
			BackendInstanceID: backend,
			BackendKind:       strings.TrimSpace(a.backendKinds[backend]),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// BindBackendKinds returns a NEW adapter bound to a copy of the supplied
// generation-resolved backend-kind inventory. The receiver is never mutated:
// the shared process adapter keeps serving its in-flight requests with the
// facts it was built with, while a new generation publishes its own. The
// returned copy shares the receiver's owned-resource lifecycle state, so one
// process still starts and closes the binding-owned resources exactly once.
func (a *Adapter) BindBackendKinds(kinds map[string]string) *Adapter {
	if a == nil {
		return nil
	}
	clone := *a
	clone.backendKinds = cloneBackendKinds(kinds)
	return &clone
}

// cloneBackendKinds returns an independent copy of the instance-to-kind map.
func cloneBackendKinds(kinds map[string]string) map[string]string {
	if len(kinds) == 0 {
		return nil
	}
	out := make(map[string]string, len(kinds))
	maps.Copy(out, kinds)
	return out
}

// BindBackendKinds returns the admission port that carries the
// generation-resolved backend-kind inventory for the planned-route facts of an
// admission. A port that is not this concrete adapter is returned unchanged:
// the binder carries metadata only and never changes a foreign admission
// implementation. The shared process admission port is never mutated.
func BindBackendKinds(admission runtimecore.BillingExposureAdmission, kinds map[string]string) runtimecore.BillingExposureAdmission {
	adapter, ok := admission.(*Adapter)
	if !ok || adapter == nil {
		return admission
	}
	return adapter.BindBackendKinds(kinds)
}

// plannedRouteCandidates projects the shared planned-leaf traversal into the
// quote request. Multiplicity and order survive exactly; a leaf-less plan
// carries no routes. The runtime-effective output caps are aligned by index
// with the same traversal, so a declaring quoter receives the actual canonical
// request limit per occurrence; a missing or short cap list keeps the
// version-1 request mapping for the remaining occurrences.
func plannedRouteCandidates(sel *routing.Selector, caps []runtimecore.BillingRouteOutputCap) []economics.RouteCandidate {
	leaves := routing.PlannedLeaves(sel)
	if len(leaves) == 0 {
		return nil
	}
	out := make([]economics.RouteCandidate, 0, len(leaves))
	for i, leaf := range leaves {
		candidate := economics.RouteCandidate{Backend: leaf.Backend, Model: leaf.Model}
		if i < len(caps) {
			// A computed entry is authoritative even when absent: nil keeps the
			// legacy/uncarried mapping, while Present=false states that the
			// executed occurrence has no positive finite cap and must never be
			// covered by the call-level client limit.
			if caps[i].Present {
				candidate.MaxOutputTokens = &economics.RouteOutputBound{Tokens: caps[i].Tokens, Present: true}
			} else {
				candidate.MaxOutputTokens = &economics.RouteOutputBound{Present: false}
			}
		}
		out = append(out, candidate)
	}
	return out
}

// admittedRouteTariffs enforces the frozen quote's route-tariff carriage and
// returns the canonical bindings to stamp on the admitted exposure. A
// non-declaring quoter keeps the version-1 mapping: no coverage requirement
// and no bindings stamped. A declaring quoter must set the explicit mode:
// none is the intentional scalar/fallback quote, frozen requires exact
// distinct-route coverage of the planned leaves (equal duplicate bindings
// collapse, conflicting duplicates fail closed, and a frozen quote without a
// supplied plan cannot prove coverage).
func admittedRouteTariffs(quote economics.ExposureQuote, sel *routing.Selector, declaring bool) ([]billingport.RouteTariffBinding, error) {
	if !declaring {
		return nil, nil
	}
	switch quote.RouteTariffMode {
	case economics.RouteTariffModeNone:
		return nil, nil
	case economics.RouteTariffModeFrozen:
		planned := routing.PlannedLeaves(sel)
		if len(planned) == 0 {
			return nil, fmt.Errorf("%w: frozen route tariffs without a planned route plan: %w", ErrAdmission, sdkbilling.ErrQuoteMismatch)
		}
		want := make(map[string]struct{}, len(planned))
		for _, leaf := range planned {
			want[economics.RouteTariffKey(leaf.Backend, leaf.Model)] = struct{}{}
		}
		bindings, err := canonicalRouteTariffBindings(quote.RouteTariffs)
		if err != nil {
			return nil, err
		}
		if len(bindings) != len(want) {
			return nil, fmt.Errorf("%w: frozen route tariffs cover %d distinct routes, planned %d: %w",
				ErrAdmission, len(bindings), len(want), sdkbilling.ErrQuoteMismatch)
		}
		for _, binding := range bindings {
			if _, ok := want[binding.RouteID]; !ok {
				return nil, fmt.Errorf("%w: frozen route tariff %q is not a planned route: %w", ErrAdmission, binding.RouteID, sdkbilling.ErrQuoteMismatch)
			}
		}
		return bindings, nil
	default:
		return nil, fmt.Errorf("%w: route tariff carrier must declare the quote mode, got %q: %w", ErrAdmission, quote.RouteTariffMode, sdkbilling.ErrQuoteMismatch)
	}
}

// canonicalRouteTariffBindings validates, sorts, and deduplicates equal
// bindings exactly like the internal reference quote producer: equal
// duplicates collapse, conflicting tariffs for one route fail closed, and the
// result is sorted by stable route identity.
func canonicalRouteTariffBindings(in []economics.RouteTariffBinding) ([]billingport.RouteTariffBinding, error) {
	out := make([]billingport.RouteTariffBinding, 0, len(in))
	for i, binding := range in {
		if err := binding.Validate(); err != nil {
			return nil, fmt.Errorf("%w: route tariff %d: %v", ErrAdmission, i, err)
		}
		out = append(out, billingport.RouteTariffBinding{
			RouteID:       binding.RouteID,
			TariffID:      binding.TariffID,
			TariffVersion: binding.TariffVersion,
			ContentHash:   binding.ContentHash,
		})
	}
	slices.SortFunc(out, func(x, y billingport.RouteTariffBinding) int {
		if c := strings.Compare(x.RouteID, y.RouteID); c != 0 {
			return c
		}
		if c := strings.Compare(x.TariffID, y.TariffID); c != 0 {
			return c
		}
		if c := strings.Compare(x.TariffVersion, y.TariffVersion); c != 0 {
			return c
		}
		return strings.Compare(x.ContentHash, y.ContentHash)
	})
	deduped := out[:0]
	for _, binding := range out {
		if n := len(deduped); n > 0 && deduped[n-1].RouteID == binding.RouteID {
			if deduped[n-1] != binding {
				return nil, fmt.Errorf("%w: conflicting route tariffs quoted for route %q: %w", ErrAdmission, binding.RouteID, sdkbilling.ErrQuoteMismatch)
			}
			continue
		}
		deduped = append(deduped, binding)
	}
	if len(deduped) == 0 {
		return nil, nil
	}
	return deduped, nil
}

// checkQuoteBinding requires the quoter response to name exactly the
// requested call subject and the binding's frozen snapshot identities.
// Tariff ID/version and policy ID/version/owner must equal the scope the
// quote was built under; anything else is rejected before admission.
func (a *Adapter) checkQuoteBinding(quote economics.ExposureQuote, subject metering.SubjectRef) error {
	if quote.Subject.Kind != subject.Kind ||
		quote.Subject.StoreID != subject.StoreID ||
		quote.Subject.BillingCallID != subject.BillingCallID {
		return fmt.Errorf("%w: quote subject %q/%q/%q for requested %q/%q/%q: %w",
			ErrAdmission,
			quote.Subject.Kind, quote.Subject.StoreID, quote.Subject.BillingCallID,
			subject.Kind, subject.StoreID, subject.BillingCallID,
			sdkbilling.ErrQuoteMismatch)
	}
	if quote.Tariff.ID != a.scope.Tariff.ID || quote.Tariff.Version != a.scope.Tariff.Version {
		return fmt.Errorf("%w: quote tariff %q/%q differs from frozen %q/%q: %w",
			ErrAdmission, quote.Tariff.ID, quote.Tariff.Version,
			a.scope.Tariff.ID, a.scope.Tariff.Version,
			sdkbilling.ErrQuoteMismatch)
	}
	if quote.Policy.ID != a.scope.Policy.ID || quote.Policy.Version != a.scope.Policy.Version ||
		quote.Policy.PolicyID != a.scope.Policy.PolicyID {
		return fmt.Errorf("%w: quote policy %q/%q/%q differs from frozen %q/%q/%q: %w",
			ErrAdmission, quote.Policy.ID, quote.Policy.Version, quote.Policy.PolicyID,
			a.scope.Policy.ID, a.scope.Policy.Version, a.scope.Policy.PolicyID,
			sdkbilling.ErrQuoteMismatch)
	}
	return nil
}

// admissionLimits projects bounded internal size facts onto finite public
// execution limits. Strict offers with no defensible bound fail closed here,
// before provider execution, matching the reference admission semantics.
func admissionLimits(in runtimecore.BillingExposureAdmissionInput) ([]economics.Limit, error) {
	var limits []economics.Limit
	if in.RequestSize.Available {
		if in.RequestSize.Tokens < 0 {
			return nil, fmt.Errorf("%w: negative request size", ErrAdmission)
		}
		limits = append(limits, economics.Limit{Name: limitInputTokens, Value: in.RequestSize.Tokens, Unit: limitUnitToken})
	}
	if in.MaxOutputTokens != nil {
		if *in.MaxOutputTokens < 0 {
			return nil, fmt.Errorf("%w: negative max output", ErrAdmission)
		}
		limits = append(limits, economics.Limit{Name: limitMaxOutputTokens, Value: int64(*in.MaxOutputTokens), Unit: limitUnitToken})
	}
	if len(limits) == 0 {
		return nil, fmt.Errorf("%w: at least one finite execution limit is required", ErrAdmission)
	}
	return limits, nil
}

// AppendLeg implements the terminal B-leg handoff by translating the leg
// record — including its neutral V2 observations — into a public terminal
// envelope. The binding durably acknowledges; success is returned only for a
// valid acknowledgement. A sink that declares RecordCarrier receives the
// version-2 envelope carrying the exact sealed record.
func (a *Adapter) AppendLeg(ctx context.Context, record usageevidence.CallLegUsageRecord) error {
	if a == nil {
		return fmt.Errorf("%w: nil adapter", ErrTerminal)
	}
	var carried *sdkbilling.TerminalUsageRecord
	if a.carriesRecords {
		var err error
		carried, err = sealLegTerminalRecord(record)
		if err != nil {
			return err
		}
	}
	outcome, err := a.legTerminalOutcome(record.Outcome)
	if err != nil {
		return err
	}
	if record.AttemptSeq <= 0 {
		return fmt.Errorf("%w: b-leg closure requires a positive authoritative attempt sequence", ErrTerminal)
	}
	callID := strings.TrimSpace(record.CallID.String())
	if callID == "" {
		return fmt.Errorf("%w: billing call identity is required", ErrTerminal)
	}
	bLegID := strings.TrimSpace(record.BLegID)
	if bLegID == "" {
		return fmt.Errorf("%w: B-leg identity is required", ErrTerminal)
	}
	// The carried record identity is canonical and authoritative for a
	// declaring sink; version 1 keeps the pre-set-or-derived envelope identity
	// byte-for-byte.
	envelopeID := strings.TrimSpace(record.Key)
	payloadHash := terminalPayloadHash(strings.TrimSpace(record.Key), strings.TrimSpace(record.Fingerprint))
	if carried != nil {
		envelopeID = carried.Key
		payloadHash = carriedPayloadHash(carried.JSON)
	} else if envelopeID == "" {
		envelopeID, err = usageevidence.CallLegUsageKey(record.CallID, bLegID)
		if err != nil {
			return fmt.Errorf("%w: envelope identity: %v", ErrTerminal, err)
		}
	}
	seq := uint64(record.AttemptSeq)
	aLegID := strings.TrimSpace(record.ALegID)
	submissionID := strings.TrimSpace(record.SubmissionID)
	// Terminal scope is mandatory, never best-effort: the runtime freezes
	// request scope onto every handoff context, so a missing scope means
	// unattributable evidence. The sink is not invoked.
	sc, ok := scopeOf(ctx)
	if !ok {
		return fmt.Errorf("%w: trusted terminal scope is required", ErrTerminal)
	}
	env := sdkbilling.TerminalEnvelope{
		Version:    sdkbilling.TerminalEnvelopeVersionV1,
		EnvelopeID: envelopeID,
		StoreID:    a.scope.StoreID,
		Scope:      sc,
		Subject: metering.SubjectRef{
			Kind:          metering.SubjectBLeg,
			StoreID:       a.scope.StoreID,
			BillingCallID: callID,
			BLegID:        bLegID,
			AttemptSeq:    seq,
			ALegID:        aLegID,
			SubmissionID:  submissionID,
		},
		Correlation: metering.CorrelationV2{
			StoreID:       a.scope.StoreID,
			BillingCallID: callID,
			BLegID:        bLegID,
			AttemptSeq:    seq,
			ALegID:        aLegID,
			SubmissionID:  submissionID,
		},
		Outcome:      outcome,
		AttemptSeq:   seq,
		ALegID:       aLegID,
		SubmissionID: submissionID,
		Workload:     mapWorkload(record.Workload),
		Tariff:       a.scope.Tariff,
		Policy:       a.scope.Policy,
		Observations: append([]metering.Observation(nil), record.Observations...),
		PayloadHash:  payloadHash,
	}
	if carried != nil {
		env.Version = sdkbilling.TerminalEnvelopeVersionV2
		env.Record = carried
	}
	return a.appendEnvelope(ctx, env)
}

// AppendCall implements the terminal call-closure handoff. Call closures
// carry identity, outcome, and frozen references; request-scoped inference
// evidence travels on B-leg envelopes. A sink that declares RecordCarrier
// receives the version-2 envelope carrying the exact sealed record.
func (a *Adapter) AppendCall(ctx context.Context, record usageevidence.CallUsageRecord) error {
	if a == nil {
		return fmt.Errorf("%w: nil adapter", ErrTerminal)
	}
	var carried *sdkbilling.TerminalUsageRecord
	if a.carriesRecords {
		var err error
		carried, err = sealCallTerminalRecord(record)
		if err != nil {
			return err
		}
	}
	outcome, err := a.turnTerminalOutcome(record.Outcome)
	if err != nil {
		return err
	}
	callID := strings.TrimSpace(record.CallID.String())
	if callID == "" {
		return fmt.Errorf("%w: billing call identity is required", ErrTerminal)
	}
	// The carried record identity is canonical and authoritative for a
	// declaring sink; version 1 keeps today's pre-set-or-derived identity.
	envelopeID := strings.TrimSpace(record.Key)
	payloadHash := terminalPayloadHash(strings.TrimSpace(record.Key), strings.TrimSpace(record.Fingerprint))
	if carried != nil {
		envelopeID = carried.Key
		payloadHash = carriedPayloadHash(carried.JSON)
	} else if envelopeID == "" {
		envelopeID, err = usageevidence.CallUsageKey(record.CallID)
		if err != nil {
			return fmt.Errorf("%w: envelope identity: %v", ErrTerminal, err)
		}
	}
	aLegID := strings.TrimSpace(record.ALegID)
	submissionID := strings.TrimSpace(record.SubmissionID)
	// Terminal scope is mandatory, never best-effort: the runtime freezes
	// request scope onto every handoff context, so a missing scope means
	// unattributable evidence. The sink is not invoked.
	sc, ok := scopeOf(ctx)
	if !ok {
		return fmt.Errorf("%w: trusted terminal scope is required", ErrTerminal)
	}
	env := sdkbilling.TerminalEnvelope{
		Version:    sdkbilling.TerminalEnvelopeVersionV1,
		EnvelopeID: envelopeID,
		StoreID:    a.scope.StoreID,
		Scope:      sc,
		Subject: metering.SubjectRef{
			Kind:          metering.SubjectBillingCall,
			StoreID:       a.scope.StoreID,
			BillingCallID: callID,
			ALegID:        aLegID,
			SubmissionID:  submissionID,
		},
		Correlation: metering.CorrelationV2{
			StoreID:       a.scope.StoreID,
			BillingCallID: callID,
			ALegID:        aLegID,
			SubmissionID:  submissionID,
		},
		Outcome:         outcome,
		AccountID:       strings.TrimSpace(record.AccountID),
		ALegID:          aLegID,
		SubmissionID:    submissionID,
		Workload:        mapWorkload(record.Workload),
		ExpectedBLegIDs: append([]string(nil), record.ExpectedBLegIDs...),
		Tariff:          a.scope.Tariff,
		Policy:          a.scope.Policy,
		PayloadHash:     payloadHash,
	}
	if carried != nil {
		env.Version = sdkbilling.TerminalEnvelopeVersionV2
		env.Record = carried
	}
	return a.appendEnvelope(ctx, env)
}

// mapWorkload projects internal workload correlation onto the public
// envelope. A zero workload maps to nil: the closure carries no workload
// attribution rather than an empty label.
func mapWorkload(workload usageevidence.WorkloadIdentity) *sdkbilling.TerminalWorkload {
	if workload.Class == "" && workload.Role == "" {
		return nil
	}
	return &sdkbilling.TerminalWorkload{
		Class: string(workload.Class),
		Role:  string(workload.Role),
	}
}

// appendEnvelope validates the translated envelope, hands it to the binding,
// and validates the durable acknowledgement. The acknowledgement must answer
// exactly the sent envelope: a foreign store or envelope identity is a typed
// mismatch, never durable success. A returned error means the record must be
// retried and is never silently dropped.
func (a *Adapter) appendEnvelope(ctx context.Context, env sdkbilling.TerminalEnvelope) error {
	if err := env.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrTerminal, err)
	}
	ack, err := a.binding.Terminal.AppendTerminal(ctx, env)
	if err != nil {
		return fmt.Errorf("%w: binding terminal: %v", ErrTerminal, err)
	}
	if err := ack.Validate(); err != nil {
		return fmt.Errorf("%w: invalid acknowledgement: %v", ErrTerminal, err)
	}
	if !ack.MatchesEnvelope(env) {
		return fmt.Errorf("%w: ack store %q envelope %q for envelope %q/%q: %w",
			ErrTerminal, ack.StoreID, ack.EnvelopeID, env.StoreID, env.EnvelopeID,
			sdkbilling.ErrTerminalAckMismatch)
	}
	return nil
}

// terminalPayloadHash binds a version-1 envelope to the exact internal record
// identity and fingerprint with a stable hex digest.
func terminalPayloadHash(key, fingerprint string) string {
	sum := sha256.Sum256([]byte(key + "\x00" + fingerprint))
	return hex.EncodeToString(sum[:])
}

// carriedPayloadHash is the version-2 payload identity: the lowercase SHA-256
// hex of the exact carried record bytes. The public contract validates it on
// both sides, so a payload edit is never acknowledgement-visible.
func carriedPayloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// sealLegTerminalRecord seals one leg record for version-2 carriage. A pre-set
// Key or Fingerprint that conflicts with the sealed identity fails closed
// before the envelope is stamped: Seal recomputes both, so an unchecked
// conflict would be silently overwritten instead of surfaced.
func sealLegTerminalRecord(record usageevidence.CallLegUsageRecord) (*sdkbilling.TerminalUsageRecord, error) {
	presetKey := strings.TrimSpace(record.Key)
	presetFingerprint := strings.TrimSpace(record.Fingerprint)
	sealed, err := record.Seal()
	if err != nil {
		return nil, fmt.Errorf("%w: seal call-leg record: %v", ErrTerminal, err)
	}
	if err := checkPresetRecordIdentity(presetKey, presetFingerprint, sealed.Key, sealed.Fingerprint); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal sealed call-leg record: %v", ErrTerminal, err)
	}
	return &sdkbilling.TerminalUsageRecord{Key: sealed.Key, Fingerprint: sealed.Fingerprint, JSON: payload}, nil
}

// sealCallTerminalRecord seals one call closure for version-2 carriage with
// the same conflicting-preset-identity rejection as the leg path.
func sealCallTerminalRecord(record usageevidence.CallUsageRecord) (*sdkbilling.TerminalUsageRecord, error) {
	presetKey := strings.TrimSpace(record.Key)
	presetFingerprint := strings.TrimSpace(record.Fingerprint)
	sealed, err := record.Seal()
	if err != nil {
		return nil, fmt.Errorf("%w: seal call record: %v", ErrTerminal, err)
	}
	if err := checkPresetRecordIdentity(presetKey, presetFingerprint, sealed.Key, sealed.Fingerprint); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal sealed call record: %v", ErrTerminal, err)
	}
	return &sdkbilling.TerminalUsageRecord{Key: sealed.Key, Fingerprint: sealed.Fingerprint, JSON: payload}, nil
}

// checkPresetRecordIdentity rejects a caller-set record identity that
// disagrees with the sealed identity. Empty pre-set values are filled by Seal;
// non-empty ones must already be canonical.
func checkPresetRecordIdentity(presetKey, presetFingerprint, sealedKey, sealedFingerprint string) error {
	if presetKey != "" && presetKey != sealedKey {
		return fmt.Errorf("%w: preset record key %q conflicts with sealed identity %q", ErrTerminal, presetKey, sealedKey)
	}
	if presetFingerprint != "" && presetFingerprint != sealedFingerprint {
		return fmt.Errorf("%w: preset record fingerprint conflicts with the sealed identity for key %q", ErrTerminal, sealedKey)
	}
	return nil
}

// legTerminalOutcome maps terminal B-leg outcomes for the negotiated envelope
// version. Version 1 keeps the closure vocabulary and fails closed on
// non-terminal dispositions; a record-carrying sink gets the coarse
// non-closure label for them, while the sealed record keeps the exact
// disposition.
func (a *Adapter) legTerminalOutcome(outcome usageevidence.LegOutcome) (sdkbilling.TerminalOutcome, error) {
	if mapped, err := legOutcomeToTerminal(outcome); err == nil {
		return mapped, nil
	}
	if a.carriesRecords {
		switch outcome {
		case usageevidence.LegOutcomeRejected, usageevidence.LegOutcomeNeverStarted, usageevidence.LegOutcomeUnknown:
			return sdkbilling.TerminalUnresolved, nil
		}
	}
	return "", fmt.Errorf("%w: unmappable leg outcome %q", ErrTerminal, outcome)
}

// turnTerminalOutcome maps terminal call outcomes for the negotiated envelope
// version with the same version-2 non-closure label.
func (a *Adapter) turnTerminalOutcome(outcome usageevidence.TurnOutcome) (sdkbilling.TerminalOutcome, error) {
	if mapped, err := turnOutcomeToTerminal(outcome); err == nil {
		return mapped, nil
	}
	if a.carriesRecords && outcome == usageevidence.TurnOutcomeUnknown {
		return sdkbilling.TerminalUnresolved, nil
	}
	return "", fmt.Errorf("%w: unmappable call outcome %q", ErrTerminal, outcome)
}

// legOutcomeToTerminal maps terminal B-leg outcomes onto the public closure
// vocabulary. Non-terminal dispositions (rejected, never started, unknown)
// have no honest closure mapping and fail closed for version 1.
func legOutcomeToTerminal(outcome usageevidence.LegOutcome) (sdkbilling.TerminalOutcome, error) {
	switch outcome {
	case usageevidence.LegOutcomeWinner, usageevidence.LegOutcomeLoser:
		return sdkbilling.TerminalCompleted, nil
	case usageevidence.LegOutcomeFailed, usageevidence.LegOutcomeSwallowed:
		return sdkbilling.TerminalFailed, nil
	case usageevidence.LegOutcomeCanceled:
		return sdkbilling.TerminalCanceled, nil
	default:
		return "", fmt.Errorf("%w: unmappable leg outcome %q", ErrTerminal, outcome)
	}
}

// turnOutcomeToTerminal maps terminal call outcomes onto the public closure
// vocabulary. Unknown outcomes fail closed rather than inventing finality.
func turnOutcomeToTerminal(outcome usageevidence.TurnOutcome) (sdkbilling.TerminalOutcome, error) {
	switch outcome {
	case usageevidence.TurnOutcomeCompleted:
		return sdkbilling.TerminalCompleted, nil
	case usageevidence.TurnOutcomeFailed:
		return sdkbilling.TerminalFailed, nil
	case usageevidence.TurnOutcomeCanceled:
		return sdkbilling.TerminalCanceled, nil
	default:
		return "", fmt.Errorf("%w: unmappable call outcome %q", ErrTerminal, outcome)
	}
}

// Identity resolves runtime billing identity from the binding scope and
// trusted per-request scope. It is wire-bounded: resolvers read only scope
// and static snapshot identities, never provider-shaped request data, so the
// large-payload fast path stays open. When the quoter supplies ReferenceFacts,
// those frozen facts replace the scope mapping so generated records carry the
// actual customer-pricing, charge-policy, and per-route operator-rate
// references; absent facts stay absent and are never substituted.
func (a *Adapter) Identity() runtimecore.BillingIdentity {
	bound := a.scope
	storeID := bound.StoreID
	pricing := usageevidence.VersionRef{ID: bound.Tariff.ID, Version: bound.Tariff.Version}
	policy := usageevidence.VersionRef{ID: bound.Policy.ID, Version: bound.Policy.Version}
	account := func(ctx context.Context, _ aipapi.Call) string {
		sc, ok := scopeOf(ctx)
		if !ok {
			return ""
		}
		return accountOf(sc)
	}
	storeResolver := func(context.Context) string { return storeID }
	wireBounded := true
	wireAccountID := func(_ context.Context, sc scope.PrincipalScopeView) string { return accountOf(sc) }
	customerPricing := func(context.Context, aipapi.Call) usageevidence.VersionRef { return pricing }
	chargePolicy := func(context.Context, aipapi.Call) usageevidence.VersionRef { return policy }
	operatorRate := func(context.Context, string, string) usageevidence.VersionRef { return pricing }
	wirePricing := func(context.Context) usageevidence.VersionRef { return pricing }
	wirePolicy := func(context.Context) usageevidence.VersionRef { return policy }
	if a.identity != nil {
		// A supplied identity resolver is the owner's authority. The account
		// callback is preserved unchanged and is never replaced by a
		// principal-derived value, and the store callback is preserved
		// unchanged when it is present: only an absent store resolver falls
		// back to the fixed binding store identity.
		//
		// The account callback is call-shaped, so the host resolves it with
		// the actual request at the identity boundary. The callback itself
		// is carried unchanged: the host never wraps it into a narrower
		// shape and never drops the request.
		account = a.identity.AccountID
		if a.identity.StoreID != nil {
			storeResolver = a.identity.StoreID
		}
		// A supplied account callback inspects the request, so the identity
		// bundle is not wire-bounded and the bounded wire path must not run
		// it. The canonical path still resolves it with the real call.
		wireBounded = false
		wireAccountID = nil
	}
	if facts := a.referenceFacts; facts != nil {
		customerPricing = func(ctx context.Context, _ aipapi.Call) usageevidence.VersionRef {
			return referenceVersionRef(facts.CustomerPricingRef(ctx))
		}
		chargePolicy = func(ctx context.Context, _ aipapi.Call) usageevidence.VersionRef {
			return referenceVersionRef(facts.ChargePolicyRef(ctx))
		}
		operatorRate = func(ctx context.Context, backendID, modelID string) usageevidence.VersionRef {
			return referenceVersionRef(facts.OperatorRateRef(ctx, backendID, modelID))
		}
		wirePricing = func(ctx context.Context) usageevidence.VersionRef {
			return referenceVersionRef(facts.CustomerPricingRef(ctx))
		}
		wirePolicy = func(ctx context.Context) usageevidence.VersionRef {
			return referenceVersionRef(facts.ChargePolicyRef(ctx))
		}
	}
	// The adapter exists only for a binding that passed the binding
	// validation, so the identity bundle it publishes carries the
	// monetary-host capability: a complete, validated monetary authority
	// stands behind this identity.
	monetaryHost := runtimecore.MonetaryHost{
		Authority: runtimecore.MonetaryAuthorityExternalBinding,
		BindingID: a.binding.ID,
	}
	// The durable-handoff statement needs the binding's terminal port to
	// implement the capability: a validated binding with plain ports and a
	// lifecycle does not hold the obligation durably by itself.
	if _, ok := a.binding.Terminal.(sdkbilling.DurableTerminalHandoff); ok {
		monetaryHost.TerminalHandoff = runtimecore.TerminalHandoffDurable
	}
	return runtimecore.BillingIdentity{
		AccountID:              account,
		StoreID:                storeResolver,
		CustomerPricingRef:     customerPricing,
		ChargePolicyRef:        chargePolicy,
		OperatorRateRef:        operatorRate,
		WireBounded:            wireBounded,
		WireAccountID:          wireAccountID,
		WireCustomerPricingRef: wirePricing,
		WireChargePolicyRef:    wirePolicy,
		// The adapter exists only for a binding that passed the binding
		// validation, so the identity bundle it publishes carries the
		// monetary-host capability: a complete, validated monetary authority
		// stands behind this identity.
		MonetaryHost: monetaryHost,
	}
}

// referenceVersionRef projects the public reference-facts identity onto the
// carved usage-evidence reference shape without losing timing provenance.
func referenceVersionRef(ref economics.VersionRef) usageevidence.VersionRef {
	return usageevidence.VersionRef{
		ID:          ref.ID,
		Version:     ref.Version,
		EffectiveAt: ref.EffectiveAt,
		FetchedAt:   ref.FetchedAt,
	}
}

// TerminalHandoffReady implements the durable-handoff port by delegating to
// the binding's terminal port.
//
// A validated binding's ports and lifecycle are not evidence that the
// obligation is held durably: only a terminal port that implements the
// capability says so. A binding without one has no durable handoff, and the
// customer-funded path is refused on it.
func (a *Adapter) TerminalHandoffReady() (bool, string) {
	if a == nil {
		return false, "binding adapter is unavailable"
	}
	handoff, ok := a.binding.Terminal.(sdkbilling.DurableTerminalHandoff)
	if !ok {
		return false, "binding terminal port holds no durable handoff"
	}
	return handoff.TerminalHandoffReady()
}

// StartOwnedResources starts binding-owned resources once, in registration
// order. A start failure unwinds already-started resources in reverse order
// and leaves the adapter unstarted so the build fails before publication.
func (a *Adapter) StartOwnedResources(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("%w: nil adapter", ErrLifecycle)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	if a.state.started {
		return nil
	}
	var started []sdkbilling.OwnedResource
	for _, resource := range a.lifecycle.Owned {
		if err := resource.Start(ctx); err != nil {
			if unwind := closeOwnedReverse(context.Background(), started); unwind != nil {
				return fmt.Errorf("%w: start %q: %v; unwind: %v", ErrLifecycle, resource.ID, err, unwind)
			}
			return fmt.Errorf("%w: start %q: %v", ErrLifecycle, resource.ID, err)
		}
		started = append(started, resource)
	}
	a.state.started = true
	return nil
}

// Close releases started binding-owned resources once, in reverse start
// order. Borrowed handles are never touched. It is registered with host
// cleanup exactly once, so normal Host/Manager Close and repeated Close
// dispose owned resources exactly once.
func (a *Adapter) Close() error {
	if a == nil {
		return nil
	}
	var err error
	a.state.closeOnce.Do(func() {
		a.state.mu.Lock()
		started := a.state.started
		owned := append([]sdkbilling.OwnedResource(nil), a.lifecycle.Owned...)
		a.state.mu.Unlock()
		if !started {
			return
		}
		err = closeOwnedReverse(context.Background(), owned)
	})
	return err
}

// closeOwnedReverse closes owned resources in reverse order, joining errors.
func closeOwnedReverse(ctx context.Context, owned []sdkbilling.OwnedResource) error {
	var out error
	for _, o := range slices.Backward(owned) {
		if err := o.Close(ctx); err != nil {
			out = errors.Join(out, fmt.Errorf("%w: close %q: %v", ErrLifecycle, o.ID, err))
		}
	}
	return out
}
