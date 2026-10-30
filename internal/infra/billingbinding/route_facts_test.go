package billingbinding_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	runtimecore "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/billingbinding"
	sdkbilling "github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// errRefused is the sentinel the fake preflight refuses with.
var errRefused = errors.New("refused")

// routeFactAdmitter is an admission port that also publishes the optional
// pre-quote refinement, so the adapter's ordering and the carried facts are
// observable in one fixture.
type routeFactAdmitter struct {
	handle    sdkbilling.ExposureHandle
	calls     int
	last      sdkbilling.ExposureAdmissionInput
	preflight int
	preRoutes []sdkbilling.AdmissionRouteFact
	preErr    error
	order     *[]string
}

func (f *routeFactAdmitter) Admit(_ context.Context, in sdkbilling.ExposureAdmissionInput) (sdkbilling.ExposureHandle, error) {
	f.calls++
	f.last = in
	if f.order != nil {
		*f.order = append(*f.order, "admit")
	}
	return f.handle, nil
}

func (f *routeFactAdmitter) PreflightExposureAdmission(_ context.Context, in sdkbilling.ExposureAdmissionPreflightInput) error {
	f.preflight++
	f.preRoutes = in.Routes
	if f.order != nil {
		*f.order = append(*f.order, "preflight")
	}
	return f.preErr
}

// orderQuoter records the order in which the adapter asks for a price.
type orderQuoter struct {
	quote economics.ExposureQuote
	order *[]string
}

func (q *orderQuoter) Quote(_ context.Context, _ economics.QuoteInput) (economics.ExposureQuote, error) {
	if q.order != nil {
		*q.order = append(*q.order, "quote")
	}
	return q.quote, nil
}

// countingAdmitter is an admission port without the optional preflight.
type countingAdmitter struct {
	order *[]string
}

func (a *countingAdmitter) Admit(_ context.Context, _ sdkbilling.ExposureAdmissionInput) (sdkbilling.ExposureHandle, error) {
	if a.order != nil {
		*a.order = append(*a.order, "admit")
	}
	return sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"}, nil
}

// foreignAdmission is an admission implementation that is not the public
// binding adapter.
type foreignAdmission struct{}

func (f *foreignAdmission) Admit(context.Context, runtimecore.BillingExposureAdmissionInput) (billingport.CallExposure, error) {
	return billingport.CallExposure{}, nil
}

// routeFactBinding builds a valid public binding over the supplied quoter and
// admission port.
func routeFactBinding(t *testing.T, quoter economics.Quoter, admission sdkbilling.ExposureAdmitter) sdkbilling.Binding {
	t.Helper()
	return sdkbilling.Binding{
		ID:           "binding-route-facts",
		Version:      sdkbilling.BindingVersionV1,
		Scope:        bindingScope(),
		CreditScreen: &fakeScreen{result: sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditAllow, StoreID: testStore, AccountID: "acct-1"}},
		Quoter:       quoter,
		Admission:    admission,
		Terminal:     &fakeTerminal{},
	}
}

// routeFactAdapter builds the public adapter over the supplied ports.
func routeFactAdapter(t *testing.T, quoter economics.Quoter, admission sdkbilling.ExposureAdmitter) *billingbinding.Adapter {
	t.Helper()
	adapter, err := billingbinding.NewAdapter(routeFactBinding(t, quoter, admission))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	return adapter
}

// TestAdapterCarriesOrderedRouteFactsIntoAdmission proves the additive
// carriage: the admission port receives exactly the planned route occurrences
// of the route plan, in order and with their multiplicity, each carrying the
// generation-resolved factory kind.
func TestAdapterCarriesOrderedRouteFactsIntoAdmission(t *testing.T) {
	t.Parallel()
	admitter := &routeFactAdmitter{handle: sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"}}
	adapter := routeFactAdapter(t, &fakeQuoter{quote: quoteFixture(t)}, admitter)
	bound := adapter.BindBackendKinds(map[string]string{"a": "kind-a", "b": "kind-b"})
	if _, err := bound.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1!b:m2!a:m1")); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	want := []sdkbilling.AdmissionRouteFact{
		{BackendInstanceID: "a", BackendKind: "kind-a"},
		{BackendInstanceID: "b", BackendKind: "kind-b"},
		{BackendInstanceID: "a", BackendKind: "kind-a"},
	}
	if !reflect.DeepEqual(admitter.last.Routes, want) {
		t.Fatalf("admission routes = %+v, want the ordered occurrences %+v", admitter.last.Routes, want)
	}
}

