package aggregate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestApplyObservationsOverflowRetainsDecimalCause proves a reducer overflow keeps
// the decimal cause reachable, so a caller can tell which bound was exceeded.
func TestApplyObservationsOverflowRetainsDecimalCause(t *testing.T) {
	t.Parallel()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken}
	max := strings.Repeat("9", metering.MaxDecimalCoefficientDigits)
	first := observation("cause-a", "cause-stream", 1, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-cause"), measure(key, max))
	second := observation("cause-b", "cause-stream", 2, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-cause"), measure(key, max))
	_, err := aggregate.ApplyObservations([]metering.Observation{first, second})
	if !errors.Is(err, aggregate.ErrOverflow) || !errors.Is(err, metering.ErrDecimalOverflow) {
		t.Fatalf("overflow error = %v, want ErrOverflow and the decimal cause", err)
	}
}
