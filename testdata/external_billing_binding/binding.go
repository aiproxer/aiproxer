// Package main is a separate-module external billing binding certification
// fixture (Task 15.3, C7; requirements 7.1, 8.1, 8.5, 15.2-15.4, 18.3). It
// imports only public packages and stdlib: no internal/... imports.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

const (
	// widgetComponent is the synthetic non-token billable component. It is
	// schema-qualified so it needs no provider registry or core change.
	widgetComponent = "widget_call"
	widgetUnit      = "widget"
	widgetSchema    = "external-billing-test/v1"
	// creditUnit is the non-monetary per-submission allowance unit.
	creditUnit = "submission_credit"
	// perSubmissionRaterID identifies the custom customer rater.
	perSubmissionRaterID = "per-submission-rater"
	// submissionFeeNano is the fixed per-submission charge in nano units.
	submissionFeeNano = 250
	// widgetPriceNano is the passthrough price per synthetic widget.
	widgetPriceNano = 10
)

const (
	testStore    = "external-billing-store"
	testCall     = "call-ext-1"
	testBLeg     = "bleg-ext-1"
	testTenant   = "tenant-ext-1"
	testAccount  = "acct-external-1"
	testCurrency = "USD"
)

// Shared stateless port implementations used by every test in this module.
// terminalLog alone carries mutex-guarded state and is safe for parallel use.
var (
	testScreen    = &offerScreen{allowedAccount: testAccount}
	testQuoter    = &offerQuoter{}
	testRater     = &offerRater{}
	testAdmission = &offerAdmission{}
	testTerminal  = &terminalLog{}
)

func testContentRef(label string) economics.SnapshotContentRef {
	sum := sha256.Sum256([]byte("external-billing-binding:" + label))
	return economics.SnapshotContentRef{ContentRef: label, ContentHash: hex.EncodeToString(sum[:])}
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func testContentRefPtr(label string) *economics.SnapshotContentRef {
	ref := testContentRef(label)
	return &ref
}

func offerScope() billing.BindingScope {
	return billing.BindingScope{
		StoreID: testStore,
		Tariff:  economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-external", Version: "v1"}},
		Policy:  economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-external", Version: "v1"}, PolicyID: "per-submission-v1"},
	}
}

func offerPolicyRef() economics.PolicySnapshotRef {
	return offerScope().Policy
}

// validScope is the module's trusted customer scope fixture, shared by the
// offer implementation and its tests.
func validScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-external-1"),
		TenantID:    scope.Known(testTenant),
		Origin:      scope.OriginClient,
	}
}

func quoteLimits(tokens int64, maxOut int64) []economics.Limit {
	return []economics.Limit{
		{Name: "input_tokens", Value: tokens, Unit: "token"},
		{Name: "max_output_tokens", Value: maxOut, Unit: "token"},
	}
}

func quoteInput(tokens int64) economics.QuoteInput {
	return economics.QuoteInput{
		Version:         1,
		Subject:         callSubject(),
		CandidateLimits: quoteLimits(tokens, 2048),
		Tariff:          economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-external", Version: "v1"}},
		Policy:          economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-external", Version: "v1"}, PolicyID: "per-submission-v1"},
	}
}

func callSubject() metering.SubjectRef {
	return metering.SubjectRef{
		Kind:          metering.SubjectBillingCall,
		StoreID:       testStore,
		BillingCallID: testCall,
	}
}

// offerScreen is the cheap credit gate: known principals on the bound
// account pass, everyone else is denied, anonymous scope fails closed.
type offerScreen struct {
	allowedAccount string
	mu             sync.Mutex
	calls          int
}

func (s *offerScreen) Check(_ context.Context, in billing.CreditScreenInput) (billing.CreditScreenResult, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if err := in.Validate(); err != nil {
		return billing.CreditScreenResult{}, err
	}
	if in.AccountID != s.allowedAccount {
		return billing.CreditScreenResult{Decision: billing.CreditDeny, StoreID: in.StoreID, AccountID: in.AccountID}, nil
	}
	return billing.CreditScreenResult{Decision: billing.CreditAllow, StoreID: in.StoreID, AccountID: in.AccountID}, nil
}

