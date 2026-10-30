package economics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

const (
	// ValuationVersionV2 is the first version of the immutable valuation
	// envelope. A valuation is a derived record and never replaces an input
	// observation.
	//nolint:staticcheck // ValuationVersionV2 intentionally carries the explicit uint32 wire width used across 100+ call sites; the Max* limits stay untyped for direct use in int cardinality contexts
	ValuationVersionV2         uint32 = 2
	MaxValuationLines                 = 128
	MaxValuationTotals                = 32
	MaxValuationRefs                  = 1024
	MaxValuationTextBytes             = 512
	MaxValuationRationalDigits        = 128
)

var (
	ErrInvalidValuation = errors.New("economics: invalid valuation")
	ErrInvalidRating    = errors.New("economics: invalid rating input")
	ErrInvalidQuote     = errors.New("economics: invalid quote")
)

// validatePublicRef is the V2-only wrapper around ValidateSafeRef. The latter
// remains unchanged for V1 callers and accepts the Go string representation of
// malformed bytes; V2 identity must reject those bytes before any JSON
// canonicalization can replace them with U+FFFD.
func validatePublicRef(field, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("economics: %s contains invalid UTF-8", field)
	}
	if err := ValidateSafeRef(field, value); err != nil {
		return err
	}
	if len(value) > MaxValuationTextBytes {
		return fmt.Errorf("economics: %s exceeds %d bytes", field, MaxValuationTextBytes)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("economics: %s must not have surrounding whitespace", field)
	}
	return nil
}

// validateV2Money keeps V2's presence semantics stricter than the historical
// Money validator. An absent amount has no economic value and therefore cannot
// carry nonzero nanos or a currency that could be mistaken for a known amount.
// Present values continue to use Money's checked currency/nano validation.
func validateV2Money(field string, money Money) error {
	if !money.Present {
		if money.NanoUnits != 0 || money.Currency != "" {
			return fmt.Errorf("economics: %s absent money cannot carry nano units or currency", field)
		}
		return nil
	}
	if err := money.Validate(); err != nil {
		return fmt.Errorf("economics: %s: %v", field, err)
	}
	return nil
}

type observationRevisionIdentity struct {
	store       string
	observation string
	revision    uint64
}

// validateObservationRefCollection validates only the references supplied by
// this DTO. It deliberately does not resolve the referenced observations: a
// late or external observation is valid input here, while a resolver with the
// referenced records must establish a closed graph before rating/posting.
func validateObservationRefCollection(refs []metering.ObservationRef, expectedStore, label string) error {
	seen := make(map[observationRevisionIdentity]string, len(refs))
	for i, ref := range refs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("economics: %s ref %d: %v", label, i, err)
		}
		if expectedStore != "" && ref.StoreID != expectedStore {
			return fmt.Errorf("economics: %s ref store mismatch", label)
		}
		key := observationRevisionIdentity{store: ref.StoreID, observation: ref.ObservationID, revision: ref.Revision}
		if priorHash, ok := seen[key]; ok {
			if priorHash != ref.PayloadHash {
				return fmt.Errorf("economics: conflicting payload hashes for %s observation revision", label)
			}
			return fmt.Errorf("economics: duplicate %s observation ref", label)
		}
		seen[key] = ref.PayloadHash
	}
	return nil
}

type coverageEdgeIdentity struct {
	store       string
	observation string
	revision    uint64
	item        string
}

// validateCoverageRefCollection checks the edge identities represented by a
// refs-only DTO. Since ChargeCoverageRef has no embedded child record, missing
// external nodes remain pending for the owning resolver; duplicate and
// contradictory local edges are still rejected immediately.
func validateCoverageRefCollection(refs []metering.ChargeCoverageRef, expectedStore, label string) error {
	seen := make(map[coverageEdgeIdentity]metering.CoverageRelation, len(refs))
	for i, ref := range refs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("economics: %s %d: %v", label, i, err)
		}
		if expectedStore != "" && ref.Ref.StoreID != expectedStore {
			return fmt.Errorf("economics: %s store mismatch", label)
		}
		key := coverageEdgeIdentity{
			store:       ref.Ref.StoreID,
			observation: ref.Ref.ObservationID,
			revision:    ref.Ref.Revision,
			item:        ref.Ref.ChargeItemID,
		}
		if priorRelation, ok := seen[key]; ok {
			if priorRelation != ref.Relation {
				return fmt.Errorf("economics: contradictory %s edge", label)
			}
			return fmt.Errorf("economics: duplicate %s edge", label)
		}
		seen[key] = ref.Relation
	}
	return nil
}

type adjustmentIdentity struct {
	store     string
	valuation string
	revision  uint64
	operation string
}

func validateAdjustmentRefCollection(refs []AdjustmentRef, label string) error {
	seen := make(map[adjustmentIdentity]struct{}, len(refs))
	for i, ref := range refs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("economics: %s ref %d: %v", label, i, err)
		}
		key := adjustmentIdentity{store: ref.StoreID, valuation: ref.ValuationID, revision: ref.Revision, operation: ref.OperationID}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("economics: duplicate %s ref", label)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ValuationBasis identifies the economic plane represented by a valuation.
// The values intentionally distinguish locally derived Q from provider P and
// statement S; equal amounts do not make those claims interchangeable.
type ValuationBasis string

