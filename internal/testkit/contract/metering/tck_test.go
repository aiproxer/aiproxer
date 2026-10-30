package metering_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/core/metering/normalize"
	contract "github.com/aiproxer/aiproxer/internal/testkit/contract/metering"
)

func TestTCK_ActualNormalizerAndReducer(t *testing.T) {
	t.Parallel()
	contract.Run(t, contract.Env{
		Normalize: normalize.Normalize,
		Reduce:    aggregate.ApplyObservations,
	})
}
