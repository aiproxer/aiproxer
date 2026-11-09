package metering

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestReduceProviderDebitsReductionFailureRetainsCause proves a reducer failure
// stays reachable through the incomplete sentinel. The incomplete classification
// tells an operator the evidence could not be reduced; the reducer and decimal
// sentinels tell why, and they are the only machine-readable cause.
func TestReduceProviderDebitsReductionFailureRetainsCause(t *testing.T) {
	t.Parallel()

	max := strings.Repeat("9", sdkmetering.MaxDecimalCoefficientDigits)
	first := phase11DebitForCore(t, "reduce-cause-a", max)
	second := phase11DebitForCore(t, "reduce-cause-b", max)

	_, err := ReduceProviderDebits([]sdkmetering.ProviderDebit{first, second})
	if !errors.Is(err, ErrProviderDebitIncomplete) {
		t.Fatalf("reduction error = %v, want ErrProviderDebitIncomplete", err)
	}
	if !errors.Is(err, aggregate.ErrOverflow) {
		t.Fatalf("reduction error = %v, want the reducer overflow cause retained", err)
	}
	if !errors.Is(err, sdkmetering.ErrDecimalOverflow) {
		t.Fatalf("reduction error = %v, want the decimal overflow cause retained", err)
	}
}
