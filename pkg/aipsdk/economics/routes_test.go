package economics_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// referenceFactsStub pins the optional ReferenceFacts quoter port shape at the
// public boundary: customer pricing, charge policy, and per-route operator
// rate references resolved without a call value.
type referenceFactsStub struct{}

func (referenceFactsStub) CustomerPricingRef(context.Context) economics.VersionRef {
	return economics.VersionRef{ID: "pricing", Version: "v1"}
}

func (referenceFactsStub) ChargePolicyRef(context.Context) economics.VersionRef {
	return economics.VersionRef{ID: "policy", Version: "v1"}
}

func (referenceFactsStub) OperatorRateRef(context.Context, string, string) economics.VersionRef {
	return economics.VersionRef{}
}

var _ economics.ReferenceFacts = referenceFactsStub{}

func TestRouteTariffKeyTrimsIdentityFields(t *testing.T) {
	t.Parallel()
	if got := economics.RouteTariffKey(" a ", " m1 "); got != "a:m1" {
		t.Fatalf("key = %q, want a:m1", got)
	}
	if got := economics.RouteTariffKey("", "m1"); got != ":m1" {
		t.Fatalf("model-only key = %q, want :m1", got)
	}
}

func TestRouteCandidateValidate(t *testing.T) {
	t.Parallel()
	valid := economics.RouteCandidate{Backend: "a", Model: "m1"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid candidate: %v", err)
	}
	// Model-only selectors have an empty backend; the model stays required.
	if err := (economics.RouteCandidate{Model: "m1"}).Validate(); err != nil {
		t.Fatalf("model-only candidate: %v", err)
	}
	for name, candidate := range map[string]economics.RouteCandidate{
		"empty model":      {Backend: "a"},
		"padded model":     {Backend: "a", Model: " m1 "},
		"padded backend":   {Backend: " a ", Model: "m1"},
		"oversized model":  {Model: strings.Repeat("m", economics.MaxValuationTextBytes+1)},
		"unsafe backend":   {Backend: "a\x00b", Model: "m1"},
		"model with space": {Model: " "},
	} {
		if err := candidate.Validate(); err == nil {
			t.Fatalf("%s: expected validation failure", name)
		}
	}
	clone := valid.Clone()
	clone.Backend = "b"
	if valid.Backend != "a" {
		t.Fatal("Clone must return an independent value")
	}
}

func TestRouteTariffBindingValidate(t *testing.T) {
	t.Parallel()
	valid := economics.RouteTariffBinding{
		RouteID: "a:m1", TariffID: "tariff-1", TariffVersion: "v1",
		ContentHash: strings.Repeat("a", 64),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid binding: %v", err)
	}
	for name, binding := range map[string]economics.RouteTariffBinding{
		"missing route":        {TariffID: "t", TariffVersion: "v1", ContentHash: strings.Repeat("a", 64)},
		"missing tariff":       {RouteID: "a:m1", TariffVersion: "v1", ContentHash: strings.Repeat("a", 64)},
		"missing version":      {RouteID: "a:m1", TariffID: "t", ContentHash: strings.Repeat("a", 64)},
		"uppercase hash":       {RouteID: "a:m1", TariffID: "t", TariffVersion: "v1", ContentHash: strings.Repeat("A", 64)},
		"short hash":           {RouteID: "a:m1", TariffID: "t", TariffVersion: "v1", ContentHash: "abc"},
		"non-hex hash":         {RouteID: "a:m1", TariffID: "t", TariffVersion: "v1", ContentHash: strings.Repeat("z", 64)},
		"padded route":         {RouteID: " a:m1", TariffID: "t", TariffVersion: "v1", ContentHash: strings.Repeat("a", 64)},
		"model-only route key": {RouteID: "", TariffID: "t", TariffVersion: "v1", ContentHash: strings.Repeat("a", 64)},
	} {
		if err := binding.Validate(); err == nil {
			t.Fatalf("%s: expected validation failure", name)
		}
	}
	if clone := valid.Clone(); clone != valid {
		t.Fatal("Clone must preserve the binding identity")
	}
	if !economics.RouteTariffModeNone.IsKnown() || !economics.RouteTariffModeFrozen.IsKnown() {
		t.Fatal("none and frozen must be documented modes")
	}
	if economics.RouteTariffMode("other").IsKnown() {
		t.Fatal("unknown modes must not be known")
	}
}

func quoteInputFixture() economics.QuoteInput {
	return economics.QuoteInput{
		Version:         1,
		Subject:         metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: "store-test", BillingCallID: "call-1"},
		CandidateLimits: []economics.Limit{{Name: "input_tokens", Value: 10, Unit: "token"}},
		Tariff:          economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"}},
		Policy:          economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"}, PolicyID: "policy-1"},
	}
}

