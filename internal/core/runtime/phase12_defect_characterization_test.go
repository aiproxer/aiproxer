package runtime

import (
	"testing"

	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Phase 2.3: spend uses resolved unknown-output bounds; totals keep inclusion schema.

func TestAttemptAuthorityUsageAmount_infersTotalWithoutSubcomponentDoubleCount(t *testing.T) {
	t.Parallel()

	ev := aipapi.Event{
		Kind:             aipapi.EventUsageDelta,
		InputTokens:      100,
		OutputTokens:     20,
		CacheReadTokens:  40,
		CacheWriteTokens: 10,
		ReasoningTokens:  5,
		TotalTokens:      0,
	}
	got := attemptAuthorityUsageAmount(ev, usagelimit.Amount{Unit: usagelimit.AmountUnitTotalTokens})
	if got.Value != 120 {
		t.Fatalf("total=%d want 120 (input+output; cache/reasoning are subcomponents)", got.Value)
	}
}

func TestAttemptAuthorityPreflightUsage_explicitZeroTotalRemainsPresent(t *testing.T) {
	t.Parallel()

	usage := attemptAuthorityPreflightUsage(accountingpreflight.Decision{
		Allowed: true,
		Count: accountingapp.CountResult{
			InputTokens:        100,
			OutputTokens:       20,
			TotalTokens:        0,
			TotalTokensPresent: true,
		},
	})
	if !usage.TotalTokensPresent {
		t.Fatal("TotalTokensPresent=false; explicit zero total must bridge as present")
	}
	if usage.TotalTokens != 0 {
		t.Fatalf("TotalTokens=%d want 0", usage.TotalTokens)
	}
	got, ok := usage.AmountForUnit(usagelimit.AmountUnitTotalTokens)
	if !ok {
		t.Fatal("AmountForUnit(total) unavailable")
	}
	if got.Value != 0 {
		t.Fatalf("authoritative zero total Amount=%d want 0 (must not infer input+output=%d)", got.Value, usage.InputTokens+usage.OutputTokens)
	}
}

func TestAttemptAuthorityPreflightUsage_omittedTotalInfersInclusion(t *testing.T) {
	t.Parallel()

	usage := attemptAuthorityPreflightUsage(accountingpreflight.Decision{
		Allowed: true,
		Count: accountingapp.CountResult{
			InputTokens:  100,
			OutputTokens: 20,
			// TotalTokens omitted / absent → infer input+output.
		},
	})
	if usage.TotalTokensPresent {
		t.Fatal("TotalTokensPresent=true; omitted total must remain absent")
	}
	got, ok := usage.AmountForUnit(usagelimit.AmountUnitTotalTokens)
	if !ok {
		t.Fatal("AmountForUnit(total) unavailable")
	}
	if got.Value != 120 {
		t.Fatalf("inferred total=%d want 120", got.Value)
	}
}
