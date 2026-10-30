package controlplane

import (
	"fmt"
	"strings"
)

// TokenQuantityInput is a safe quantity observation without raw content
// (requirements 4.7, 14.6, 17.5).
type TokenQuantityInput struct {
	Component string `json:"component,omitempty"`
	Unit      string `json:"unit,omitempty"`
	Value     int64  `json:"value,omitempty"`
	Present   bool   `json:"present"`
}

// MoneyAmountInput is a currency-tagged amount with explicit presence (6.2).
type MoneyAmountInput struct {
	NanoUnits int64  `json:"nano_units,omitempty"`
	Currency  string `json:"currency,omitempty"`
	Present   bool   `json:"present"`
}

// CustomerChargeInput holds independently queryable customer charge facts (5.7).
type CustomerChargeInput struct {
	Money                 MoneyAmountInput   `json:"money,omitzero"`
	FrontendIngressTokens TokenQuantityInput `json:"frontend_ingress_tokens,omitzero"`
	FrontendEgressTokens  TokenQuantityInput `json:"frontend_egress_tokens,omitzero"`
	RatingVersion         VersionRef         `json:"rating_version,omitzero"`
}

// OperatorCostInput holds independently queryable operator cost facts (5.7).
type OperatorCostInput struct {
	Money                MoneyAmountInput   `json:"money,omitzero"`
	BackendIngressTokens TokenQuantityInput `json:"backend_ingress_tokens,omitzero"`
	BackendEgressTokens  TokenQuantityInput `json:"backend_egress_tokens,omitzero"`
	ProviderReportedCost MoneyAmountInput   `json:"provider_reported_cost,omitzero"`
	RatingVersion        VersionRef         `json:"rating_version,omitzero"`
}

// CompressionReportInput exposes transformation lineage without raw content
// (requirements 4.7, 14.6).
type CompressionReportInput struct {
	FrontendInput   TokenQuantityInput `json:"frontend_input,omitzero"`
	BackendInput    TokenQuantityInput `json:"backend_input,omitzero"`
	DeliveredOutput TokenQuantityInput `json:"delivered_output,omitzero"`
	ProviderOutput  TokenQuantityInput `json:"provider_output,omitzero"`
	ProviderCost    MoneyAmountInput   `json:"provider_cost,omitzero"`
}

// RoutingOverheadInput holds routing overhead separately from direct attempt
// costs (5.7).
type RoutingOverheadInput struct {
	AttemptCount        TokenQuantityInput `json:"attempt_count,omitzero"`
	NonSurfacedAttempts TokenQuantityInput `json:"non_surfaced_attempts,omitzero"`
	OverheadCost        MoneyAmountInput   `json:"overhead_cost,omitzero"`
}

// ReportCalculationType names an explicit cross-plane calculation. Customer
// charge and operator cost must never merge without one of these types (14.7).
type ReportCalculationType string

const (
	ReportCalculationGrossMargin        ReportCalculationType = "gross_margin"
	ReportCalculationNetExposure        ReportCalculationType = "net_exposure"
	ReportCalculationCompressionSavings ReportCalculationType = "compression_token_savings"
	ReportCalculationRoutingOverhead    ReportCalculationType = "routing_overhead_total"
)

// IsKnown reports whether t is a documented report calculation type.
func (t ReportCalculationType) IsKnown() bool {
	switch t {
	case ReportCalculationGrossMargin, ReportCalculationNetExposure,
		ReportCalculationCompressionSavings, ReportCalculationRoutingOverhead:
		return true
	}
	return false
}

// CalculatedAmount is the only DTO that may combine customer and operator
// economics. It always carries an explicit ReportCalculationType (14.7).
type CalculatedAmount struct {
	Calculation ReportCalculationType `json:"calculation"`
	Money       MoneyAmountInput      `json:"money,omitzero"`
	Quantity    TokenQuantityInput    `json:"quantity,omitzero"`
}

// ReportCompleteness classifies whether reconstructed report inputs have the
// ingress facts required for a complete dual-plane view (task 3.5 / design
// migration note: historical rows without ingress are incomplete).
type ReportCompleteness string

const (
	ReportCompletenessComplete   ReportCompleteness = "complete"
	ReportCompletenessIncomplete ReportCompleteness = "incomplete"
)

// ReportLegacyProvenance names how reconstructed inputs relate to legacy
// historical metering without silent reinterpretation.
type ReportLegacyProvenance string

const (
	ReportLegacyProvenanceNone                     ReportLegacyProvenance = ""
	ReportLegacyProvenanceHistoricalWithoutIngress ReportLegacyProvenance = "historical_without_ingress"
)