const (
	BasisLocalExpected         ValuationBasis = "local_expected"          // E
	BasisProviderQuantityLocal ValuationBasis = "provider_quantity_local" // Q
	// BasisProviderUnitDebit is a provider-reported nonmonetary request debit
	// (for example credits consumed). It is not provider money P and cannot be
	// rated or converted without an explicit later rule.
	BasisProviderUnitDebit ValuationBasis = "provider_unit_debit"
	BasisProviderReported  ValuationBasis = "provider_reported"  // P
	BasisStatementReported ValuationBasis = "statement_reported" // S
	BasisCustomerPolicy    ValuationBasis = "customer_policy"    // R
	BasisAllocatedCost     ValuationBasis = "allocated_cost"
	// Short aliases are useful at adapter boundaries while preserving the
	// descriptive wire values above.
	BasisE          = BasisLocalExpected
	BasisQ          = BasisProviderQuantityLocal
	BasisD          = BasisProviderUnitDebit
	BasisP          = BasisProviderReported
	BasisS          = BasisStatementReported
	BasisR          = BasisCustomerPolicy
	ValuationBasisE = BasisLocalExpected
	ValuationBasisQ = BasisProviderQuantityLocal
	ValuationBasisD = BasisProviderUnitDebit
	ValuationBasisP = BasisProviderReported
	ValuationBasisS = BasisStatementReported
	ValuationBasisR = BasisCustomerPolicy
)

func (b ValuationBasis) IsKnown() bool {
	switch b {
	case BasisLocalExpected, BasisProviderQuantityLocal, BasisProviderUnitDebit, BasisProviderReported,
		BasisStatementReported, BasisCustomerPolicy, BasisAllocatedCost:
		return true
	default:
		return false
	}
}

func (b ValuationBasis) Validate() error {
	if !b.IsKnown() {
		return fmt.Errorf("%w: unknown basis %q", ErrInvalidValuation, b)
	}
	return nil
}

// RoundingScope records the boundary at which exact rating output entered the
// integer nano-money ledger. Sum-of-rounded-lines and round-of-total therefore
// remain distinguishable during replay.
type RoundingScope string

const (
	RoundingScopeLine   RoundingScope = "line"
	RoundingScopeCall   RoundingScope = "call"
	RoundingScopePeriod RoundingScope = "period"
	RoundingLine                      = RoundingScopeLine
	RoundingCall                      = RoundingScopeCall
	RoundingPeriod                    = RoundingScopePeriod
)

func (s RoundingScope) IsKnown() bool {
	switch s {
	case RoundingScopeLine, RoundingScopeCall, RoundingScopePeriod:
		return true
	default:
		return false
	}
}

// Completeness is independent of whether the economic value is estimated or
// reported. An estimated complete quantity and an incomplete provider claim
// must not collapse into one status.
type Completeness string

const (
	CompletenessComplete    Completeness = "complete"
	CompletenessPartial     Completeness = "partial"
	CompletenessUnknown     Completeness = "unknown"
	CompletenessConflict    Completeness = "conflict"
	CompletenessUnavailable Completeness = "unavailable"
)

// CoverageStatus is a domain vocabulary alias used by reconciliation clients.
type CoverageStatus = Completeness

const (
	CoverageComplete    = CompletenessComplete
	CoveragePartial     = CompletenessPartial
	CoverageUnknown     = CompletenessUnknown
	CoverageConflict    = CompletenessConflict
	CoverageUnavailable = CompletenessUnavailable
)

func (c Completeness) IsKnown() bool {
	switch c {
	case CompletenessComplete, CompletenessPartial, CompletenessUnknown, CompletenessConflict, CompletenessUnavailable:
		return true
	default:
		return false
	}
}

// FixedFeeScope identifies the trusted commercial scope of a fixed fee. It is
// deliberately separate from B-leg inference quantities so a call fee is not
// repeated once for every retry.
type FixedFeeScope string

const (
	FixedFeeScopeSubmission FixedFeeScope = "submission"
	FixedFeeScopeCall       FixedFeeScope = "call"
	FixedFeeScopePeriod     FixedFeeScope = "period"
)

func (s FixedFeeScope) IsKnown() bool {
	switch s {
	case FixedFeeScopeSubmission, FixedFeeScopeCall, FixedFeeScopePeriod:
		return true
	default:
		return false
	}
}

// FixedFeeIdentity is the stable identity of a non-quantity charge line.
// Scope is part of identity and must be frozen by the commercial policy.
type FixedFeeIdentity struct {
	ID      string        `json:"id"`
	Scope   FixedFeeScope `json:"scope"`
	Version string        `json:"version,omitempty"`
}

// RatingLineStatus records why a line is or is not economically usable. An
// explicit free rule is intentionally distinct from missing/unsupported rate
// evidence; neither missing state is represented as a zero amount.
type RatingLineStatus string

const (
	RatingLineRated              RatingLineStatus = "rated"
	RatingLineExplicitFree       RatingLineStatus = "explicit_free"
	RatingLineProviderReported   RatingLineStatus = "provider_reported"
	RatingLineRateMissing        RatingLineStatus = "rate_missing"
	RatingLineRateUnsupported    RatingLineStatus = "rate_unsupported"
	RatingLineCurrencyMismatch   RatingLineStatus = "currency_mismatch"
	RatingLineQuantityIncomplete RatingLineStatus = "quantity_incomplete"
	RatingLineCoverageIncomplete RatingLineStatus = "coverage_incomplete"
)

func (s RatingLineStatus) IsKnown() bool {
	switch s {
	case "", RatingLineRated, RatingLineExplicitFree, RatingLineProviderReported,
		RatingLineRateMissing, RatingLineRateUnsupported, RatingLineCurrencyMismatch,
		RatingLineQuantityIncomplete, RatingLineCoverageIncomplete:
		return true
	default:
		return false
	}
}

func (f FixedFeeIdentity) Validate() error {
	if err := validatePublicRef("fixed fee id", f.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	if !f.Scope.IsKnown() {
		return fmt.Errorf("%w: unknown fixed fee scope %q", ErrInvalidValuation, f.Scope)
	}
	if f.Version != "" {
		if err := validatePublicRef("fixed fee version", f.Version); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
		}
	}
	return nil
}

// AdjustmentRef links a line to an immutable later correction or selection
// operation. It carries no journal command or provider payload.
type AdjustmentRef struct {
	StoreID     string `json:"store_id"`
	ValuationID string `json:"valuation_id"`
	Revision    uint64 `json:"revision"`
	OperationID string `json:"operation_id"`
}

