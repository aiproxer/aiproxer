// Package main is a separate-module external billing binding certification
// fixture (Task 15.3, C7; requirements 7.1, 8.1, 8.5, 15.2-15.4, 18.3). It
// imports only public packages and stdlib: no internal/... imports.
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func scopedCtx() context.Context {
	return scope.WithScope(context.Background(), validScope())
}

func observationFixture(t *testing.T, widgetQty string, tokenQty string) metering.Observation {
	t.Helper()
	widget, err := metering.ParseDecimal(widgetQty)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := metering.ParseDecimal(tokenQty)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	obs := metering.Observation{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-ext-1",
		SourceEventKey: "evt-ext-1",
		Revision:       1,
		StreamID:       "stream-ext-1",
		Sequence:       1,
		Origin:         metering.OriginLocal,
		Acquisition:    metering.AcquisitionLocalTransport,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveCustomer,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject:        metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: testStore, BillingCallID: testCall, BLegID: testBLeg},
		Correlation:    metering.CorrelationV2{StoreID: testStore, BillingCallID: testCall, BLegID: testBLeg},
		Semantics:      metering.SemanticsDelta,
		ObservedAt:     now,
		ReceivedAt:     now,
		MappingRef:     "mapping-external-v1",
		Measures: []metering.Measure{
			{
				Key:     metering.ComponentKey{Direction: metering.DirectionNone, Component: widgetComponent, Unit: widgetUnit, SchemaID: widgetSchema},
				Value:   &widget,
				Quality: metering.QualityObserved,
			},
			{
				Key:     metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputTokenUncached, Unit: metering.UnitToken},
				Value:   &tokens,
				Quality: metering.QualityObserved,
			},
		},
	}
	if err := obs.Validate(); err != nil {
		t.Fatalf("observation fixture: %v", err)
	}
	return obs
}

func ratingInput(t *testing.T, tokenQty string) economics.RatingInput {
	t.Helper()
	obs := observationFixture(t, "5", tokenQty)
	ref, err := obs.Ref(testStore)
	if err != nil {
		t.Fatalf("observation ref: %v", err)
	}
	_ = ref
	return economics.RatingInput{
		Version:              1,
		Perspective:          metering.PerspectiveCustomer,
		Basis:                economics.BasisCustomerPolicy,
		Subject:              callSubject(),
		Observations:         []metering.Observation{obs},
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater-external", Version: "v1"}, RaterID: perSubmissionRaterID},
		RaterContent:         testContentRefPtr("rater"),
		Policy:               economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-external", Version: "v1"}, PolicyID: "per-submission-v1"},
		PolicyContent:        testContentRefPtr("policy"),
		QualifierSnapshotRef: testContentRefPtr("qualifiers"),
	}
}

// TestCustomOfferIgnoresProviderTokenPricing certifies requirement 8.1: the
// customer quote is a per-submission offer independent of provider token
// quantities. A 10-token and a 10M-token candidate receive the same maximum.
func TestCustomOfferIgnoresProviderTokenPricing(t *testing.T) {
	t.Parallel()
	small, err := testQuoter.Quote(context.Background(), quoteInput(10))
	if err != nil {
		t.Fatalf("small quote: %v", err)
	}
	large, err := testQuoter.Quote(context.Background(), quoteInput(10_000_000))
	if err != nil {
		t.Fatalf("large quote: %v", err)
	}
	if err := small.Validate(); err != nil {
		t.Fatalf("small quote invalid: %v", err)
	}
	if err := large.Validate(); err != nil {
		t.Fatalf("large quote invalid: %v", err)
	}
	if small.Maximum != large.Maximum {
		t.Fatalf("token-dependent quote: small=%+v large=%+v", small.Maximum, large.Maximum)
	}
	if !small.Maximum.Present || small.Maximum.NanoUnits != submissionFeeNano || small.Maximum.Currency != testCurrency {
		t.Fatalf("quote maximum = %+v, want 250 nano USD", small.Maximum)
	}
	if len(small.CreditUnits) != 1 || small.CreditUnits[0].Unit != creditUnit || !small.CreditUnits[0].Present {
		t.Fatalf("quote must carry the per-submission credit unit: %+v", small.CreditUnits)
	}
}

