package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestAttemptRatingQuantitiesAreNonMoneyQuotaOnly(t *testing.T) {
	t.Parallel()
	out := 8
	tests := []struct {
		name string
		in   accountingpreflight.Decision
		want []metering.Quantity
	}{
		{
			name: "input always present without output",
			in:   accountingpreflight.Decision{Count: accountingapp.CountResult{InputTokens: 12}},
			want: []metering.Quantity{{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 12, Present: true}},
		},
		{
			name: "explicit adjusted max output",
			in: accountingpreflight.Decision{
				Count:                   accountingapp.CountResult{InputTokens: 1},
				AdjustedMaxOutputTokens: &out,
			},
			want: []metering.Quantity{
				{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
				{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 8, Present: true},
			},
		},
		{
			name: "positive counted output without adjusted max",
			in:   accountingpreflight.Decision{Count: accountingapp.CountResult{InputTokens: 1, OutputTokens: 4}},
			want: []metering.Quantity{
				{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
				{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 4, Present: true},
			},
		},
		{
			name: "cache and reasoning only when positive",
			in: accountingpreflight.Decision{Count: accountingapp.CountResult{
				InputTokens: 1, CacheReadTokens: 2, CacheWriteTokens: 3, ReasoningTokens: 4,
			}},
			want: []metering.Quantity{
				{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
				{Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken, Value: 2, Present: true},
				{Component: metering.ComponentCacheWriteInputToken, Unit: metering.UnitToken, Value: 3, Present: true},
				{Component: metering.ComponentReasoningOutputToken, Unit: metering.UnitToken, Value: 4, Present: true},
			},
		},
		{
			name: "zero cache and reasoning omitted",
			in:   accountingpreflight.Decision{Count: accountingapp.CountResult{InputTokens: 1, CacheReadTokens: 0, ReasoningTokens: 0}},
			want: []metering.Quantity{{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := attemptRatingQuantities(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("quantities = %+v, want %+v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("quantity[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
				if got[i].Unit != metering.UnitToken {
					t.Fatalf("quantity[%d] must be token-only, got %+v", i, got[i])
				}
			}
		})
	}
}

func TestExplicitOutputQuantityOmitsUnknownAndNegative(t *testing.T) {
	t.Parallel()
	if _, ok := explicitOutputQuantity(accountingpreflight.Decision{}); ok {
		t.Fatal("unknown output must stay absent")
	}
	neg := -1
	if _, ok := explicitOutputQuantity(accountingpreflight.Decision{AdjustedMaxOutputTokens: &neg}); ok {
		t.Fatal("negative adjusted max must stay absent")
	}
	zero := 0
	got, ok := explicitOutputQuantity(accountingpreflight.Decision{AdjustedMaxOutputTokens: &zero})
	if !ok || got.Value != 0 || !got.Present {
		t.Fatalf("explicit zero max must be present, got %+v ok=%v", got, ok)
	}
}

func TestFinalOperatorAttemptQuantitiesPreferBackendIngress(t *testing.T) {
	t.Parallel()
	decision := accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 9, OutputTokens: 7},
	}
	fallback := finalOperatorAttemptQuantities(context.Background(), "b-1", decision)
	if len(fallback) != 2 || fallback[0].Value != 9 || fallback[1].Component != metering.ComponentOutputToken {
		t.Fatalf("nil holder must use preflight, got %+v", fallback)
	}

	holder := &checkpoint.RequestHolder{BackendIngress: map[string]*checkpoint.Snapshot{
		"b-1": {Public: metering.Checkpoint{Quantities: []metering.Quantity{
			{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 40, Present: true},
		}}},
	}}
	ctx := withMeteringHolder(context.Background(), holder)
	merged := finalOperatorAttemptQuantities(ctx, "b-1", decision)
	if len(merged) != 2 {
		t.Fatalf("missing output must be filled from preflight, got %+v", merged)
	}
	if merged[0].Component != metering.ComponentInputToken || merged[0].Value != 40 {
		t.Fatalf("backend ingress input must win, got %+v", merged)
	}
	if merged[1].Component != metering.ComponentOutputToken || merged[1].Value != 7 {
		t.Fatalf("preflight output fill = %+v", merged)
	}

	holder.BackendIngress["b-1"].Public.Quantities = []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 40, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 3, Present: true},
	}
	complete := finalOperatorAttemptQuantities(ctx, "b-1", decision)
	if len(complete) != 2 || complete[1].Value != 3 {
		t.Fatalf("present backend output must not be replaced, got %+v", complete)
	}
}