func (r AdjustmentRef) Validate() error {
	for name, value := range map[string]string{
		"adjustment store_id": r.StoreID, "adjustment valuation_id": r.ValuationID,
		"adjustment operation_id": r.OperationID,
	} {
		if err := validatePublicRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
		}
	}
	if r.Revision == 0 {
		return fmt.Errorf("%w: adjustment revision required", ErrInvalidValuation)
	}
	return nil
}

// CurrencyConversionRef identifies an explicit frozen reporting conversion.
// Native and reporting amounts remain separate; a missing conversion is never
// interpreted as rate one.
type CurrencyConversionRef struct {
	ID           string            `json:"id"`
	Version      string            `json:"version"`
	FromCurrency string            `json:"from_currency"`
	ToCurrency   string            `json:"to_currency"`
	Rate         *metering.Decimal `json:"rate"`
}

func (r CurrencyConversionRef) Validate() error {
	for name, value := range map[string]string{
		"conversion id": r.ID, "conversion version": r.Version,
	} {
		if err := validatePublicRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
		}
	}
	from, err := NormalizeCurrency(r.FromCurrency)
	if err != nil {
		return fmt.Errorf("%w: conversion from currency: %v", ErrInvalidValuation, err)
	}
	to, err := NormalizeCurrency(r.ToCurrency)
	if err != nil {
		return fmt.Errorf("%w: conversion to currency: %v", ErrInvalidValuation, err)
	}
	if from == to {
		return fmt.Errorf("%w: conversion currencies must differ", ErrInvalidValuation)
	}
	if r.Rate == nil {
		return fmt.Errorf("%w: conversion rate required", ErrInvalidValuation)
	}
	n, err := r.Rate.Normalize()
	if err != nil {
		return fmt.Errorf("%w: conversion rate: %v", ErrInvalidValuation, err)
	}
	if n.Coefficient == "0" || strings.HasPrefix(n.Coefficient, "-") {
		return fmt.Errorf("%w: conversion rate must be positive", ErrInvalidValuation)
	}
	return nil
}

func (r CurrencyConversionRef) Clone() CurrencyConversionRef {
	out := r
	if r.Rate != nil {
		rate := *r.Rate
		out.Rate = &rate
	}
	return out
}

// LineItem is one exact economic line. Component and FixedFee are mutually
// exclusive identities; no provider aggregate is decomposed by this DTO.
type LineItem struct {
	ID              string                 `json:"id"`
	RuleID          string                 `json:"rule_id"`
	ItemID          string                 `json:"item_id"`
	Component       *metering.ComponentKey `json:"component,omitempty"`
	FixedFee        *FixedFeeIdentity      `json:"fixed_fee,omitempty"`
	Quantity        *metering.Decimal      `json:"quantity,omitempty"`
	Unit            string                 `json:"unit"`
	UnitPrice       *metering.Decimal      `json:"unit_price,omitempty"`
	RateNumerator   *metering.Decimal      `json:"rate_numerator,omitempty"`
	RateDenominator *metering.Decimal      `json:"rate_denominator,omitempty"`
	Amount          *metering.Decimal      `json:"amount,omitempty"`
	// AmountNumerator and AmountDenominator preserve an exact bounded rational
	// when Amount cannot be represented as a terminating Decimal. They are
	// mutually required and never contain a rounded approximation.
	AmountNumerator       string                    `json:"amount_numerator,omitempty"`
	AmountDenominator     string                    `json:"amount_denominator,omitempty"`
	RoundedAmount         *Money                    `json:"rounded_amount,omitempty"`
	RoundingScope         RoundingScope             `json:"rounding_scope,omitempty"`
	RoundingPolicy        RoundingPolicy            `json:"rounding_policy,omitempty"`
	IncludedUnit          bool                      `json:"included_unit,omitempty"`
	Status                RatingLineStatus          `json:"status,omitempty"`
	ReportedAggregate     bool                      `json:"reported_aggregate,omitempty"`
	ChargeKind            string                    `json:"charge_kind,omitempty"`
	SourceObservationRefs []metering.ObservationRef `json:"source_observation_refs,omitempty"`
	AdjustmentRefs        []AdjustmentRef           `json:"adjustment_refs,omitempty"`
}

