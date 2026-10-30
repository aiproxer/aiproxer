package controlplane_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestDualPlaneReportInputsExposeIndependentCustomerAndOperator(t *testing.T) {
	t.Parallel()
	inputs := controlplane.DualPlaneReportInputs{
		Customer: controlplane.CustomerChargeInput{
			Money: controlplane.MoneyAmountInput{NanoUnits: 500, Currency: "USD", Present: true},
			FrontendIngressTokens: controlplane.TokenQuantityInput{
				Component: "input_token", Unit: "token", Value: 100, Present: true,
			},
		},
		Operator: controlplane.OperatorCostInput{
			Money: controlplane.MoneyAmountInput{NanoUnits: 200, Currency: "USD", Present: true},
			BackendIngressTokens: controlplane.TokenQuantityInput{
				Component: "input_token", Unit: "token", Value: 40, Present: true,
			},
		},
		Compression: controlplane.CompressionReportInput{
			FrontendInput:   controlplane.TokenQuantityInput{Component: "input_token", Unit: "token", Value: 100, Present: true},
			BackendInput:    controlplane.TokenQuantityInput{Component: "input_token", Unit: "token", Value: 40, Present: true},
			DeliveredOutput: controlplane.TokenQuantityInput{Component: "output_token", Unit: "token", Value: 20, Present: true},
			ProviderOutput:  controlplane.TokenQuantityInput{Component: "output_token", Unit: "token", Value: 22, Present: true},
			ProviderCost:    controlplane.MoneyAmountInput{NanoUnits: 200, Currency: "USD", Present: true},
		},
		RoutingOverhead: controlplane.RoutingOverheadInput{
			AttemptCount: controlplane.TokenQuantityInput{Component: "request", Unit: "count", Value: 2, Present: true},
			OverheadCost: controlplane.MoneyAmountInput{NanoUnits: 15, Currency: "USD", Present: true},
		},
	}
	raw := roundTripJSON(t, inputs)
	for _, key := range []string{`"customer"`, `"operator"`, `"compression"`, `"routing_overhead"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("expected key %s in %s", key, raw)
		}
	}
	for _, forbidden := range []string{`"total"`, `"combined"`, `"merged"`, `"gross"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("DualPlaneReportInputs must not carry implicit merged field %q: %s", forbidden, raw)
		}
	}
}

func TestCalculateGrossMarginRequiresExplicitReportCalculation(t *testing.T) {
	t.Parallel()
	customer := controlplane.CustomerChargeInput{
		Money: controlplane.MoneyAmountInput{NanoUnits: 500, Currency: "USD", Present: true},
	}
	operator := controlplane.OperatorCostInput{
		Money: controlplane.MoneyAmountInput{NanoUnits: 200, Currency: "USD", Present: true},
	}
	got, err := controlplane.CalculateGrossMargin(customer, operator)
	if err != nil {
		t.Fatal(err)
	}
	if got.Calculation != controlplane.ReportCalculationGrossMargin {
		t.Fatalf("calculation=%q want gross_margin", got.Calculation)
	}
	if !got.Money.Present || got.Money.NanoUnits != 300 {
		t.Fatalf("money=%#v want present 300", got.Money)
	}
}

func TestCalculateGrossMarginRejectsMissingAmounts(t *testing.T) {
	t.Parallel()
	_, err := controlplane.CalculateGrossMargin(
		controlplane.CustomerChargeInput{},
		controlplane.OperatorCostInput{Money: controlplane.MoneyAmountInput{NanoUnits: 1, Currency: "USD", Present: true}},
	)
	if err == nil {
		t.Fatal("expected error when customer charge absent")
	}
}

func TestCalculateCompressionTokenSavingsWithoutRawContent(t *testing.T) {
	t.Parallel()
	in := controlplane.CompressionReportInput{
		FrontendInput: controlplane.TokenQuantityInput{Component: "input_token", Unit: "token", Value: 100, Present: true},
		BackendInput:  controlplane.TokenQuantityInput{Component: "input_token", Unit: "token", Value: 40, Present: true},
	}
	got, err := controlplane.CalculateCompressionTokenSavings(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Calculation != controlplane.ReportCalculationCompressionSavings {
		t.Fatalf("calculation=%q", got.Calculation)
	}
	if !got.Quantity.Present || got.Quantity.Value != 60 {
		t.Fatalf("quantity=%#v want savings 60", got.Quantity)
	}
}

func TestCalculatedAmountRejectsMissingCalculationType(t *testing.T) {
	t.Parallel()
	err := controlplane.ValidateCalculatedAmount(controlplane.CalculatedAmount{
		Money: controlplane.MoneyAmountInput{NanoUnits: 1, Currency: "USD", Present: true},
	})
	if err == nil {
		t.Fatal("expected validation error without explicit calculation type")
	}
}

func TestDualPlaneReportTypesAvoidForbiddenFields(t *testing.T) {
	t.Parallel()
	forbidden := []string{"Bearer", "APIKey", "Secret", "OAuth", "Header", "Password", "RawPayload", "RawBody", "Prompt", "ProviderPayload"}
	assertNoForbiddenFields(t, controlplane.DualPlaneReportInputs{}, forbidden)
	assertNoForbiddenFields(t, controlplane.CompressionReportInput{}, forbidden)
	assertNoForbiddenFields(t, controlplane.CalculatedAmount{}, forbidden)
}

func TestDualPlaneReportInputsJSONHasNoMergedAmountField(t *testing.T) {
	t.Parallel()
	var m map[string]json.RawMessage
	raw := roundTripJSON(t, controlplane.DualPlaneReportInputs{
		Customer: controlplane.CustomerChargeInput{
			Money: controlplane.MoneyAmountInput{NanoUnits: 1, Currency: "USD", Present: true},
		},
		Operator: controlplane.OperatorCostInput{
			Money: controlplane.MoneyAmountInput{NanoUnits: 2, Currency: "USD", Present: true},
		},
	})
	unmarshalJSON(t, raw, &m)
	for _, key := range []string{"amount", "total", "combined", "merged_cost", "net"} {
		if _, ok := m[key]; ok {
			t.Fatalf("forbidden merged top-level field %q", key)
		}
	}
}