// callsValue reports how many checks ran.
func (s *offerScreen) callsValue() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// offerQuoter is the per-submission customer quoter. The maximum is a fixed
// per-call offer that never varies with provider token quantities.
type offerQuoter struct {
	mu    sync.Mutex
	calls int
	last  economics.QuoteInput
}

func (q *offerQuoter) Quote(_ context.Context, in economics.QuoteInput) (economics.ExposureQuote, error) {
	q.mu.Lock()
	q.calls++
	q.last = in.Clone()
	q.mu.Unlock()
	if err := in.Validate(); err != nil {
		return economics.ExposureQuote{}, err
	}
	sc := offerScope()
	unit, err := metering.ParseDecimal("1")
	if err != nil {
		return economics.ExposureQuote{}, err
	}
	quote := economics.ExposureQuote{
		ID:                   "quote-external-1",
		Version:              1,
		Basis:                economics.BasisCustomerPolicy,
		Subject:              in.Subject.Clone(),
		Minimum:              economics.Money{NanoUnits: submissionFeeNano, Currency: testCurrency, Present: true},
		Maximum:              economics.Money{NanoUnits: submissionFeeNano, Currency: testCurrency, Present: true},
		CreditBound:          economics.Money{NanoUnits: submissionFeeNano, Currency: testCurrency, Present: true},
		CreditUnits:          []economics.UnitBound{{Unit: creditUnit, Amount: &unit, Present: true}},
		Assumptions:          []economics.QuoteAssumption{{Name: "pricing", Value: "per-submission-fixed"}},
		RequiredCapabilities: []economics.EvidenceCapability{{Name: "trusted_submission", Required: true}},
		Tariff:               sc.Tariff,
		TariffContent:        testContentRefPtr("tariff"),
		Policy:               sc.Policy,
		PolicyContent:        testContentRefPtr("policy"),
		InputSetHash:         hashHex("quote-inputs"),
		QualifierSnapshotRef: testContentRefPtr("qualifiers"),
		Completeness:         economics.CompletenessComplete,
	}
	if err := quote.Validate(); err != nil {
		return economics.ExposureQuote{}, err
	}
	return quote, nil
}

// callsValue reports how many quotes ran.
func (q *offerQuoter) callsValue() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.calls
}

// lastSubject reports the BillingCallID of the most recent quote input.
func (q *offerQuoter) lastSubject() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.last.Subject.BillingCallID
}

// offerRater is the custom customer rater: one trusted-submission fixed fee
// plus synthetic non-token passthrough. Provider token quantities are
// explicitly unrated and never become lines.
type offerRater struct{}