func validRouteTariffBinding() economics.RouteTariffBinding {
	return economics.RouteTariffBinding{
		RouteID: "a:m1", TariffID: "tariff-1", TariffVersion: "v1",
		ContentHash: strings.Repeat("a", 64),
	}
}

// TestQuoteInputRoutesKeepOrderedOccurrences locks the planned-route carriage:
// an ordered occurrence list with duplicates retained exactly as planned; the
// list is never deduplicated and stays bound.
func TestQuoteInputRoutesKeepOrderedOccurrences(t *testing.T) {
	t.Parallel()
	in := quoteInputFixture()
	in.Routes = []economics.RouteCandidate{
		{Backend: "a", Model: "m1"},
		{Backend: "a", Model: "m1"},
		{Model: "m2"},
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("ordered occurrence list: %v", err)
	}
	clone := in.Clone()
	clone.Routes[0].Model = "mutated"
	if in.Routes[0].Model != "m1" {
		t.Fatal("Clone must not share route candidate storage")
	}
	over := quoteInputFixture()
	over.Routes = make([]economics.RouteCandidate, economics.MaxQuoteRoutes+1)
	for i := range over.Routes {
		over.Routes[i] = economics.RouteCandidate{Model: "m1"}
	}
	if err := over.Validate(); err == nil {
		t.Fatal("route candidate bound must fail closed")
	}
	bad := quoteInputFixture()
	bad.Routes = []economics.RouteCandidate{{Model: ""}}
	if err := bad.Validate(); err == nil {
		t.Fatal("invalid route candidates must fail closed")
	}
}

// TestExposureQuoteRouteTariffModeConsistency locks the explicit quote-level
// mode: none requires no bindings, frozen requires at least one, bindings
// without a mode fail closed, and the legacy mode-less empty quote stays valid.
func TestExposureQuoteRouteTariffModeConsistency(t *testing.T) {
	t.Parallel()
	base := func() economics.ExposureQuote {
		return economics.ExposureQuote{
			Version:      1,
			Subject:      metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: "store-test", BillingCallID: "call-1"},
			Tariff:       economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"}},
			Policy:       economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"}, PolicyID: "policy-1"},
			Completeness: economics.CompletenessComplete,
		}
	}
	valid := map[string]func(*economics.ExposureQuote){
		"legacy mode-less empty": func(q *economics.ExposureQuote) {},
		"none with empty bindings": func(q *economics.ExposureQuote) {
			q.RouteTariffMode = economics.RouteTariffModeNone
		},
		"frozen with one binding": func(q *economics.ExposureQuote) {
			q.RouteTariffMode = economics.RouteTariffModeFrozen
			q.RouteTariffs = []economics.RouteTariffBinding{validRouteTariffBinding()}
		},
	}
	for name, mutate := range valid {
		q := base()
		mutate(&q)
		if err := q.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	invalid := map[string]func(*economics.ExposureQuote){
		"bindings without mode": func(q *economics.ExposureQuote) {
			q.RouteTariffs = []economics.RouteTariffBinding{validRouteTariffBinding()}
		},
		"none with bindings": func(q *economics.ExposureQuote) {
			q.RouteTariffMode = economics.RouteTariffModeNone
			q.RouteTariffs = []economics.RouteTariffBinding{validRouteTariffBinding()}
		},
		"frozen without bindings": func(q *economics.ExposureQuote) {
			q.RouteTariffMode = economics.RouteTariffModeFrozen
		},
		"unknown mode": func(q *economics.ExposureQuote) {
			q.RouteTariffMode = economics.RouteTariffMode("partial")
		},
		"invalid binding": func(q *economics.ExposureQuote) {
			binding := validRouteTariffBinding()
			binding.ContentHash = "not-a-hash"
			q.RouteTariffMode = economics.RouteTariffModeFrozen
			q.RouteTariffs = []economics.RouteTariffBinding{binding}
		},
	}
	for name, mutate := range invalid {
		q := base()
		mutate(&q)
		if err := q.Validate(); err == nil {
			t.Fatalf("%s: expected validation failure", name)
		}
	}
	q := base()
	q.RouteTariffMode = economics.RouteTariffModeFrozen
	q.RouteTariffs = []economics.RouteTariffBinding{validRouteTariffBinding()}
	clone := q.Clone()
	clone.RouteTariffs[0].TariffID = "mutated"
	if q.RouteTariffs[0].TariffID != "tariff-1" {
		t.Fatal("Clone must not share route tariff binding storage")
	}
}