func (l LineItem) Validate() error {
	for name, value := range map[string]string{"line id": l.ID, "line rule_id": l.RuleID, "line item_id": l.ItemID, "line unit": l.Unit} {
		if err := validatePublicRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
		}
	}
	if (l.Component == nil) == (l.FixedFee == nil) && !l.ReportedAggregate {
		return fmt.Errorf("%w: line requires exactly one component or fixed-fee identity", ErrInvalidValuation)
	}
	if l.ReportedAggregate && (l.Component != nil || l.FixedFee != nil) {
		return fmt.Errorf("%w: aggregate line cannot carry component/fixed-fee identity", ErrInvalidValuation)
	}
	if !l.Status.IsKnown() {
		return fmt.Errorf("%w: unknown line status %q", ErrInvalidValuation, l.Status)
	}
	if l.ChargeKind != "" {
		if err := validatePublicRef("line charge kind", l.ChargeKind); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
		}
	}
	if l.Component != nil {
		if err := l.Component.Validate(); err != nil {
			return fmt.Errorf("%w: line component: %v", ErrInvalidValuation, err)
		}
		if l.Unit != l.Component.Unit {
			return fmt.Errorf("%w: line unit %q differs from component unit %q", ErrInvalidValuation, l.Unit, l.Component.Unit)
		}
	}
	if l.FixedFee != nil {
		if err := l.FixedFee.Validate(); err != nil {
			return err
		}
	}
	if l.Quantity != nil {
		normalized, err := l.Quantity.Normalize()
		if err != nil {
			return fmt.Errorf("%w: line quantity: %v", ErrInvalidValuation, err)
		}
		if (l.Unit == metering.UnitToken || l.Unit == metering.UnitCount) && normalized.Scale != 0 {
			return fmt.Errorf("%w: line quantity for %s must be an exact integer", ErrInvalidValuation, l.Unit)
		}
	}
	for name, value := range map[string]*metering.Decimal{
		"unit price": l.UnitPrice, "rate numerator": l.RateNumerator, "rate denominator": l.RateDenominator, "amount": l.Amount,
	} {
		if value != nil {
			if _, err := value.Normalize(); err != nil {
				return fmt.Errorf("%w: line %s: %v", ErrInvalidValuation, name, err)
			}
		}
	}
	if (l.AmountNumerator == "") != (l.AmountDenominator == "") {
		return fmt.Errorf("%w: amount numerator and denominator must be supplied together", ErrInvalidValuation)
	}
	if l.AmountNumerator != "" {
		if err := validateRationalParts("amount", l.AmountNumerator, l.AmountDenominator); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
		}
	}
	if l.Amount != nil && l.AmountNumerator != "" {
		return fmt.Errorf("%w: decimal and rational amount are mutually exclusive", ErrInvalidValuation)
	}
	if l.UnitPrice != nil && (l.RateNumerator != nil || l.RateDenominator != nil) {
		return fmt.Errorf("%w: unit price and rational rate are mutually exclusive", ErrInvalidValuation)
	}
	if (l.RateNumerator == nil) != (l.RateDenominator == nil) {
		return fmt.Errorf("%w: rate numerator and denominator must be supplied together", ErrInvalidValuation)
	}
	if l.RateDenominator != nil {
		zero, err := l.RateDenominator.Normalize()
		if err != nil {
			return fmt.Errorf("%w: rate denominator: %v", ErrInvalidValuation, err)
		}
		if zero.Coefficient == "0" || strings.HasPrefix(zero.Coefficient, "-") {
			return fmt.Errorf("%w: rate denominator must be positive", ErrInvalidValuation)
		}
	}
	if l.RoundedAmount != nil {
		if err := validateV2Money("rounded amount", *l.RoundedAmount); err != nil {
			return fmt.Errorf("%w: rounded amount: %v", ErrInvalidValuation, err)
		}
		if l.RoundingScope != "" && l.RoundingScope != RoundingScopeLine {
			return fmt.Errorf("%w: non-line rounding scope %q cannot carry rounded amount", ErrInvalidValuation, l.RoundingScope)
		}
	}
	if l.RoundingScope != "" && !l.RoundingScope.IsKnown() {
		return fmt.Errorf("%w: unknown rounding scope %q", ErrInvalidValuation, l.RoundingScope)
	}
	if !l.RoundingPolicy.IsKnown() {
		return fmt.Errorf("%w: unknown rounding policy %q", ErrInvalidValuation, l.RoundingPolicy)
	}
	if l.RoundingScope != "" && l.RoundingPolicy == RoundingUnspecified {
		return fmt.Errorf("%w: rounding policy required with scope", ErrInvalidValuation)
	}
	if l.RoundingPolicy != RoundingUnspecified && l.RoundingScope == "" {
		return fmt.Errorf("%w: rounding scope required with policy", ErrInvalidValuation)
	}
	if (l.Amount != nil || l.AmountNumerator != "") && (l.RoundingScope == "" || l.RoundingScope == RoundingScopeLine) && (l.RoundedAmount == nil || !l.RoundedAmount.Present) {
		return fmt.Errorf("%w: line-scoped exact amount requires rounded amount", ErrInvalidValuation)
	}
	if len(l.SourceObservationRefs) > MaxValuationRefs || len(l.AdjustmentRefs) > MaxValuationRefs {
		return fmt.Errorf("%w: line reference bound exceeded", ErrInvalidValuation)
	}
	if err := validateObservationRefCollection(l.SourceObservationRefs, "", "source observation"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	if err := validateAdjustmentRefCollection(l.AdjustmentRefs, "adjustment"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	return nil
}

func (l LineItem) Clone() LineItem {
	out := l
	if l.Component != nil {
		component := l.Component.Clone()
		out.Component = &component
	}
	if l.FixedFee != nil {
		fee := *l.FixedFee
		out.FixedFee = &fee
	}
	cloneDecimal := func(in *metering.Decimal) *metering.Decimal {
		if in == nil {
			return nil
		}
		value := *in
		return &value
	}
	out.Quantity = cloneDecimal(l.Quantity)
	out.UnitPrice = cloneDecimal(l.UnitPrice)
	out.RateNumerator = cloneDecimal(l.RateNumerator)
	out.RateDenominator = cloneDecimal(l.RateDenominator)
	out.Amount = cloneDecimal(l.Amount)
	if l.RoundedAmount != nil {
		money := *l.RoundedAmount
		out.RoundedAmount = &money
	}
	out.SourceObservationRefs = append([]metering.ObservationRef(nil), l.SourceObservationRefs...)
	out.AdjustmentRefs = append([]AdjustmentRef(nil), l.AdjustmentRefs...)
	return out
}

func normalizeDecimal(in *metering.Decimal) (*metering.Decimal, error) {
	if in == nil {
		return nil, nil
	}
	n, err := in.Normalize()
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func validateRationalParts(name, numerator, denominator string) error {
	if numerator == "" || denominator == "" {
		return fmt.Errorf("%s numerator and denominator are required", name)
	}
	parse := func(part, label string, allowNegative bool) (*big.Int, error) {
		if len(part) > MaxValuationRationalDigits+1 {
			return nil, fmt.Errorf("%s %s exceeds %d digits", name, label, MaxValuationRationalDigits)
		}
		if !allowNegative && strings.HasPrefix(part, "-") {
			return nil, fmt.Errorf("%s denominator must be positive", name)
		}
		value, ok := new(big.Int).SetString(part, 10)
		if !ok || value.String() != part {
			return nil, fmt.Errorf("%s %s must be a canonical integer", name, label)
		}
		digits := part
		digits = strings.TrimPrefix(digits, "-")
		if len(digits) > MaxValuationRationalDigits {
			return nil, fmt.Errorf("%s %s exceeds %d digits", name, label, MaxValuationRationalDigits)
		}
		return value, nil
	}
	n, err := parse(numerator, "numerator", true)
	if err != nil {
		return err
	}
	d, err := parse(denominator, "denominator", false)
	if err != nil {
		return err
	}
	if d.Sign() <= 0 {
		return fmt.Errorf("%s denominator must be positive", name)
	}
	if new(big.Int).GCD(nil, nil, n, d).Cmp(big.NewInt(1)) != 0 {
		return fmt.Errorf("%s rational must be reduced", name)
	}
	return nil
}

// CurrencyTotal keeps exact native-currency value and checked rounded money
// together. ReportingAmount is optional and requires Conversion.
type CurrencyTotal struct {
	Currency          string                 `json:"currency"`
	Amount            *metering.Decimal      `json:"amount,omitempty"`
	AmountNumerator   string                 `json:"amount_numerator,omitempty"`
	AmountDenominator string                 `json:"amount_denominator,omitempty"`
	RoundedAmount     Money                  `json:"rounded_amount,omitzero"`
	ReportingAmount   Money                  `json:"reporting_amount,omitzero"`
	Conversion        *CurrencyConversionRef `json:"conversion,omitempty"`
}

func (t CurrencyTotal) Validate() error {
	cur, err := NormalizeCurrency(t.Currency)
	if err != nil {
		return fmt.Errorf("%w: total currency: %v", ErrInvalidValuation, err)
	}
	if t.Amount != nil {
		if _, err := t.Amount.Normalize(); err != nil {
			return fmt.Errorf("%w: total amount: %v", ErrInvalidValuation, err)
		}
	}
	if (t.AmountNumerator == "") != (t.AmountDenominator == "") {
		return fmt.Errorf("%w: total amount numerator and denominator must be supplied together", ErrInvalidValuation)
	}
	if t.AmountNumerator != "" {
		if err := validateRationalParts("total amount", t.AmountNumerator, t.AmountDenominator); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
		}
	}
	if t.Amount != nil && t.AmountNumerator != "" {
		return fmt.Errorf("%w: decimal and rational total amount are mutually exclusive", ErrInvalidValuation)
	}
	if err := validateV2Money("rounded total", t.RoundedAmount); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	if t.RoundedAmount.Present {
		if t.RoundedAmount.Currency != cur {
			return fmt.Errorf("%w: rounded total currency %q differs from %q", ErrInvalidValuation, t.RoundedAmount.Currency, cur)
		}
	}
	if err := validateV2Money("reporting total", t.ReportingAmount); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	if t.ReportingAmount.Present {
		if t.Amount == nil && t.AmountNumerator == "" {
			return fmt.Errorf("%w: reporting total requires native amount", ErrInvalidValuation)
		}
		if t.ReportingAmount.Currency == cur || t.Conversion == nil {
			return fmt.Errorf("%w: reporting currency requires a distinct explicit conversion", ErrInvalidValuation)
		}
		if err := t.Conversion.Validate(); err != nil {
			return err
		}
		if t.Conversion.FromCurrency != cur || t.Conversion.ToCurrency != t.ReportingAmount.Currency {
			return fmt.Errorf("%w: reporting conversion currency mismatch", ErrInvalidValuation)
		}
	} else if t.Conversion != nil {
		return fmt.Errorf("%w: conversion requires reporting amount", ErrInvalidValuation)
	}
	return nil
}

// ConvertReporting consumes the exact native total and applies the explicit
// frozen conversion rate once at reporting nano-unit precision. A rational
// native amount is preserved through multiplication; it is never converted
// through a terminating decimal approximation first.
func (t CurrencyTotal) ConvertReporting(policy RoundingPolicy) (Money, error) {
	if t.Conversion == nil {
		return Money{}, fmt.Errorf("%w: conversion required", ErrInvalidValuation)
	}
	if err := t.Conversion.Validate(); err != nil {
		return Money{}, err
	}
	nativeCurrency, err := NormalizeCurrency(t.Currency)
	if err != nil {
		return Money{}, fmt.Errorf("%w: total currency: %v", ErrInvalidValuation, err)
	}
	if t.Conversion.FromCurrency != nativeCurrency {
		return Money{}, fmt.Errorf("%w: reporting conversion source currency mismatch", ErrInvalidValuation)
	}
	targetCurrency, err := NormalizeCurrency(t.Conversion.ToCurrency)
	if err != nil {
		return Money{}, fmt.Errorf("%w: reporting conversion target currency: %v", ErrInvalidValuation, err)
	}
	native, err := t.nativeAmountRat()
	if err != nil {
		return Money{}, err
	}
	rate, err := t.Conversion.Rate.ToRat()
	if err != nil {
		return Money{}, fmt.Errorf("%w: conversion rate: %v", ErrInvalidValuation, err)
	}
	nanos := new(big.Rat).Mul(native, rate)
	nanos.Mul(nanos, new(big.Rat).SetInt64(1_000_000_000))
	units, err := RoundToInt64(nanos, policy)
	if err != nil {
		return Money{}, err
	}
	return Money{NanoUnits: units, Currency: targetCurrency, Present: true}, nil
}

func (t CurrencyTotal) nativeAmountRat() (*big.Rat, error) {
	if t.Amount != nil && t.AmountNumerator != "" {
		return nil, fmt.Errorf("%w: decimal and rational total amount are mutually exclusive", ErrInvalidValuation)
	}
	if t.Amount != nil {
		value, err := t.Amount.ToRat()
		if err != nil {
			return nil, fmt.Errorf("%w: total amount: %v", ErrInvalidValuation, err)
		}
		return value, nil
	}
	if t.AmountNumerator == "" || t.AmountDenominator == "" {
		return nil, fmt.Errorf("%w: exact native amount required", ErrInvalidValuation)
	}
	if err := validateRationalParts("total amount", t.AmountNumerator, t.AmountDenominator); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	numerator, _ := new(big.Int).SetString(t.AmountNumerator, 10)
	denominator, _ := new(big.Int).SetString(t.AmountDenominator, 10)
	return new(big.Rat).SetFrac(numerator, denominator), nil
}

func (t CurrencyTotal) Clone() CurrencyTotal {
	out := t
	if t.Amount != nil {
		amount := *t.Amount
		out.Amount = &amount
	}
	if t.Conversion != nil {
		conversion := t.Conversion.Clone()
		out.Conversion = &conversion
	}
	return out
}

// Valuation is an immutable, derived E/Q/P/S/R record. It contains safe
// economic metadata and references, never raw prompts, responses or provider
// payloads.
type Valuation struct {
	ID                   string                       `json:"id"`
	Version              uint32                       `json:"version"`
	Perspective          metering.EconomicPerspective `json:"perspective"`
	Basis                ValuationBasis               `json:"basis"`
	Subject              metering.SubjectRef          `json:"subject"`
	Scope                string                       `json:"scope,omitempty"`
	InputObservations    []metering.ObservationRef    `json:"input_observations"`
	InputSetHash         string                       `json:"input_set_hash,omitempty"`
	Rater                RatingSnapshotRef            `json:"rater"`
	RaterContent         *SnapshotContentRef          `json:"rater_content,omitempty"`
	Tariff               RatingSnapshotRef            `json:"tariff"`
	TariffContent        *SnapshotContentRef          `json:"tariff_content,omitempty"`
	Policy               PolicySnapshotRef            `json:"policy"`
	PolicyContent        *SnapshotContentRef          `json:"policy_content,omitempty"`
	QualifierSnapshot    string                       `json:"qualifier_snapshot,omitempty"`
	QualifierSnapshotRef *SnapshotContentRef          `json:"qualifier_snapshot_ref,omitempty"`
	// EffectiveQualifiers are the canonical values actually used for rule
	// selection. They are immutable valuation identity, not merely retrieval
	// metadata, because changing one can select a different rate.
	EffectiveQualifiers []metering.Dimension         `json:"effective_qualifiers,omitempty"`
	Payer               metering.PaymentParty        `json:"payer,omitzero"`
	Lines               []LineItem                   `json:"lines"`
	Totals              []CurrencyTotal              `json:"totals"`
	Completeness        Completeness                 `json:"completeness"`
	MissingObservations []metering.ObservationRef    `json:"missing_observations,omitempty"`
	CoverageRefs        []metering.ChargeCoverageRef `json:"coverage_refs,omitempty"`
	// AllocationCoverageRefs names the exact immutable non-request allocation
	// revisions this valuation's monetary amount proves included. It is the
	// allocation counterpart of CoverageRefs: a conserved resource/statement
	// allocation is only proven contained in this valuation when it is named
	// here by exact allocation identity (store, allocation, version and payload
	// hash). It carries identity only, never money, and never turns an
	// allocation into provider-charge evidence or a request debit.
	AllocationCoverageRefs  []AllocationRef          `json:"allocation_coverage_refs,omitempty"`
	SupportAdvisoryContexts []SupportAdvisoryContext `json:"support_advisory_contexts,omitempty"`
	SupportAdvisory         *SupportAdvisoryReport   `json:"support_advisory,omitempty"`
	CreatedAt               time.Time                `json:"created_at"`
}

func (v Valuation) Validate() error {
	if err := validatePublicRef("valuation id", v.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	if v.Version == 0 {
		return fmt.Errorf("%w: version required", ErrInvalidValuation)
	}
	if err := v.Perspective.Validate(); err != nil {
		return fmt.Errorf("%w: perspective: %v", ErrInvalidValuation, err)
	}
	if err := v.Basis.Validate(); err != nil {
		return err
	}
	if err := v.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %v", ErrInvalidValuation, err)
	}
	if err := v.Payer.Validate(); err != nil {
		return fmt.Errorf("%w: payer: %v", ErrInvalidValuation, err)
	}
	if v.Scope != "" {
		if err := validatePublicRef("valuation scope", v.Scope); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
		}
	}
	if err := validateInputSetIdentity("input set hash", v.InputSetHash, false, ErrInvalidValuation); err != nil {
		return err
	}
	if !isZeroRatingRef(v.Rater) {
		if err := validateRaterRef("rater", v.Rater); err != nil {
			return err
		}
	}
	if !isZeroRatingRef(v.Tariff) {
		if err := validateRatingRef("tariff", v.Tariff); err != nil {
			return err
		}
	}
	if !isZeroPolicyRef(v.Policy) {
		if err := validatePolicyRef("policy", v.Policy); err != nil {
			return err
		}
	}
	// Derived planes need the corresponding immutable snapshot context. Direct
	// provider/statement claims may legitimately arrive before a local tariff or
	// customer policy exists, so their references remain optional.
	if (v.Basis == BasisLocalExpected || v.Basis == BasisProviderQuantityLocal) && isZeroRatingRef(v.Tariff) {
		return fmt.Errorf("%w: %s valuation requires tariff snapshot", ErrInvalidValuation, v.Basis)
	}
	if (v.Basis == BasisLocalExpected || v.Basis == BasisProviderQuantityLocal || v.Basis == BasisCustomerPolicy) && isZeroRatingRef(v.Rater) {
		return fmt.Errorf("%w: %s valuation requires rater snapshot", ErrInvalidValuation, v.Basis)
	}
	if v.Basis == BasisCustomerPolicy && isZeroPolicyRef(v.Policy) {
		return fmt.Errorf("%w: customer-policy valuation requires policy snapshot", ErrInvalidValuation)
	}
	if err := validateQualifierSnapshot(v.QualifierSnapshot, v.QualifierSnapshotRef, ErrInvalidValuation); err != nil {
		return err
	}
	if err := validateDimensions(v.EffectiveQualifiers, ErrInvalidValuation); err != nil {
		return err
	}
	if len(v.EffectiveQualifiers) > 0 && v.QualifierSnapshotRef == nil {
		return fmt.Errorf("%w: effective qualifiers require qualifier snapshot content reference", ErrInvalidValuation)
	}
	if err := validateValuationSnapshotContext(v); err != nil {
		return err
	}
	if !v.Completeness.IsKnown() {
		return fmt.Errorf("%w: unknown completeness %q", ErrInvalidValuation, v.Completeness)
	}
	if v.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at required", ErrInvalidValuation)
	}
	if len(v.InputObservations) > MaxValuationRefs || len(v.MissingObservations) > MaxValuationRefs || len(v.CoverageRefs) > MaxValuationRefs {
		return fmt.Errorf("%w: observation reference bound exceeded", ErrInvalidValuation)
	}
	if len(v.AllocationCoverageRefs) > MaxAllocationRefs {
		return fmt.Errorf("%w: allocation coverage reference bound exceeded", ErrInvalidValuation)
	}
	seenAllocationCoverage := make(map[string]struct{}, len(v.AllocationCoverageRefs))
	for i, ref := range v.AllocationCoverageRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: allocation coverage ref %d: %v", ErrInvalidValuation, i, err)
		}
		if ref.StoreID != v.Subject.StoreID {
			return fmt.Errorf("%w: allocation coverage ref store mismatch", ErrInvalidValuation)
		}
		key := allocationRefSortKey(ref)
		if _, exists := seenAllocationCoverage[key]; exists {
			return fmt.Errorf("%w: duplicate allocation coverage ref", ErrInvalidValuation)
		}
		seenAllocationCoverage[key] = struct{}{}
	}
	if len(v.InputObservations) == 0 {
		return fmt.Errorf("%w: at least one input observation reference required", ErrInvalidValuation)
	}
	if err := validateObservationRefCollection(v.InputObservations, v.Subject.StoreID, "input observation"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	if err := validateObservationRefCollection(v.MissingObservations, v.Subject.StoreID, "missing observation"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	if err := validateCoverageRefCollection(v.CoverageRefs, v.Subject.StoreID, "coverage"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	if len(v.Lines) > MaxValuationLines {
		return fmt.Errorf("%w: line bound exceeded", ErrInvalidValuation)
	}
	seenLines := make(map[string]struct{}, len(v.Lines))
	for i, line := range v.Lines {
		if err := line.Validate(); err != nil {
			return fmt.Errorf("%w: line %d: %v", ErrInvalidValuation, i, err)
		}
		if v.Completeness == CompletenessComplete && (line.Amount == nil && line.AmountNumerator == "") {
			return fmt.Errorf("%w: complete valuation line %d lacks exact amount", ErrInvalidValuation, i)
		}
		lineNeedsRound := line.RoundingScope == "" || line.RoundingScope == RoundingScopeLine
		if v.Completeness == CompletenessComplete && lineNeedsRound && (line.RoundedAmount == nil || !line.RoundedAmount.Present) {
			return fmt.Errorf("%w: complete valuation line %d lacks line-rounded amount", ErrInvalidValuation, i)
		}
		if _, ok := seenLines[line.ID]; ok {
			return fmt.Errorf("%w: duplicate line id %q", ErrInvalidValuation, line.ID)
		}
		seenLines[line.ID] = struct{}{}
		if err := validateObservationRefCollection(line.SourceObservationRefs, v.Subject.StoreID, "line source observation"); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
		}
		for _, ref := range line.AdjustmentRefs {
			if ref.StoreID != v.Subject.StoreID {
				return fmt.Errorf("%w: line adjustment ref store mismatch", ErrInvalidValuation)
			}
		}
	}
	if len(v.Totals) > MaxValuationTotals {
		return fmt.Errorf("%w: total bound exceeded", ErrInvalidValuation)
	}
	seenCurrencies := make(map[string]struct{}, len(v.Totals))
	for i, total := range v.Totals {
		if err := total.Validate(); err != nil {
			return fmt.Errorf("%w: total %d: %v", ErrInvalidValuation, i, err)
		}
		if v.Completeness == CompletenessComplete && ((total.Amount == nil && total.AmountNumerator == "") || !total.RoundedAmount.Present) {
			return fmt.Errorf("%w: complete valuation total %d lacks exact and rounded amounts", ErrInvalidValuation, i)
		}
		currency, _ := NormalizeCurrency(total.Currency)
		if _, ok := seenCurrencies[currency]; ok {
			return fmt.Errorf("%w: duplicate currency total %q", ErrInvalidValuation, currency)
		}
		seenCurrencies[currency] = struct{}{}
	}
	if v.Completeness == CompletenessComplete && len(v.Totals) == 0 {
		return fmt.Errorf("%w: complete valuation requires totals", ErrInvalidValuation)
	}
	return v.validateSupportAdvisory()
}

func validateRatingRef(name string, ref RatingSnapshotRef) error {
	if err := validateVersionRef(name, ref.VersionRef); err != nil {
		return err
	}
	if ref.RaterID != "" {
		if err := validatePublicRef(name+" rater_id", ref.RaterID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
		}
	}
	return nil
}

func validateRaterRef(name string, ref RatingSnapshotRef) error {
	if err := validateRatingRef(name, ref); err != nil {
		return err
	}
	if err := validatePublicRef(name+" rater_id", ref.RaterID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	return nil
}

func isZeroRatingRef(ref RatingSnapshotRef) bool {
	return ref.ID == "" && ref.Version == "" && ref.RaterID == "" && ref.EffectiveAt.IsZero() && ref.FetchedAt.IsZero()
}

func isZeroPolicyRef(ref PolicySnapshotRef) bool {
	return ref.ID == "" && ref.Version == "" && ref.PolicyID == "" && ref.EffectiveAt.IsZero() && ref.FetchedAt.IsZero()
}

func validatePolicyRef(name string, ref PolicySnapshotRef) error {
	if err := validateVersionRef(name, ref.VersionRef); err != nil {
		return err
	}
	if err := validatePublicRef(name+" policy_id", ref.PolicyID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	return nil
}

func validateVersionRef(name string, ref VersionRef) error {
	if err := validatePublicRef(name+" id", ref.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	if err := validatePublicRef(name+" version", ref.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	return nil
}

// Clone makes all nested valuation data independent of the source.
func (v Valuation) Clone() Valuation {
	out := v
	out.SupportAdvisoryContexts = slices.Clone(v.SupportAdvisoryContexts)
	out.SupportAdvisory = v.SupportAdvisory.Clone()
	out.Subject = v.Subject.Clone()
	out.RaterContent = cloneSnapshotContentRef(v.RaterContent)
	out.TariffContent = cloneSnapshotContentRef(v.TariffContent)
	out.PolicyContent = cloneSnapshotContentRef(v.PolicyContent)
	out.QualifierSnapshotRef = cloneSnapshotContentRef(v.QualifierSnapshotRef)
	out.EffectiveQualifiers = append([]metering.Dimension(nil), v.EffectiveQualifiers...)
	out.InputObservations = append([]metering.ObservationRef(nil), v.InputObservations...)
	out.MissingObservations = append([]metering.ObservationRef(nil), v.MissingObservations...)
	out.CoverageRefs = append([]metering.ChargeCoverageRef(nil), v.CoverageRefs...)
	out.AllocationCoverageRefs = append([]AllocationRef(nil), v.AllocationCoverageRefs...)
	if v.Lines != nil {
		out.Lines = make([]LineItem, len(v.Lines))
		for i, line := range v.Lines {
			out.Lines[i] = line.Clone()
		}
	}
	if v.Totals != nil {
		out.Totals = make([]CurrencyTotal, len(v.Totals))
		for i, total := range v.Totals {
			out.Totals[i] = total.Clone()
		}
	}
	return out
}

// Canonical returns a validated deep copy with unordered refs, lines and
// totals sorted by their stable identities.
func (v Valuation) Canonical() (Valuation, error) {
	if err := v.Validate(); err != nil {
		return Valuation{}, err
	}
	out := v.Clone()
	out.canonicalizeSupportAdvisory()
	for i := range out.Lines {
		if out.Lines[i].Component != nil {
			component, err := out.Lines[i].Component.Normalize()
			if err != nil {
				return Valuation{}, err
			}
			out.Lines[i].Component = &component
		}
		var err error
		if out.Lines[i].Quantity, err = normalizeDecimal(out.Lines[i].Quantity); err != nil {
			return Valuation{}, err
		}
		if out.Lines[i].UnitPrice, err = normalizeDecimal(out.Lines[i].UnitPrice); err != nil {
			return Valuation{}, err
		}
		if out.Lines[i].RateNumerator, err = normalizeDecimal(out.Lines[i].RateNumerator); err != nil {
			return Valuation{}, err
		}
		if out.Lines[i].RateDenominator, err = normalizeDecimal(out.Lines[i].RateDenominator); err != nil {
			return Valuation{}, err
		}
		if out.Lines[i].Amount, err = normalizeDecimal(out.Lines[i].Amount); err != nil {
			return Valuation{}, err
		}
	}
	for i := range out.Totals {
		amount, err := normalizeDecimal(out.Totals[i].Amount)
		if err != nil {
			return Valuation{}, err
		}
		out.Totals[i].Amount = amount
		if out.Totals[i].Conversion != nil && out.Totals[i].Conversion.Rate != nil {
			rate, err := normalizeDecimal(out.Totals[i].Conversion.Rate)
			if err != nil {
				return Valuation{}, err
			}
			out.Totals[i].Conversion.Rate = rate
		}
	}
	slices.SortFunc(out.InputObservations, compareObservationRef)
	slices.SortFunc(out.EffectiveQualifiers, func(a, b metering.Dimension) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Value, b.Value)
	})
	slices.SortFunc(out.MissingObservations, compareObservationRef)
	slices.SortFunc(out.CoverageRefs, func(a, b metering.ChargeCoverageRef) int {
		return strings.Compare(coverageKey(a), coverageKey(b))
	})
	slices.SortFunc(out.AllocationCoverageRefs, func(a, b AllocationRef) int {
		return strings.Compare(allocationRefSortKey(a), allocationRefSortKey(b))
	})
	slices.SortFunc(out.Lines, func(a, b LineItem) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(out.Totals, func(a, b CurrencyTotal) int { return strings.Compare(a.Currency, b.Currency) })
	for i := range out.Lines {
		slices.SortFunc(out.Lines[i].SourceObservationRefs, compareObservationRef)
		slices.SortFunc(out.Lines[i].AdjustmentRefs, func(a, b AdjustmentRef) int {
			return strings.Compare(adjustmentKey(a), adjustmentKey(b))
		})
	}
	return out, nil
}

func compareObservationRef(a, b metering.ObservationRef) int {
	return strings.Compare(observationRefKey(a), observationRefKey(b))
}

func observationRefKey(ref metering.ObservationRef) string {
	return fmt.Sprintf("%s\x00%s\x00%020d\x00%s", ref.StoreID, ref.ObservationID, ref.Revision, ref.PayloadHash)
}

func coverageKey(ref metering.ChargeCoverageRef) string {
	return fmt.Sprintf("%s\x00%s\x00%020d\x00%s\x00%s", ref.Ref.StoreID, ref.Ref.ObservationID, ref.Ref.Revision, ref.Ref.ChargeItemID, ref.Relation)
}

func adjustmentKey(ref AdjustmentRef) string {
	return fmt.Sprintf("%s\x00%s\x00%020d\x00%s", ref.StoreID, ref.ValuationID, ref.Revision, ref.OperationID)
}

type valuationWire Valuation

// CanonicalJSON is the deterministic wire form used for durable identity.
func (v Valuation) CanonicalJSON() ([]byte, error) {
	n, err := v.Canonical()
	if err != nil {
		return nil, err
	}
	return json.Marshal(valuationWire(n))
}

func (v Valuation) MarshalJSON() ([]byte, error) { return v.CanonicalJSON() }

func (v Valuation) CanonicalKey() string {
	b, err := v.CanonicalJSON()
	if err != nil {
		return ""
	}
	return string(b)
}

func (v Valuation) Fingerprint() string {
	b, err := v.CanonicalJSON()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
