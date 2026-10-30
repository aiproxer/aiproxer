package plane_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/metering/plane"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestMoneyFromUsageEvent_ExplicitPresenceOnly(t *testing.T) {
	t.Parallel()
	if plane.MoneyFromUsageEvent(aipapi.Event{CostNanoUnits: 99, Currency: "USD", CostSource: "provider_reported"}) != nil {
		t.Fatal("nonzero cost without CostPresent must stay absent")
	}
	got := plane.MoneyFromUsageEvent(aipapi.Event{CostPresent: true, CostNanoUnits: 0, Currency: "EUR"})
	if got == nil || !got.Present || got.NanoUnits != 0 || got.Currency != "EUR" {
		t.Fatalf("authoritative zero lost: %+v", got)
	}
}

func TestQuantitiesFromUsageEvent_EmptyUnmarkedUsageDeltaOmits(t *testing.T) {
	t.Parallel()
	qs := plane.QuantitiesFromUsageEvent(aipapi.Event{Kind: aipapi.EventUsageDelta})
	if len(qs) != 0 {
		t.Fatalf("all-zero unmarked UsageDelta must omit quantities; got %+v", qs)
	}
}

func TestQuantitiesFromUsageEvent_LegacyUnmarkedNonzeroOnly(t *testing.T) {
	t.Parallel()
	qs := plane.QuantitiesFromUsageEvent(aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: 3,
	})
	if len(qs) != 2 {
		t.Fatalf("want input + derived total only; got %+v", qs)
	}
	in, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentInputToken)
	if !ok || in != 3 {
		t.Fatalf("input=%d ok=%v", in, ok)
	}
	total, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentTotalToken)
	if !ok || total != 3 {
		t.Fatalf("derived total=%d ok=%v", total, ok)
	}
}

func TestQuantitiesFromUsageEvent_ExplicitZeroPresenceRetained(t *testing.T) {
	t.Parallel()
	qs := plane.QuantitiesFromUsageEvent(aipapi.Event{
		Kind: aipapi.EventUsageDelta,
		UsagePresence: aipapi.UsagePresence{
			InputTokens: true, OutputTokens: true, TotalTokens: true,
		},
	})
	in, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentInputToken)
	if !ok || in != 0 {
		t.Fatalf("authoritative zero input lost: ok=%v v=%d", ok, in)
	}
}

func TestCustomerPlaneUsageEvent_StripsProviderScopesAndMoney(t *testing.T) {
	t.Parallel()
	ev := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		CostNanoUnits: 999,
		Currency:      "USD",
		CostPresent:   true,
		CostSource:    string(aipapi.UsageSourceProviderReported),
		MessageIndex:  3,
		RawUsageJSON:  `{"provider":"x"}`,
		UsageScopes: []aipapi.ScopedUsageDelta{
			{
				InputTokens: 40, OutputTokens: 12, TotalTokens: 52,
				Accounting: aipapi.UsageAccountingMetadata{
					Plane: aipapi.UsagePlaneClientVisible, Source: aipapi.UsageSourceLocalTokenizer, Authority: aipapi.UsageAuthorityEstimated,
				},
			},
			{
				InputTokens: 200, OutputTokens: 80, TotalTokens: 280,
				Accounting: aipapi.UsageAccountingMetadata{
					Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
				},
			},
		},
	}
	got := plane.CustomerPlaneUsageEvent(ev)
	if got.InputTokens != 40 || got.OutputTokens != 12 {
		t.Fatalf("customer plane tokens in=%d out=%d", got.InputTokens, got.OutputTokens)
	}
	if got.CostPresent || got.CostNanoUnits != 0 || got.Currency != "" {
		t.Fatalf("customer plane must strip money; got %+v", got)
	}
	// Projection is a fresh UsageDelta: aipapi.Event has no trace/model identity fields,
	// and provider RawUsageJSON must not leak onto the customer plane.
	if got.MessageIndex != 0 || got.RawUsageJSON != "" {
		t.Fatalf("customer projection must not carry provider envelope fields: %+v", got)
	}
}
