package controlplane

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// DualPlaneReportInputsFromFacts reconstructs customer/operator/compression/
// routing-overhead report inputs solely from metering facts. Missing ingress
// is classified incomplete; quantities are never invented.
func DualPlaneReportInputsFromFacts(facts []metering.Fact) (DualPlaneReportInputs, error) {
	var out DualPlaneReportInputs
	seenFEIngress := false
	seenBEIngress := false
	seenCustomer := false
	seenOperator := false

	type attemptAcc struct {
		surfacedNo bool
		moneyNano  int64
		currency   string
		moneySet   bool
	}
	attempts := map[string]*attemptAcc{}
	attemptKey := func(f metering.Fact) string {
		if id := strings.TrimSpace(f.Correlation.AttemptID); id != "" {
			return id
		}
		if id := strings.TrimSpace(f.Correlation.BLegID); id != "" {
			return id
		}
		return strings.TrimSpace(f.StreamID)
	}

	addTokens := func(dst *TokenQuantityInput, component string, value int64) {
		if !dst.Present {
			*dst = TokenQuantityInput{Component: component, Unit: string(metering.UnitToken), Value: value, Present: true}
			return
		}
		dst.Value += value
	}
	addMoney := func(dst *MoneyAmountInput, nano int64, currency string) error {
		currency = strings.TrimSpace(currency)
		if !dst.Present {
			*dst = MoneyAmountInput{NanoUnits: nano, Currency: currency, Present: true}
			return nil
		}
		if err := requireSameCurrency(*dst, MoneyAmountInput{NanoUnits: nano, Currency: currency, Present: true}); err != nil {
			return err
		}
		sum, ok := addInt64Checked(dst.NanoUnits, nano)
		if !ok {
			return fmt.Errorf("controlplane: money overflow")
		}
		dst.NanoUnits = sum
		return nil
	}
	quantityValue := func(f metering.Fact, component string) (int64, bool) {
		for _, q := range f.Quantities {
			if q.Present && q.Component == component {
				return q.Value, true
			}
		}
		return 0, false
	}

	for _, f := range facts {
		switch f.Perspective {
		case metering.PerspectiveCustomer:
			seenCustomer = true
		case metering.PerspectiveOperator:
			seenOperator = true
		}
		switch f.Boundary {
		case metering.BoundaryFrontendIngress:
			seenFEIngress = true
		case metering.BoundaryBackendIngress:
			seenBEIngress = true
		}

		switch {
		case f.Perspective == metering.PerspectiveCustomer && f.Boundary == metering.BoundaryFrontendIngress:
			if v, ok := quantityValue(f, metering.ComponentInputToken); ok {
				addTokens(&out.Customer.FrontendIngressTokens, string(metering.ComponentInputToken), v)
				addTokens(&out.Compression.FrontendInput, string(metering.ComponentInputToken), v)
			}
		case f.Perspective == metering.PerspectiveCustomer && f.Boundary == metering.BoundaryFrontendEgress:
			if v, ok := quantityValue(f, metering.ComponentOutputToken); ok {
				addTokens(&out.Customer.FrontendEgressTokens, string(metering.ComponentOutputToken), v)
				addTokens(&out.Compression.DeliveredOutput, string(metering.ComponentOutputToken), v)
			}
			if f.Money != nil && f.Money.Present {
				if err := addMoney(&out.Customer.Money, f.Money.NanoUnits, f.Money.Currency); err != nil {
					return DualPlaneReportInputs{}, err
				}
			}
		case f.Perspective == metering.PerspectiveOperator && f.Boundary == metering.BoundaryBackendIngress:
			if v, ok := quantityValue(f, metering.ComponentInputToken); ok {
				addTokens(&out.Operator.BackendIngressTokens, string(metering.ComponentInputToken), v)
				addTokens(&out.Compression.BackendInput, string(metering.ComponentInputToken), v)
			}
			key := attemptKey(f)
			if key != "" {
				if _, ok := attempts[key]; !ok {
					attempts[key] = &attemptAcc{}
				}
			}
		case f.Perspective == metering.PerspectiveOperator && f.Boundary == metering.BoundaryBackendEgress:
			if v, ok := quantityValue(f, metering.ComponentOutputToken); ok {
				addTokens(&out.Operator.BackendEgressTokens, string(metering.ComponentOutputToken), v)
				addTokens(&out.Compression.ProviderOutput, string(metering.ComponentOutputToken), v)
			}
			if f.Money != nil && f.Money.Present {
				if err := addMoney(&out.Operator.Money, f.Money.NanoUnits, f.Money.Currency); err != nil {
					return DualPlaneReportInputs{}, err
				}
				if err := addMoney(&out.Compression.ProviderCost, f.Money.NanoUnits, f.Money.Currency); err != nil {
					return DualPlaneReportInputs{}, err
				}
			}
			key := attemptKey(f)
			if key != "" {
				acc, ok := attempts[key]
				if !ok {
					acc = &attemptAcc{}
					attempts[key] = acc
				}
				if f.Surfaced == metering.SurfacedNo {
					acc.surfacedNo = true
					if f.Money != nil && f.Money.Present {
						if acc.moneySet {
							if err := requireSameCurrency(
								MoneyAmountInput{NanoUnits: acc.moneyNano, Currency: acc.currency, Present: true},
								MoneyAmountInput{NanoUnits: f.Money.NanoUnits, Currency: f.Money.Currency, Present: true},
							); err != nil {
								return DualPlaneReportInputs{}, err
							}
							sum, ok := addInt64Checked(acc.moneyNano, f.Money.NanoUnits)
							if !ok {
								return DualPlaneReportInputs{}, fmt.Errorf("controlplane: money overflow")
							}
							acc.moneyNano = sum
						} else {
							acc.moneyNano = f.Money.NanoUnits
							acc.currency = f.Money.Currency
							acc.moneySet = true
						}
					}
				}
			}
		}
	}

	out.RoutingOverhead.AttemptCount = TokenQuantityInput{
		Component: "request", Unit: "count", Value: int64(len(attempts)), Present: len(attempts) > 0,
	}
	var nonSurfaced int64
	var overhead MoneyAmountInput
	for _, acc := range attempts {
		if !acc.surfacedNo {
			continue
		}
		nonSurfaced++
		if acc.moneySet {
			if err := addMoney(&overhead, acc.moneyNano, acc.currency); err != nil {
				return DualPlaneReportInputs{}, err
			}
		}
	}
	if nonSurfaced > 0 {
		out.RoutingOverhead.NonSurfacedAttempts = TokenQuantityInput{
			Component: "request", Unit: "count", Value: nonSurfaced, Present: true,
		}
	}
	if overhead.Present {
		out.RoutingOverhead.OverheadCost = overhead
	}

	if len(facts) == 0 {
		out.Completeness = ReportCompletenessIncomplete
		return out, nil
	}
	customerMissing := seenCustomer && !seenFEIngress
	operatorMissing := seenOperator && !seenBEIngress
	if customerMissing || operatorMissing {
		out.Completeness = ReportCompletenessIncomplete
		out.LegacyProvenance = ReportLegacyProvenanceHistoricalWithoutIngress
	} else {
		out.Completeness = ReportCompletenessComplete
	}
	return out, nil
}

func addInt64Checked(a, b int64) (int64, bool) {
	if b > 0 && a > (1<<63-1)-b {
		return 0, false
	}
	if b < 0 && a < (-1<<63)-b {
		return 0, false
	}
	return a + b, true
}