// TestAdapterWithoutBackendKindsCarriesNoRouteFacts proves the carriage stays
// additive: an adapter without the generation-resolved inventory carries no
// route facts at all, so a consumer never reads an absent inventory as a set
// of unresolved routes, and the admission stays valid.
func TestAdapterWithoutBackendKindsCarriesNoRouteFacts(t *testing.T) {
	t.Parallel()
	admitter := &routeFactAdmitter{handle: sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"}}
	adapter := routeFactAdapter(t, &fakeQuoter{quote: quoteFixture(t)}, admitter)
	if _, err := adapter.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1")); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if len(admitter.last.Routes) != 0 {
		t.Fatalf("admission routes = %+v, want none", admitter.last.Routes)
	}
}

// TestAdapterCarriesAnUnresolvedKindForAnUnknownInstance proves the honesty
// of the carriage: a concrete instance the generation did not resolve keeps
// its identity and carries an unresolved kind, so a consumer that needs the
// kind fails closed instead of assuming one.
func TestAdapterCarriesAnUnresolvedKindForAnUnknownInstance(t *testing.T) {
	t.Parallel()
	admitter := &routeFactAdmitter{handle: sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"}}
	adapter := routeFactAdapter(t, &fakeQuoter{quote: quoteFixture(t)}, admitter)
	bound := adapter.BindBackendKinds(map[string]string{"b": "kind-b"})
	if _, err := bound.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1!b:m2")); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	want := []sdkbilling.AdmissionRouteFact{
		{BackendInstanceID: "a"},
		{BackendInstanceID: "b", BackendKind: "kind-b"},
	}
	if !reflect.DeepEqual(admitter.last.Routes, want) {
		t.Fatalf("admission routes = %+v, want %+v", admitter.last.Routes, want)
	}
}

// TestBindBackendKindsClonesTheGenerationInventory proves the binder is
// generation-local: the receiver keeps its own facts, the copy owns an
// independent map, and the shared process adapter is never mutated.
func TestBindBackendKindsClonesTheGenerationInventory(t *testing.T) {
	t.Parallel()
	admitter := &routeFactAdmitter{handle: sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"}}
	adapter := routeFactAdapter(t, &fakeQuoter{quote: quoteFixture(t)}, admitter)
	kinds := map[string]string{"a": "kind-a"}
	bound := adapter.BindBackendKinds(kinds)
	kinds["a"] = "mutated"
	if _, err := bound.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1")); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if len(admitter.last.Routes) != 1 || admitter.last.Routes[0].BackendKind != "kind-a" {
		t.Fatalf("bound routes = %+v, want the cloned kind-a fact", admitter.last.Routes)
	}
	// The process adapter is never mutated: it still carries no facts.
	if _, err := adapter.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1")); err != nil {
		t.Fatalf("Admit on the process adapter: %v", err)
	}
	if len(admitter.last.Routes) != 0 {
		t.Fatalf("the process adapter routes = %+v, want none", admitter.last.Routes)
	}
}

// TestBindBackendKindsLeavesForeignAdmissionPortsUnchanged proves the binder
// is generic: a foreign admission implementation is returned unchanged.
func TestBindBackendKindsLeavesForeignAdmissionPortsUnchanged(t *testing.T) {
	t.Parallel()
	foreign := &foreignAdmission{}
	bound := billingbinding.BindBackendKinds(foreign, map[string]string{"a": "kind-a"})
	if bound != runtimecore.BillingExposureAdmission(foreign) {
		t.Fatal("a foreign admission port must be returned unchanged")
	}
}

// TestAdapterPreflightsBeforeTheQuote proves the pre-quote ordering: the
// preflight runs after the trusted-input checks and before the quoter, and a
// refusal costs no price, no admission, and no exposure.
func TestAdapterPreflightsBeforeTheQuote(t *testing.T) {
	t.Parallel()
	order := []string{}
	admitter := &routeFactAdmitter{
		handle: sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"},
		order:  &order,
	}
	adapter := routeFactAdapter(t, &orderQuoter{quote: quoteFixture(t), order: &order}, admitter)
	bound := adapter.BindBackendKinds(map[string]string{"a": "kind-a"})
	if _, err := bound.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1")); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"preflight", "quote", "admit"}) {
		t.Fatalf("call order = %v, want preflight, quote, admit", order)
	}
	if len(admitter.preRoutes) != 1 || admitter.preRoutes[0].BackendInstanceID != "a" {
		t.Fatalf("preflight routes = %+v, want the planned a:m1 occurrence", admitter.preRoutes)
	}
}