// TestCustomRaterChargesSubmissionAndWidgetNotTokens certifies requirements
// 7.1/8.1/8.5: one trusted-submission fee plus synthetic non-token
// passthrough, with provider token quantities explicitly unrated.
func TestCustomRaterChargesSubmissionAndWidgetNotTokens(t *testing.T) {
	t.Parallel()
	rich, err := testRater.Rate(context.Background(), ratingInput(t, "1000000"))
	if err != nil {
		t.Fatalf("rate: %v", err)
	}
	if err := rich.Validate(); err != nil {
		t.Fatalf("valuation invalid: %v", err)
	}
	if len(rich.Lines) != 2 {
		t.Fatalf("lines = %d, want submission fee + widget passthrough (no token line)", len(rich.Lines))
	}
	byID := map[string]economics.LineItem{}
	for _, line := range rich.Lines {
		byID[line.ID] = line
	}
	fee, ok := byID["line-submission-fee"]
	if !ok {
		t.Fatal("missing per-submission fee line")
	}
	if fee.FixedFee == nil || fee.FixedFee.Scope != economics.FixedFeeScopeSubmission {
		t.Fatalf("fee identity = %+v, want submission scope", fee.FixedFee)
	}
	if fee.RoundedAmount == nil || fee.RoundedAmount.NanoUnits != submissionFeeNano {
		t.Fatalf("fee rounded = %+v, want 250 nano", fee.RoundedAmount)
	}
	widget, ok := byID["line-widget-passthrough"]
	if !ok {
		t.Fatal("missing synthetic widget passthrough line")
	}
	if widget.RoundedAmount == nil || widget.RoundedAmount.NanoUnits != 5*widgetPriceNano {
		t.Fatalf("widget rounded = %+v, want 50 nano", widget.RoundedAmount)
	}
	if len(rich.Totals) != 1 || !rich.Totals[0].RoundedAmount.Present || rich.Totals[0].RoundedAmount.NanoUnits != submissionFeeNano+5*widgetPriceNano {
		t.Fatalf("totals = %+v, want 300 nano USD", rich.Totals)
	}
	lean, err := testRater.Rate(context.Background(), ratingInput(t, "0"))
	if err != nil {
		t.Fatalf("token-free rate: %v", err)
	}
	if len(lean.Lines) != 2 {
		t.Fatalf("token-free lines = %d, want fee + widget (widget is request evidence, not tokens)", len(lean.Lines))
	}
}

// TestBindingValidationRejectsBeforeStart certifies requirement 15.4: typed-
// nil, incomplete, and duplicate monetary bindings fail before any owned
// resource starts or anything is published.
func TestBindingValidationRejectsBeforeStart(t *testing.T) {
	t.Parallel()
	tracker := &lifecycleTracker{}
	fix, err := AssembleBinding(tracker)
	if err != nil {
		t.Fatalf("AssembleBinding: %v", err)
	}
	good := fix.Binding
	if err := good.Validate(); err != nil {
		t.Fatalf("assembled binding invalid: %v", err)
	}
	cases := map[string]func(*billing.Binding){
		"nil screen":    func(b *billing.Binding) { b.CreditScreen = nil },
		"typed-nil":     func(b *billing.Binding) { b.Terminal = (*terminalLog)(nil) },
		"bad version":   func(b *billing.Binding) { b.Version = 0 },
		"missing scope": func(b *billing.Binding) { b.Scope = billing.BindingScope{} },
		"bad lifecycle": func(b *billing.Binding) { b.Lifecycle.Owned[0].Close = nil },
	}
	for name, mutate := range cases {
		bad := good
		// Copy the owned slice: Binding is a value but shares the backing
		// array, so per-case mutation must not corrupt the fixture.
		bad.Lifecycle = good.Lifecycle.Clone()
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatalf("%s: expected validation failure", name)
		}
	}
	if err := billing.ValidateBindings([]billing.Binding{good, good}); !errors.Is(err, billing.ErrDuplicateMonetaryBinding) {
		t.Fatalf("duplicate bindings err=%v, want ErrDuplicateMonetaryBinding", err)
	}
	if tracker.totalStarts() != 0 || tracker.totalCloses() != 0 {
		t.Fatalf("validation must not start resources: %+v", tracker.snapshot())
	}
}

// TestBindingOptionalMaintenanceAttachment is the public-import-only smoke case
// for the additive provider-maintenance attachment: absent stays valid, an
// attached observer stays valid with the version unchanged, and a typed-nil
// observer is rejected before start.
func TestBindingOptionalMaintenanceAttachment(t *testing.T) {
	t.Parallel()
	fix, err := AssembleBinding(&lifecycleTracker{})
	if err != nil {
		t.Fatalf("AssembleBinding: %v", err)
	}
	if fix.Binding.Maintenance != nil {
		t.Fatal("fixture must not declare a maintenance observer")
	}
	if err := fix.Binding.Validate(); err != nil {
		t.Fatalf("absent attachment must stay valid: %v", err)
	}
	attached := fix.Binding
	observer := billing.MaintenanceObserverFunc(func(context.Context, billing.MaintenanceUsage) error { return nil })
	attached.Maintenance = observer
	if err := attached.Validate(); err != nil {
		t.Fatalf("attached observer must stay valid: %v", err)
	}
	if attached.Version != billing.BindingVersionV1 {
		t.Fatalf("version = %d, want BindingVersionV1", attached.Version)
	}
	typedNil := fix.Binding
	typedNil.Maintenance = (*maintenanceSmoke)(nil)
	if err := typedNil.Validate(); !errors.Is(err, billing.ErrInvalidBinding) {
		t.Fatalf("typed-nil observer err=%v, want ErrInvalidBinding", err)
	}
}