// DualPlaneReportInputs groups independent source inputs for query/report
// consumers. It intentionally omits any merged total field (14.7).
type DualPlaneReportInputs struct {
	Customer         CustomerChargeInput    `json:"customer"`
	Operator         OperatorCostInput      `json:"operator"`
	Compression      CompressionReportInput `json:"compression,omitzero"`
	RoutingOverhead  RoutingOverheadInput   `json:"routing_overhead,omitzero"`
	Completeness     ReportCompleteness     `json:"completeness,omitempty"`
	LegacyProvenance ReportLegacyProvenance `json:"legacy_provenance,omitempty"`
}

// ValidateCalculatedAmount rejects amounts that omit an explicit calculation
// type (14.7).
func ValidateCalculatedAmount(a CalculatedAmount) error {
	if !a.Calculation.IsKnown() {
		return fmt.Errorf("controlplane: calculated amount requires explicit report calculation type")
	}
	if !a.Money.Present && !a.Quantity.Present {
		return fmt.Errorf("controlplane: calculated amount requires present money or quantity")
	}
	return nil
}

// CalculateGrossMargin returns customer charge minus operator cost using an
// explicit gross_margin calculation (14.7).
func CalculateGrossMargin(customer CustomerChargeInput, operator OperatorCostInput) (CalculatedAmount, error) {
	if !customer.Money.Present || !operator.Money.Present {
		return CalculatedAmount{}, fmt.Errorf("controlplane: gross margin requires present customer charge and operator cost")
	}
	if err := requireSameCurrency(customer.Money, operator.Money); err != nil {
		return CalculatedAmount{}, err
	}
	diff, ok := subMoneyChecked(customer.Money.NanoUnits, operator.Money.NanoUnits)
	if !ok {
		return CalculatedAmount{}, fmt.Errorf("controlplane: gross margin underflow")
	}
	out := CalculatedAmount{
		Calculation: ReportCalculationGrossMargin,
		Money: MoneyAmountInput{
			NanoUnits: diff,
			Currency:  customer.Money.Currency,
			Present:   true,
		},
	}
	return out, ValidateCalculatedAmount(out)
}

// CalculateCompressionTokenSavings returns frontend minus backend input tokens
// via an explicit compression_token_savings calculation (4.7, 14.6).
func CalculateCompressionTokenSavings(in CompressionReportInput) (CalculatedAmount, error) {
	if !in.FrontendInput.Present || !in.BackendInput.Present {
		return CalculatedAmount{}, fmt.Errorf("controlplane: compression savings requires present frontend and backend input quantities")
	}
	if in.FrontendInput.Value < in.BackendInput.Value {
		return CalculatedAmount{}, fmt.Errorf("controlplane: compression savings requires frontend input >= backend input")
	}
	out := CalculatedAmount{
		Calculation: ReportCalculationCompressionSavings,
		Quantity: TokenQuantityInput{
			Component: in.FrontendInput.Component,
			Unit:      in.FrontendInput.Unit,
			Value:     in.FrontendInput.Value - in.BackendInput.Value,
			Present:   true,
		},
	}
	return out, ValidateCalculatedAmount(out)
}

// CalculateRoutingOverheadTotal returns non-surfaced attempt overhead cost via
// an explicit routing_overhead_total calculation (5.7, 14.7).
func CalculateRoutingOverheadTotal(in RoutingOverheadInput) (CalculatedAmount, error) {
	if !in.OverheadCost.Present {
		return CalculatedAmount{}, fmt.Errorf("controlplane: routing overhead requires present overhead cost")
	}
	out := CalculatedAmount{
		Calculation: ReportCalculationRoutingOverhead,
		Money: MoneyAmountInput{
			NanoUnits: in.OverheadCost.NanoUnits,
			Currency:  in.OverheadCost.Currency,
			Present:   true,
		},
	}
	if in.NonSurfacedAttempts.Present {
		out.Quantity = in.NonSurfacedAttempts
	}
	return out, ValidateCalculatedAmount(out)
}

func requireSameCurrency(a, b MoneyAmountInput) error {
	ac := strings.TrimSpace(a.Currency)
	bc := strings.TrimSpace(b.Currency)
	if ac == "" || bc == "" {
		return fmt.Errorf("controlplane: currency required")
	}
	if ac != bc {
		return fmt.Errorf("controlplane: currency mismatch %q vs %q", ac, bc)
	}
	return nil
}

func subMoneyChecked(a, b int64) (int64, bool) {
	if b > a {
		return 0, false
	}
	return a - b, true
}
