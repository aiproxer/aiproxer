package economics

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Repair6_RationalNativeTotalValidatesAndConvertsExactlyOnce(t *testing.T) {
	t.Parallel()
	nativeRounded := Money{NanoUnits: 333333333, Currency: "USD", Present: true}
	reporting := Money{NanoUnits: 666666667, Currency: "EUR", Present: true}
	rate := decimalForRepair6(t, "2")
	total := CurrencyTotal{
		Currency:          "USD",
		AmountNumerator:   "1",
		AmountDenominator: "3",
		RoundedAmount:     nativeRounded,
		ReportingAmount:   reporting,
		Conversion: &CurrencyConversionRef{
			ID: "fx-repair6", Version: "v1", FromCurrency: "USD", ToCurrency: "EUR", Rate: rate,
		},
	}
	if err := total.Validate(); err != nil {
		t.Fatalf("rational native total with reporting conversion rejected: %v", err)
	}
	converted, err := total.ConvertReporting(RoundingHalfAwayFromZero)
	if err != nil {
		t.Fatalf("ConvertReporting: %v", err)
	}
	if converted != reporting {
		t.Fatalf("reporting conversion=%+v, want %+v", converted, reporting)
	}
}

func decimalForRepair6(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("parse decimal %q: %v", raw, err)
	}
	return &value
}