// maintenanceSmoke is the public-only typed-nil observer fixture.
type maintenanceSmoke struct{}

func (*maintenanceSmoke) ObserveProviderMaintenance(context.Context, billing.MaintenanceUsage) error {
	return nil
}

// TestCreditScreenAllowDeny exercises the cheap-screen port directly: known
// principals on the bound account pass, unknown scope fails closed.
func TestCreditScreenAllowDeny(t *testing.T) {
	t.Parallel()
	allowed := billing.CreditScreenInput{Scope: validScope(), StoreID: testStore, AccountID: testAccount, Policy: offerPolicyRef()}
	res, err := testScreen.Check(scopedCtx(), allowed)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if res.Decision != billing.CreditAllow {
		t.Fatalf("decision = %q, want allow", res.Decision)
	}
	denied := allowed
	denied.AccountID = "acct-stranger"
	res, err = testScreen.Check(scopedCtx(), denied)
	if err != nil {
		t.Fatalf("deny transport: %v", err)
	}
	if res.Decision != billing.CreditDeny {
		t.Fatalf("decision = %q, want deny", res.Decision)
	}
	anon := allowed
	anon.Scope = scope.PrincipalScopeView{}
	if _, err := testScreen.Check(scopedCtx(), anon); err == nil {
		t.Fatal("anonymous scope must fail")
	}
}

// TestTerminalAckMonotonic certifies the terminal handoff port: envelopes are
// recorded, acknowledgements carry strictly increasing durable revisions,
// and invalid envelopes never consume a revision.
func TestTerminalAckMonotonic(t *testing.T) {
	t.Parallel()
	before := testTerminal.recorded()
	firstEnv, err := terminalEnvelopeFixture("env-ack-1", nil)
	if err != nil {
		t.Fatalf("envelope fixture: %v", err)
	}
	first, err := testTerminal.AppendTerminal(context.Background(), firstEnv)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	secondEnv, err := terminalEnvelopeFixture("env-ack-2", nil)
	if err != nil {
		t.Fatalf("envelope fixture: %v", err)
	}
	second, err := testTerminal.AppendTerminal(context.Background(), secondEnv)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if second.Revision != first.Revision+1 {
		t.Fatalf("revisions = %d/%d, want monotonic +1", first.Revision, second.Revision)
	}
	bad, err := terminalEnvelopeFixture("env-ack-bad", nil)
	if err != nil {
		t.Fatalf("envelope fixture: %v", err)
	}
	bad.PayloadHash = "not-a-hash"
	if _, err := testTerminal.AppendTerminal(context.Background(), bad); err == nil {
		t.Fatal("invalid envelope must be rejected")
	}
	if got := testTerminal.recorded(); got != before+2 {
		t.Fatalf("recorded envelopes = %d, want %d (rejected envelope not recorded)", got, before+2)
	}
}

// TestAdmissionAdmitsFrozenQuote exercises the atomic admission port: the
// frozen quote becomes an admitted handle bound to the call identity.
func TestAdmissionAdmitsFrozenQuote(t *testing.T) {
	t.Parallel()
	quote, err := testQuoter.Quote(context.Background(), quoteInput(100))
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	in := billing.ExposureAdmissionInput{
		Subject:         callSubject(),
		Scope:           validScope(),
		AccountID:       testAccount,
		Quote:           quote,
		ExecutionLimits: quoteLimits(100, 2048),
	}
	handle, err := testAdmission.Admit(context.Background(), in)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := handle.Validate(); err != nil {
		t.Fatalf("handle invalid: %v", err)
	}
	if handle.BillingCallID != testCall || handle.StoreID != testStore {
		t.Fatalf("handle = %+v, want call/store binding", handle)
	}
	bad := in
	bad.ExecutionLimits = nil
	if _, err := testAdmission.Admit(context.Background(), bad); err == nil {
		t.Fatal("limit-free admission must fail")
	}
}