func (offerRater) Rate(_ context.Context, in economics.RatingInput) (economics.Valuation, error) {
	if err := in.Validate(); err != nil {
		return economics.Valuation{}, err
	}
	if in.Basis != economics.BasisCustomerPolicy {
		return economics.Valuation{}, fmt.Errorf("external offer: unsupported basis %q", in.Basis)
	}
	widgets, err := sumWidgetQuantities(in.Observations)
	if err != nil {
		return economics.Valuation{}, err
	}
	feeAmount, err := metering.ParseDecimal("0.00000025")
	if err != nil {
		return economics.Valuation{}, err
	}
	feeRounded := economics.Money{NanoUnits: submissionFeeNano, Currency: testCurrency, Present: true}
	lines := []economics.LineItem{{
		ID:             "line-submission-fee",
		RuleID:         "per-submission-fee",
		ItemID:         "submission-fee",
		FixedFee:       &economics.FixedFeeIdentity{ID: "submission-fee", Scope: economics.FixedFeeScopeSubmission},
		Unit:           "submission",
		Amount:         &feeAmount,
		RoundedAmount:  &feeRounded,
		RoundingScope:  economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfAwayFromZero,
		Status:         economics.RatingLineRated,
		ChargeKind:     "fixed_fee",
	}}
	widgetNano := int64(0)
	if widgets.Sign() > 0 {
		count, err := widgetInt64(widgets)
		if err != nil {
			return economics.Valuation{}, err
		}
		qty, err := metering.Decimal{Coefficient: strconv.FormatInt(count, 10), Scale: 0}.Normalize()
		if err != nil {
			return economics.Valuation{}, err
		}
		price, err := nanoDecimal(widgetPriceNano)
		if err != nil {
			return economics.Valuation{}, err
		}
		widgetAmount, err := nanoDecimal(count * widgetPriceNano)
		if err != nil {
			return economics.Valuation{}, err
		}
		widgetNano = count * widgetPriceNano
		widgetRounded := economics.Money{NanoUnits: widgetNano, Currency: testCurrency, Present: true}
		lines = append(lines, economics.LineItem{
			ID:             "line-widget-passthrough",
			RuleID:         "widget-passthrough",
			ItemID:         "widget-passthrough",
			Component:      &metering.ComponentKey{Direction: metering.DirectionNone, Component: widgetComponent, Unit: widgetUnit, SchemaID: widgetSchema},
			Quantity:       &qty,
			Unit:           widgetUnit,
			UnitPrice:      &price,
			Amount:         &widgetAmount,
			RoundedAmount:  &widgetRounded,
			RoundingScope:  economics.RoundingScopeLine,
			RoundingPolicy: economics.RoundingHalfAwayFromZero,
			Status:         economics.RatingLineRated,
			ChargeKind:     "component",
		})
	}
	totalNano := submissionFeeNano + widgetNano
	totalAmount, err := nanoDecimal(totalNano)
	if err != nil {
		return economics.Valuation{}, err
	}
	valuation := economics.Valuation{
		ID:                   "valuation-external-1",
		Version:              economics.ValuationVersionV2,
		Perspective:          in.Perspective,
		Basis:                economics.BasisCustomerPolicy,
		Subject:              in.Subject.Clone(),
		InputObservations:    observationRefs(in.Observations),
		InputSetHash:         hashHex("valuation-inputs"),
		Rater:                in.Rater,
		RaterContent:         in.RaterContent,
		Policy:               in.Policy,
		PolicyContent:        in.PolicyContent,
		QualifierSnapshotRef: in.QualifierSnapshotRef,
		Payer:                in.Payer,
		Lines:                lines,
		Totals: []economics.CurrencyTotal{{
			Currency:      testCurrency,
			Amount:        &totalAmount,
			RoundedAmount: economics.Money{NanoUnits: totalNano, Currency: testCurrency, Present: true},
		}},
		Completeness: economics.CompletenessComplete,
		CreatedAt:    time.Now().UTC(),
	}
	if err := valuation.Validate(); err != nil {
		return economics.Valuation{}, err
	}
	return valuation, nil
}

// sumWidgetQuantities adds exact integer quantities of the synthetic
// component across all observations. Non-integer widget evidence is rejected.
func sumWidgetQuantities(observations []metering.Observation) (*big.Int, error) {
	total := new(big.Int)
	for _, obs := range observations {
		for _, measure := range obs.Measures {
			if measure.Key.Component != widgetComponent || measure.Value == nil {
				continue
			}
			rat, err := measure.Value.ToRat()
			if err != nil {
				return nil, err
			}
			if !rat.IsInt() {
				return nil, fmt.Errorf("external offer: non-integer widget quantity %v", rat)
			}
			total.Add(total, rat.Num())
		}
	}
	return total, nil
}

// nanoDecimal renders an integer nano-unit amount as canonical major-unit
// decimal money math (scale 9).
func nanoDecimal(nano int64) (metering.Decimal, error) {
	return metering.Decimal{Coefficient: strconv.FormatInt(nano, 10), Scale: 9}.Normalize()
}

// widgetInt64 converts an exact integer quantity to int64, rejecting overflow.
func widgetInt64(v *big.Int) (int64, error) {
	if v == nil || !v.IsInt64() {
		return 0, fmt.Errorf("external offer: widget quantity out of range")
	}
	return v.Int64(), nil
}