// TestAdapterPreflightRefusalSkipsQuoteAndAdmission proves the refusal is
// effective: a refused preflight keeps the quoter and the admission port
// untouched and keeps the binding's own error class in the chain.
func TestAdapterPreflightRefusalSkipsQuoteAndAdmission(t *testing.T) {
	t.Parallel()
	order := []string{}
	admitter := &routeFactAdmitter{
		handle: sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"},
		order:  &order,
		preErr: errRefused,
	}
	adapter := routeFactAdapter(t, &orderQuoter{quote: quoteFixture(t), order: &order}, admitter)
	bound := adapter.BindBackendKinds(map[string]string{"a": "kind-a"})
	_, err := bound.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1"))
	if err == nil {
		t.Fatal("a refused preflight must fail the admission")
	}
	if !reflect.DeepEqual(order, []string{"preflight"}) {
		t.Fatalf("call order = %v, want preflight only", order)
	}
	if !errors.Is(err, billingbinding.ErrAdmission) {
		t.Fatalf("err = %v, want the adapter admission class", err)
	}
	if !errors.Is(err, errRefused) {
		t.Fatalf("err = %v, want the binding's own refusal class", err)
	}
}

// TestAdapterWithoutPreflighterKeepsTheQuotePath proves the refinement is
// optional: an admission port that does not implement it keeps the exact
// quote-then-admit path.
func TestAdapterWithoutPreflighterKeepsTheQuotePath(t *testing.T) {
	t.Parallel()
	order := []string{}
	adapter := routeFactAdapter(t, &orderQuoter{quote: quoteFixture(t), order: &order}, &countingAdmitter{order: &order})
	bound := adapter.BindBackendKinds(map[string]string{"a": "kind-a"})
	if _, err := bound.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1")); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"quote", "admit"}) {
		t.Fatalf("call order = %v, want quote, admit", order)
	}
}

// TestBoundAdapterSharesTheOwnedResourceLifecycle proves the generation copy
// shares the process adapter's owned-resource lifecycle: the resources start
// exactly once and close exactly once, whichever copy drives it. One process
// still owns one set of binding-owned resources.
func TestBoundAdapterSharesTheOwnedResourceLifecycle(t *testing.T) {
	t.Parallel()
	var starts, closes atomic.Int32
	binding := routeFactBinding(t, &fakeQuoter{quote: quoteFixture(t)}, &routeFactAdmitter{
		handle: sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"},
	})
	binding.Lifecycle = sdkbilling.Lifecycle{Owned: []sdkbilling.OwnedResource{{
		ID:    "worker-1",
		Start: func(context.Context) error { starts.Add(1); return nil },
		Close: func(context.Context) error { closes.Add(1); return nil },
	}}}
	adapter, err := billingbinding.NewAdapter(binding)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	bound := adapter.BindBackendKinds(map[string]string{"a": "kind-a"})
	if err := bound.StartOwnedResources(context.Background()); err != nil {
		t.Fatalf("StartOwnedResources: %v", err)
	}
	if err := bound.StartOwnedResources(context.Background()); err != nil {
		t.Fatalf("StartOwnedResources (repeat): %v", err)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("owned-resource starts = %d, want exactly one", got)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := bound.Close(); err != nil {
		t.Fatalf("Close (the generation copy): %v", err)
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("owned-resource closes = %d, want exactly one", got)
	}
}

// TestAdmissionRouteFactsSkipModelOnlyLeaves proves the facts name concrete
// backend instances only: a model-only planned leaf carries no instance and
// therefore no fact.
func TestAdmissionRouteFactsSkipModelOnlyLeaves(t *testing.T) {
	t.Parallel()
	admitter := &routeFactAdmitter{handle: sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"}}
	adapter := routeFactAdapter(t, &fakeQuoter{quote: quoteFixture(t)}, admitter)
	bound := adapter.BindBackendKinds(map[string]string{"a": "kind-a"})
	sel, err := routing.Parse("m1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}
	in := admitInput()
	in.Route = sel
	if _, err := bound.Admit(scopedCtx(validScope()), in); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if len(admitter.last.Routes) != 0 {
		t.Fatalf("admission routes = %+v, want none for a model-only plan", admitter.last.Routes)
	}
}