func observationRefs(observations []metering.Observation) []metering.ObservationRef {
	refs := make([]metering.ObservationRef, 0, len(observations))
	for _, obs := range observations {
		ref, err := obs.Ref(obs.Subject.StoreID)
		if err != nil {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

// offerAdmission admits the frozen quote into a call-scoped handle.
type offerAdmission struct {
	mu       sync.Mutex
	admitted int
	last     billing.ExposureAdmissionInput
}

func (a *offerAdmission) Admit(_ context.Context, in billing.ExposureAdmissionInput) (billing.ExposureHandle, error) {
	if err := in.Validate(); err != nil {
		return billing.ExposureHandle{}, err
	}
	a.mu.Lock()
	a.admitted++
	n := a.admitted
	a.last = in.Clone()
	a.mu.Unlock()
	handle := billing.ExposureHandle{
		StoreID:       in.Subject.StoreID,
		BillingCallID: in.Subject.BillingCallID,
		ExposureID:    "exp-external-" + strconv.Itoa(n),
		QuoteID:       in.Quote.ID,
	}
	if handle.QuoteID == "" {
		handle.QuoteID = "quote-external-1"
	}
	if err := handle.Validate(); err != nil {
		return billing.ExposureHandle{}, err
	}
	return handle, nil
}

// callsValue reports how many admissions ran.
func (a *offerAdmission) callsValue() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admitted
}

// lastCall reports the BillingCallID of the most recent admission input.
func (a *offerAdmission) lastCall() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last.Subject.BillingCallID
}

// lastScopeAccount reports the trusted scope principal and credited account
// of the most recent admission input.
func (a *offerAdmission) lastScopeAccount() (principal, account string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last.Scope.PrincipalID.String(), a.last.AccountID
}

// terminalLog records terminal envelopes and acknowledges with strictly
// increasing durable revisions.
type terminalLog struct {
	mu        sync.Mutex
	revision  uint64
	envelopes []billing.TerminalEnvelope
	acks      []billing.TerminalAck
}

func (l *terminalLog) AppendTerminal(_ context.Context, env billing.TerminalEnvelope) (billing.TerminalAck, error) {
	if err := env.Validate(); err != nil {
		return billing.TerminalAck{}, err
	}
	l.mu.Lock()
	l.revision++
	ack := billing.TerminalAck{StoreID: env.StoreID, EnvelopeID: env.EnvelopeID, Revision: l.revision}
	l.envelopes = append(l.envelopes, env.Clone())
	l.acks = append(l.acks, ack)
	l.mu.Unlock()
	if err := ack.Validate(); err != nil {
		return billing.TerminalAck{}, err
	}
	return ack, nil
}

func (l *terminalLog) recorded() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.envelopes)
}

// envelopeAt returns an independent copy of the i-th recorded envelope.
func (l *terminalLog) envelopeAt(i int) billing.TerminalEnvelope {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.envelopes[i].Clone()
}

// ackAt returns the acknowledgement recorded for the i-th envelope.
func (l *terminalLog) ackAt(i int) billing.TerminalAck {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acks[i].Clone()
}

// drainSince returns envelopes (with parallel ack revisions) acknowledged
// after the cursor revision, in acknowledgement order.
func (l *terminalLog) drainSince(cursor uint64) (envs []billing.TerminalEnvelope, revisions []uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, ack := range l.acks {
		if ack.Revision > cursor {
			envs = append(envs, l.envelopes[i].Clone())
			revisions = append(revisions, ack.Revision)
		}
	}
	return envs, revisions
}

func terminalEnvelopeFixture(id string, obs []metering.Observation) (billing.TerminalEnvelope, error) {
	env := billing.TerminalEnvelope{
		Version:    billing.TerminalEnvelopeVersionV1,
		EnvelopeID: id,
		StoreID:    testStore,
		Scope:      validScope(),
		Subject: metering.SubjectRef{
			Kind:          metering.SubjectBLeg,
			StoreID:       testStore,
			BillingCallID: testCall,
			BLegID:        testBLeg,
			AttemptSeq:    3,
			ALegID:        "aleg-ext-1",
		},
		Correlation: metering.CorrelationV2{
			StoreID:       testStore,
			BillingCallID: testCall,
			BLegID:        testBLeg,
			AttemptSeq:    3,
			ALegID:        "aleg-ext-1",
		},
		Outcome:      billing.TerminalCompleted,
		AttemptSeq:   3,
		ALegID:       "aleg-ext-1",
		Tariff:       offerScope().Tariff,
		Policy:       offerScope().Policy,
		Observations: obs,
		PayloadHash:  hashHex("payload-" + id),
	}
	if err := env.Validate(); err != nil {
		return billing.TerminalEnvelope{}, fmt.Errorf("terminal envelope fixture: %w", err)
	}
	return env, nil
}
